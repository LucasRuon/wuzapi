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

**Última sessão:** 2026-08-09
**Feature ativa:** `anti-ban-guardrails`
**Estado:** spec + context + design escritos. `tasks.md` gerado para a Onda 1 (P1).
Nada implementado ainda.

**Próximo passo:** executar T0 (habilitar `go test` no CI) e seguir as fases de
`.specs/features/anti-ban-guardrails/tasks.md`.

**Contexto que não está no código:**
- O broadcast-app (`../broadcast-app`) é o consumidor principal e hoje carrega a
  única proteção anti-ban existente (jitter 45–75 s). Ela **continua** lá depois
  deste feature — o gateway garante o piso, o app propõe o ritmo.
- Alvo calibrado para listas **opt-in**, 200 msg/dia por instância, multi-tenant.
  Se alguma base deixar de ser opt-in, as quotas precisam cair ~70 %.
