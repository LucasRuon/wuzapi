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

**Última sessão:** 2026-08-10
**Feature ativa:** `anti-ban-guardrails` — branch `feat/anti-ban-guardrails`
**Fase:** 2 de 4 concluída.

**Concluído:**
- T0 `92ffac7` — CI passa a rodar `go test ./... -race`
- T1 `8db8fac` — migration 10 (17 colunas em `users`, tabelas `suppression` e `send_events`)
- T2 `532f883` — `governor.go`: `GateError`, `SendKind`, `GovernorDefaults` + flags/env
- T3 `569e981` — reserva atômica de cota e pacing (`UPDATE` condicional + diagnóstico)
- T4 `c4d332c` — rampa de warmup por dias desde o pareamento
- T5 `b2d51db` — janela horária por instância e poda de `send_events`
- T6 `114bca1` — circuit breaker de ban com expiração preguiçosa + `Acquire` composto

**Próximo passo:** Fase 3 — T7 (`SuppressionStore`), T8 (`matchOptOut`, único `[P]`)
e T9 (`resolveRecipientJID` com cache negativo). Os dois pontos de encaixe já
estão marcados por comentário em `Acquire` (`governor.go`, passos 2 e 6).

**Suíte:** 132 testes de topo (eram 79 ao fim da Fase 1), 0 skips,
`go vet && go build && go test ./... -race` verde. Nenhum arquivo não commitado.

**Desvios registrados:**
- `GateError.WriteTo` usa `banCode` em vez de `code` para o código do ban — o
  envelope de `s.Respond` (`handlers.go:6199`) já ocupa `code` com o status HTTP.
  Marcado com `SPEC_DEVIATION` em `governor.go` e corrigido na spec (BAN-02 AC-2).
- `Acquire` **não** recebe `context.Context`, ao contrário da assinatura do
  `design.md`: todo o acesso a dados do repo é sem contexto e não há cancelamento
  a propagar. Marcado com `SPEC_DEVIATION` em `governor.go`.
- O `UPDATE` do design não tratava `quota_reset_at IS NULL`. Sem isso a primeira
  reserva de uma instância nunca agendaria a virada e a cota jamais zeraria — a
  condição foi adicionada nas três cláusulas.
- `reserve` recebe `*time.Location` explícito (o design não define a assinatura).
  Quem resolve o fuso da instância é o `Acquire`.
- `server` ganhou o campo `governor`, instanciado em `main.go`, porque a poda de
  `send_events` precisa rodar no boot. T11 reusa a mesma instância.

**Lacunas conhecidas:**
- O ramo **Postgres** da migration 10 não é exercitado por teste: `makeTestServer`
  usa SQLite `:memory:` e o repo não tem Postgres em CI. O `ADD COLUMN IF NOT
  EXISTS` está garantido por revisão, não por gate.
- O **tick de 6 h** da poda não tem teste (seria testar `time.Ticker`); só a poda
  em si e a execução no boot estão cobertas.
- BAN-10 AC-6 ("`sent_today` não é devolvido quando o whatsmeow falha") não tem
  teste: não existe caminho de decremento no código. Verificável de fato só em
  T11, quando a chamada ao whatsmeow entra.

**Contexto que não está no código:**
- O broadcast-app (`../broadcast-app`) é o consumidor principal e hoje carrega a
  única proteção anti-ban existente (jitter 45–75 s). Ela **continua** lá depois
  deste feature — o gateway garante o piso, o app propõe o ritmo.
- Alvo calibrado para listas **opt-in**, 200 msg/dia por instância, multi-tenant.
  Se alguma base deixar de ser opt-in, as quotas precisam cair ~70 %.
- O driver `modernc.org/sqlite` serializa `time.Time` preservando o offset e o
  SQLite compara `TIMESTAMP` como texto: **todo timestamp do governor vai e volta
  em UTC**. Misturar fusos faz a virada de cota acontecer horas antes.
- `:memory:` puro dá um banco **vazio** a cada conexão nova do pool. Teste de
  concorrência real usa `file:<nome>?mode=memory&cache=shared`
  (`makeSharedMemoryDB`, em `governor_reserve_test.go`).
