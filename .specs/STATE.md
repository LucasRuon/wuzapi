# Project State

Memória de projeto do fork da WuzAPI. `## Decisions` são restrições de nível de
projeto que features futuras devem respeitar (ou superseder explicitamente).

---

## Decisions

### AD-001 — Contrato de erro dos endpoints de envio

**Status:** active
**Data:** 2026-08-09
**Origem:** `.specs/features/anti-ban-guardrails/design.md`

Todo endpoint `/chat/send/*` usa três status para recusa por guardrail, e cada um
exige uma reação diferente do consumidor:

| Status | Significado | Reação esperada do cliente |
| --- | --- | --- |
| `423 Locked` | Instância travada (ban temporário/permanente, auto-pausa). Corpo: `{code, reason, until}` | Pausar a campanha até `until` |
| `429 Too Many Requests` | Rápido ou volumoso demais agora. Header `Retry-After` | Esperar e **repetir o mesmo destinatário** |
| `422 Unprocessable Entity` | Este destinatário é inválido (não existe no WhatsApp, ou suprimido). Corpo: `{error, ...}` | Pular o destinatário e seguir |

**Por quê:** sem status distintos o cliente não sabe se deve pausar, repetir ou
pular — e a diferença entre repetir e pular é a diferença entre perder mensagens
e duplicá-las.

### AD-002 — Fail-closed em falha de dependência de segurança

**Status:** active
**Data:** 2026-08-09
**Origem:** `.specs/features/anti-ban-guardrails/design.md`

Quando uma verificação que existe para **proteger** (governor, estado de ban,
supressão) não consegue rodar — banco fora, timeout — a resposta é recusar
(`503`), nunca liberar.

**Por quê:** fail-open num guardrail de rate limit converte uma falha de infra
numa rajada sem limite, que é exatamente o cenário que o guardrail existe para
impedir. Não vale para verificações de *melhor esforço* (`IsOnWhatsApp`,
`SendChatPresence`), que degradam silenciosamente.

### AD-003 — Limites de segurança só podem ficar mais restritivos

**Status:** active
**Data:** 2026-08-09
**Origem:** `.specs/features/anti-ban-guardrails/design.md`; precedente em
`broadcast-app/src/app/api/broadcast/route.ts:20-32`

Configuração (flag, env, coluna por instância) pode **aumentar** o intervalo
mínimo entre envios e **diminuir** a quota diária — nunca o contrário. O piso de
45 s e o teto de 200/dia são invariantes do código, não do deploy.

**Por quê:** um limite anti-ban que a config pode relaxar não é um limite. Já é a
regra do broadcast-app; passa a valer no gateway.

---

## Handoff

**Última sessão:** 2026-08-21
**Branch:** `feat/anti-ban-guardrails`
**Feature ativa:** nenhuma — `contact-name-resolution` foi concluída e verificada.

**Concluído nesta sessão — `contact-name-resolution`:**
- T1 `530724b` — `POST /user/contacts/resync` (`FetchAppState` de `critical_unblock_low`,
  full sync) + `case *events.Contact` para não afogar o log
- T2 `6578aec` — `resolveMissingBusinessNames` / `enrichGroupList`: coleta os
  participantes sem nome, deduplica, teto de 64, uma consulta em lote
- T3 `c2630aa` — opt-in `resolveBusiness=true` em `/group/info` e `/group/list`
- Fix `d64e3f1` — dois mutantes sobreviventes apontados pelo Verifier
- Verificação independente: **PASS** (16/16 ACs, 7/7 edge cases, 18/18 mutantes
  mortos). Relatório em `.specs/features/contact-name-resolution/validation.md`

**Próximo passo:** voltar para `anti-ban-guardrails`, Fase 3 — T7 (`SuppressionStore`),
T8 (`matchOptOut`, único `[P]`) e T9 (`resolveRecipientJID` com cache negativo). Os
dois pontos de encaixe seguem marcados por comentário em `Acquire` (`governor.go`,
passos 2 e 6). Fases 1 e 2 do anti-ban continuam concluídas (T0–T6, `92ffac7`..`114bca1`).

**Suíte:** 152 testes de topo (eram 132 no fim da Fase 2 do anti-ban), 0 skips,
`go vet && go build && go test ./... -race` verde. Nenhum arquivo não commitado.

**Desvios registrados (anti-ban, ainda válidos):**
- `GateError.WriteTo` usa `banCode` em vez de `code` para o código do ban — o
  envelope de `s.Respond` já ocupa `code` com o status HTTP. Marcado com
  `SPEC_DEVIATION` em `governor.go` e corrigido na spec (BAN-02 AC-2).
- `Acquire` **não** recebe `context.Context`, ao contrário do `design.md`: todo o
  acesso a dados do repo é sem contexto. Marcado com `SPEC_DEVIATION`.
- O `UPDATE` do design não tratava `quota_reset_at IS NULL`; a condição foi
  adicionada nas três cláusulas.
- `reserve` recebe `*time.Location` explícito; quem resolve o fuso é o `Acquire`.
- `server` ganhou o campo `governor`, instanciado em `main.go`, porque a poda de
  `send_events` precisa rodar no boot.

**Desvio registrado (contact-name-resolution):**
- `/group/list` passou a devolver `"Groups": []` em vez de `null` quando não há
  grupos — efeito de `enrichGroupList` sempre alocar o slice. Aceito e reivindicado
  na spec (P2 AC-2), na mesma direção de `formatBlocklist`.

**Lacunas conhecidas:**
- Anti-ban: o ramo **Postgres** da migration 10 não é exercitado por teste
  (`makeTestServer` usa SQLite `:memory:`); o **tick de 6 h** da poda não tem teste;
  BAN-10 AC-6 só será verificável em T11.
- Contact names: o `case *events.Contact` (`wmiau.go`) e o log do teto de 64 não têm
  gate automatizado — o repo não tem harness para `myEventHandler` nem captura de
  `zerolog`. Ambos declarados na spec.
- Os caminhos HTTP que exigem um cliente whatsmeow real (200 do resync e os 500 de
  sync/contagem) são cobertos só até "rota registrada, auth passa, `no session`".

**Contexto que não está no código:**
- O broadcast-app (`../broadcast-app`) é o consumidor principal e carrega a única
  proteção anti-ban existente (jitter 45–75 s). Ela **continua** lá depois do
  feature — o gateway garante o piso, o app propõe o ritmo.
- Alvo calibrado para listas **opt-in**, 200 msg/dia por instância, multi-tenant.
  Se alguma base deixar de ser opt-in, as quotas precisam cair ~70 %.
- O driver `modernc.org/sqlite` serializa `time.Time` preservando o offset e o
  SQLite compara `TIMESTAMP` como texto: **todo timestamp do governor vai e volta
  em UTC**. Misturar fusos faz a virada de cota acontecer horas antes.
- `:memory:` puro dá um banco **vazio** a cada conexão nova do pool. Teste de
  concorrência real usa `file:<nome>?mode=memory&cache=shared`
  (`makeSharedMemoryDB`, em `governor_reserve_test.go`).
- Nome de participante de grupo tem três fontes possíveis e **nenhuma** é
  consultável sob demanda além do verified name de conta comercial: push name só
  chega embutido em mensagem, e agenda só via app-state sync.
