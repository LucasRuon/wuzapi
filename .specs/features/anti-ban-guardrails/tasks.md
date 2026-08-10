# Anti-Ban Guardrails — Tasks (Onda 1 / P1)

## Execution Protocol (MANDATORY -- do not skip)

Implement these tasks with the `tlc-spec-driven-v3` skill: **activate it by name and
follow its Execute flow and Critical Rules.** Do not search for skill files by
filesystem path. The skill is the source of truth for the full flow (per-task cycle,
sub-agent delegation, adequacy review, Verifier, discrimination sensor).

**If the skill cannot be activated, STOP and tell the user — do not proceed without it.**

---

**Design**: `.specs/features/anti-ban-guardrails/design.md`
**Status**: In Progress — Fase 1 concluída (T0, T1, T2)
**Escopo deste arquivo**: apenas os 18 requisitos P1 (BAN-01 … BAN-18). P2/P3
(auto-pausa, digitação, spintax, fingerprint, health, retomada no app) entram em
`tasks-onda2.md` depois que a Onda 1 estiver verificada.

---

## Test Coverage Matrix

> Gerada do codebase + spec. **Guidelines encontradas: nenhuma** — não há
> `AGENTS.md`, `CLAUDE.md`, `CONTRIBUTING.md`, `Makefile` nem seção de testes no
> `README.md` do fork. Defaults fortes aplicados. Confirme antes de executar.

| Code Layer | Required Test Type | Coverage Expectation | Location Pattern | Run Command |
| --- | --- | --- | --- | --- |
| Lógica de decisão pura (rampa de warmup, janela horária, `matchOptOut`, mapeamento de `ConnectFailureReason`) | unit | Todos os ramos; 1:1 com os ACs da spec; todo edge case listado tem teste | `./<nome>_test.go` (package `main`, raiz) | `go test ./...` |
| Acesso a dados (reserva de cota, `SuppressionStore`, estado de ban) | integration | Caminhos de query principais + erros + concorrência; SQLite `:memory:` via `makeTestServer(t)` | `./<nome>_test.go` | `go test ./...` |
| Handlers HTTP (`/chat/send/*`, `/user/suppress`, `/session/ban/clear`) | e2e | Toda rota tocada: happy path + cada edge case + caminhos de erro, via `httptest` | `./<nome>_test.go` | `go test ./...` |
| Event handler (`myEventHandler`) | integration | Transição de estado por evento tratado + payload do webhook | `./<nome>_test.go` | `go test ./...` |
| Migration / flags / config | none | — (build gate) | — | `go build` |

**Provenance:** amostrados `phone_normalization_test.go` (table-driven puro),
`user_pn_test.go` (fake de store whatsmeow), `db_history_test.go` +
`stdio_test.go:378` (`makeTestServer` com SQLite `:memory:` e schema real),
`webhooks_test.go` (`httptest` + mutação de global com `t.Cleanup`).
Repositório é **package único `main`** na raiz — todo teste é `./<nome>_test.go`.

## Parallelism Assessment

> Gerada do codebase. Confirme antes de executar.

| Test Type | Parallel-Safe? | Isolation Model | Evidence |
| --- | --- | --- | --- |
| unit (funções puras) | **Sim** | Sem estado compartilhado; entrada → saída | `phone_normalization_test.go` — table-driven sem globais |
| integration (DB) | **Não** | `makeTestServer(t)` escreve o global `*adminToken` | `stdio_test.go:383` — `*adminToken = testToken` |
| e2e (handlers) | **Não** | Mesma mutação de global + `userinfocache`/`phoneJIDCache` são globais de pacote | `webhooks_test.go:20-22`; `main.go:78-81` |
| event handler | **Não** | Escreve `store.DeviceProps` (global de pacote do whatsmeow) e o cache de userinfo | `wmiau.go:585-586` |

**Consequência:** só tarefas cujos testes são exclusivamente unitários puros podem
levar `[P]`. Todo o resto roda sequencial.

## Gate Check Commands

> Gerada do codebase. Confirme antes de executar.

| Gate Level | When to Use | Command |
| --- | --- | --- |
| Quick | Tarefas com testes unitários puros | `go test ./...` |
| Full | Tarefas com testes de integração / e2e / event handler | `go test ./... -race` |
| Build | Fim de fase, ou tarefas só de migration/config | `go vet ./... && go build -o /tmp/wuzapi-gate . && go test ./... -race` |

---

## Execution Plan

### Phase 1: Fundação (Sequencial)

```
T0 → T1 → T2
```

Rede de segurança (CI), schema e tipos base. Nada depois disso é verificável sem estes três.

### Phase 2: Núcleo do governor (Sequencial)

Todas as tarefas modificam `governor.go` — sem paralelismo possível.

```
T2 → T3 → T4 → T5 → T6
```

### Phase 3: Destinatário e supressão

```
        ┌→ T7 ──┐
T2 ─────┼→ T8 ──┼──→ (Phase 4)
        └→ T9 ──┘
```

Arquivos distintos e sem dependência entre si. Só T8 leva `[P]` (testes puros);
T7 e T9 têm testes de integração, que não são parallel-safe.

### Phase 4: Integração (Sequencial)

```
T3..T9 → T10 → T11 → T12 → T13 → T14
```

---

## Task Breakdown

### T0: ✅ Habilitar `go test` no CI

> **Concluída** — commit `92ffac7`.

**What**: adicionar um step `go test ./... -race` ao workflow de build, entre
`go vet` e `go build`.
**Where**: `.github/workflows/build.yml`
**Depends on**: None
**Reuses**: estrutura de steps já existente (`build.yml:42-46`)
**Requirement**: — (habilitador; ver `design.md` → Risks & Concerns, linha 1)

**Tools**: MCP: NONE · Skill: NONE

**Done when**:

- [ ] Workflow tem step `Run tests` com `go test ./... -race`
- [ ] O step roda **antes** do `Build application`
- [ ] A suíte existente continua passando: `go test ./... -race`

> **Verificado em 2026-08-09:** `go test ./...` → `ok wuzapi 0.913s` e
> `go test ./... -race` → `ok wuzapi 3.367s`. Não há teste quebrado hoje; o CI
> pode ser habilitado sem correções prévias. (O `-race` passa porque nenhum teste
> atual exercita `startClient` concorrente — a race de `store.DeviceProps` é real
> no código de produção mas só será detectada pelo teste que a tarefa BAN-26,
> da Onda 2, vai adicionar.)

**Tests**: none (config de CI)
**Gate**: build
**Commit**: `ci: roda go test -race no pipeline`

---

### T1: ✅ Migration 10 — estado anti-ban

> **Concluída** — commit `8db8fac`.

**What**: adicionar a migration `add_anti_ban_state` com as 17 colunas em `users`
e as tabelas `suppression` e `send_events`.
**Where**: `migrations.go` (próximo ID livre = **10**)
**Depends on**: T0
**Reuses**: padrão da migration 2 `add_proxy_url` (`migrations.go:26-41`) — bloco
`DO $$` para Postgres + tratamento SQLite em código; `initializeSchema` usado por
`makeTestServer`
**Requirement**: base de BAN-01 … BAN-18

**Tools**: MCP: NONE · Skill: NONE

**Done when**:

- [ ] Migration com `ID: 10, Name: "add_anti_ban_state"` registrada no slice
- [ ] As 17 colunas de `users` do `design.md` existem, todas com default
- [ ] Tabelas `suppression` (PK composta `user_id, jid`) e `send_events`
      (+ índice `send_events_user_time_idx`) criadas
- [ ] `makeTestServer(t)` inicializa sem erro (schema aplicado)
- [ ] Migration é idempotente: rodar duas vezes não falha
- [ ] Gate: `go vet ./... && go build -o /tmp/wuzapi-gate . && go test ./... -race`
- [ ] Contagem de testes: suíte existente continua passando, 0 removidos

**Tests**: integration (1 teste: schema aplica e é idempotente)
**Gate**: full
**Commit**: `feat(governor): migration 10 com estado anti-ban por instância`

---

### T2: ✅ Tipos base do governor — `GateError` e `GovernorDefaults`

> **Concluída** — commit `532f883`.

**What**: criar `governor.go` com `GateError` (status/code/reason/retryAfter/until
+ `WriteTo`), `GovernorDefaults`, `SendKind`, `RampStep`, e as flags/env que
alimentam os defaults.
**Where**: `governor.go` (novo), `main.go` (flags)
**Depends on**: T1
**Reuses**: `s.Respond` (`helpers.go`) para o corpo de erro; padrão de flag+env de
`main.go:53` + `main.go:276-278`
**Requirement**: BAN-11

**Tools**: MCP: NONE · Skill: NONE

**Done when**:

- [ ] `GateError.WriteTo` emite 423 com `{code, reason, until}`, 429 com header
      `Retry-After` em segundos, 422 com `{error, ...}` — conforme AD-001
- [ ] `GovernorDefaults` lê flags `-maxdailyquota`, `-mininterval`,
      `-windowstart`, `-windowend`, `-sendtimezone`, com override por env
- [ ] **AD-003**: `MinInterval` configurado abaixo de 45 s é elevado para 45 s;
      `MaxDailyQuota` acima de 200 é reduzido para 200
- [ ] `SendKind` definido com `KindOutbound`, `KindEdit`, `KindGroup`
- [ ] Gate: `go test ./...`
- [ ] Contagem de testes: +6 (3 status de `WriteTo`, 2 clamps de AD-003, 1 default)

**Tests**: unit
**Gate**: quick
**Commit**: `feat(governor): tipos base, contrato de erro e defaults configuráveis`

---

### T3: Reserva atômica de cota e pacing

**What**: implementar `SendGovernor.reserve(userID, quota, minInterval)` com o
`UPDATE ... WHERE` condicional e a discriminação pós-`RowsAffected==0` entre
"quota estourada" e "pacing violado".
**Where**: `governor.go`
**Depends on**: T2
**Reuses**: `db.Rebind` (padrão de `wmiau.go:983`) para SQLite/Postgres
**Requirement**: BAN-06, BAN-07, BAN-10

**Tools**: MCP: NONE · Skill: NONE

**Done when**:

- [ ] `UPDATE` único no caminho feliz; incremento de `sent_today` e escrita de
      `last_send_at` acontecem na mesma instrução
- [ ] `quota_reset_at` vencido zera `sent_today` para 1 e reagenda para a próxima
      meia-noite do fuso da instância (AC edge case "virada do dia")
- [ ] `RowsAffected()==0` → `SELECT` de diagnóstico monta `GateError` com o
      `Retry-After` correto (segundos até a virada, ou segundos restantes de pacing)
- [ ] **Falha ao chamar o banco → `GateError{Status:503}`** (AD-002, fail-closed)
- [ ] **Teste de concorrência**: 50 goroutines chamando `reserve` com
      `quota=10` → exatamente 10 sucessos, 40 recusas
- [ ] Gate: `go test ./... -race`
- [ ] Contagem de testes: +8

**Tests**: integration
**Gate**: full
**Commit**: `feat(governor): reserva atômica de cota diária e piso de intervalo`

---

### T4: Rampa de warmup

**What**: implementar `effectiveQuota(warmupStartedAt, maxDailyQuota, now)`
aplicando a rampa 30/60/100/150/200 e integrá-la ao `reserve`.
**Where**: `governor.go`
**Depends on**: T3
**Reuses**: `GovernorDefaults.WarmupRamp` (T2)
**Requirement**: BAN-08

**Tools**: MCP: NONE · Skill: NONE

**Done when**:

- [ ] `ramp(d)` = 30 (d0–1), 60 (d2–3), 100 (d4–6), 150 (d7–13), 200 (d14+)
- [ ] Retorna `min(max_daily_quota, ramp(d))` — a config nunca sobe acima da rampa
- [ ] `warmup_started_at` nulo (instância nunca pareada) → quota do dia 0
- [ ] Edge case: repareamento reinicia `warmup_started_at` (teste cobre a
      instância que volta do zero)
- [ ] Relógio injetável — nenhum teste depende do relógio de parede
- [ ] Gate: `go test ./...`
- [ ] Contagem de testes: +7 (um por degrau + nulo + clamp)

**Tests**: unit
**Gate**: quick
**Commit**: `feat(governor): rampa de warmup por dias desde o pareamento`

---

### T5: Janela horária e poda de `send_events`

**What**: implementar `windowGate(now, tz, start, end, skipSunday)` retornando
aberto/fechado + segundos até a próxima abertura; e a poda periódica de
`send_events` (> 7 dias) no boot e a cada 6 h.
**Where**: `governor.go`
**Depends on**: T4
**Reuses**: `safeGo` (`wmiau.go:48`) para o ticker de poda
**Requirement**: BAN-09, BAN-11

**Tools**: MCP: NONE · Skill: NONE

**Done when**:

- [ ] Fora de `[start, end]` ou domingo → fechado, com `Retry-After` = segundos
      até a próxima abertura (inclusive atravessando o domingo)
- [ ] `time.LoadLocation` explícito — nunca `time.Local` (ver Risks & Concerns)
- [ ] Fuso inválido na instância → cai no default global + warning, não entra em pânico
- [ ] Poda roda no boot e a cada 6 h, remove `created_at < now-7d`
- [ ] Gate: `go test ./... -race`
- [ ] Contagem de testes: +9 (dentro, antes, depois, domingo, virada de domingo,
      fuso inválido, DST, poda remove antigos, poda preserva recentes)

**Tests**: integration
**Gate**: full
**Commit**: `feat(governor): janela horária por instância e poda de send_events`

---

### T6: Estado de ban — persistência, checagem e expiração

**What**: implementar `MarkBanned`, `ClearBan`, `banGate` (com limpeza preguiçosa
quando `ban_until` já passou) e integrá-lo como o **primeiro** gate do `Acquire`.
**Where**: `governor.go`
**Depends on**: T5
**Reuses**: colunas `ban_*` da migration 10
**Requirement**: BAN-02, BAN-05

**Tools**: MCP: NONE · Skill: NONE

**Done when**:

- [ ] `ban_state='temp'` com `ban_until` futuro → `GateError{423, "instance_banned"}`
      com `code`, `reason` e `until` preenchidos
- [ ] `ban_state='perm'` → 423 **independente** de `ban_until`
- [ ] `ban_state='self_paused'` → 423 com `reason` distinguível
- [ ] `ban_until` no passado → estado volta a `'ok'` no próprio `Acquire` e o
      envio prossegue (limpeza preguiçosa, sem goroutine de varredura)
- [ ] `ClearBan` zera estado, código, razão e `until`
- [ ] `Acquire` monta a ordem completa: ban → supressão → janela → quota → pacing
      → pré-flight (supressão e pré-flight ainda como no-op até T7/T9)
- [ ] Gate: `go test ./... -race`
- [ ] Contagem de testes: +8

**Tests**: integration
**Gate**: full
**Commit**: `feat(governor): circuit breaker de ban com expiração preguiçosa`

---

### T7: `SuppressionStore`

**What**: criar `suppression.go` com `Suppress` (upsert idempotente),
`IsSuppressed`, `Unsuppress` e `List`; ligar `IsSuppressed` ao segundo gate do
`Acquire`.
**Where**: `suppression.go` (novo), `governor.go` (wiring)
**Depends on**: T2
**Reuses**: padrão `INSERT ... ON CONFLICT DO NOTHING` de `saveMessageToHistory`
(evidência em `db_history_test.go:12-33`)
**Requirement**: BAN-15

**Tools**: MCP: NONE · Skill: NONE

**Done when**:

- [ ] `Suppress` duas vezes com o mesmo `(user_id, jid)` é no-op silencioso,
      não erro, e não duplica linha
- [ ] `IsSuppressed` devolve `(true, reason)` e usa a PK composta (sem varredura)
- [ ] Supressão é **por instância**: suprimir em A não afeta B
- [ ] `Acquire` devolve `GateError{422, "recipient_suppressed"}` com o motivo
- [ ] Gate: `go test ./... -race`
- [ ] Contagem de testes: +7

**Tests**: integration
**Gate**: full
**Commit**: `feat(suppression): store de supressão por instância`

---

### T8: `matchOptOut` [P]

**What**: criar `optout.go` com a normalização (trim → minúsculas → sem acento →
sem pontuação → espaços colapsados) e o casamento **exato** contra o conjunto de
termos de descadastro.
**Where**: `optout.go` (novo)
**Depends on**: T2
**Reuses**: nenhum — função pura sem dependências
**Requirement**: BAN-17 (parte pura)

**Tools**: MCP: NONE · Skill: NONE

**Done when**:

- [ ] Casa: `SAIR`, `sair`, ` Sair `, `PARAR`, `pare`, `stop`, `descadastrar`,
      `cancelar`, `remover`, `Sair.`, `SAIR!`
- [ ] **Não** casa: `"não quero parar de receber"`, `"vou sair mais tarde"`,
      `"cancelar pedido"` — casamento é sobre o texto inteiro, nunca substring (AC-6)
- [ ] Texto vazio, só espaços, ou só pontuação → `false`
- [ ] Remoção de acento cobre `descadastrar`/`cancelar` acentuados incorretamente
- [ ] Gate: `go test ./...`
- [ ] Contagem de testes: +1 table-driven com ≥ 16 casos

**Tests**: unit
**Gate**: quick
**Commit**: `feat(optout): matcher exato de termos de descadastro`

---

### T9: `resolveRecipientJID` com cache negativo

**What**: generalizar `normalizeBrazilianJID` para verificar registro no WhatsApp
para todo `DefaultUserServer`, devolvendo `RecipientStatus`, com cache positivo e
negativo em `phoneJIDCache`.
**Where**: `wmiau.go` (modifica `normalizeBrazilianJID`, `wmiau.go:403`)
**Depends on**: T2
**Reuses**: `brazilianMobileVariants` (`wmiau.go:375`), `phoneJIDCache`
(`main.go:81`), fake de store whatsmeow do `user_pn_test.go`
**Requirement**: BAN-12, BAN-13, BAN-14

**Tools**: MCP: `context7` (confirmar assinatura de `IsOnWhatsApp`) · Skill: NONE

**Done when**:

- [ ] Grupo / newsletter / LID / broadcast → `(recipient, Unknown)`, sem consulta
- [ ] Celular BR ambíguo → comportamento **idêntico** ao atual (os 7 casos de
      `phone_normalization_test.go` continuam passando sem alteração)
- [ ] Número não-BR válido → consulta `IsOnWhatsApp` e devolve `Registered`
- [ ] Nenhuma variante registrada → `NotRegistered` (não mais fallback silencioso)
- [ ] `IsOnWhatsApp` com erro/timeout → `(recipient, Unknown)` + warning; **nunca**
      degrada o envio (AC-3)
- [ ] Cache **negativo**: segundo envio para um número inexistente não consulta de novo
- [ ] Cache positivo: 200 envios para o mesmo número → 1 consulta
- [ ] Gate: `go test ./... -race`
- [ ] Contagem de testes: +10 (7 existentes preservados + novos)

**Tests**: integration
**Gate**: full
**Commit**: `feat(preflight): verifica registro no WhatsApp com cache negativo`

---

### T10: Converter `validateMessageFields` em método de `*server`

**What**: refatoração mecânica, **sem mudança de comportamento**: transformar a
função livre em método e atualizar os 12 call sites.
**Where**: `handlers.go` (`:6226` + call sites `:1023, 1193, 1399, 1592, 1757,
1932, 2055, 2254, 2554, 2765, 2907, 3046`)
**Depends on**: T6, T7, T9
**Reuses**: —
**Requirement**: habilitador de BAN-02, BAN-13, BAN-15

**Tools**: MCP: NONE · Skill: NONE

**Done when**:

- [ ] `func (s *server) validateMessageFields(...)` com a mesma assinatura de
      parâmetros e retorno
- [ ] Os **12** call sites atualizados — verificado por
      `grep -c "s.validateMessageFields(" handlers.go` retornando 12
- [ ] Zero mudança de comportamento: nenhum teste existente alterado
- [ ] Gate: `go vet ./... && go build -o /tmp/wuzapi-gate . && go test ./... -race`
- [ ] Contagem de testes: inalterada, 0 removidos

**Tests**: none (refatoração pura; coberta pelo build gate e pela suíte existente)
**Gate**: build
**Commit**: `refactor(handlers): validateMessageFields vira método de server`

---

### T11: Plugar o `Acquire` nos endpoints de envio

**What**: chamar `governor.Acquire` dentro de `s.validateMessageFields`, mapear
`SendKind` por handler, e devolver o `GateError` pelo `s.Respond`.
**Where**: `handlers.go`
**Depends on**: T10
**Reuses**: `GateError.WriteTo` (T2), tabela `SendKind` do `design.md`
**Requirement**: BAN-02, BAN-13, BAN-15

**Tools**: MCP: NONE · Skill: NONE

**Done when**:

- [ ] `KindOutbound` nos 10 handlers de envio direto; `KindEdit` em
      `SendEditMessage`; `KindGroup` em `SendPoll` e alvos `@g.us`
- [ ] `/chat/send/edit` **não** consome cota (teste explícito)
- [ ] `/chat/send/poll` não passa por supressão nem pré-flight
- [ ] E2E via `httptest` cobrindo, em `/chat/send/text`: happy path,
      423 (banido), 429 (quota), 429 (pacing), 422 (suprimido),
      422 (não está no WhatsApp), 503 (banco fora)
- [ ] Header `Retry-After` presente e correto em ambos os 429
- [ ] Nenhum caminho de gate chega a `whatsmeow.SendMessage` — provado por
      client fake que registra invocações
- [ ] Gate: `go test ./... -race`
- [ ] Contagem de testes: +12

**Tests**: e2e
**Gate**: full
**Commit**: `feat(handlers): aplica o governor em todos os endpoints de envio`

---

### T12: Coletor de eventos de banimento

**What**: tratar `events.TemporaryBan` e `events.ConnectFailure` no event handler:
persistir estado, enriquecer o webhook, desconectar em ban.
**Where**: `wmiau.go` (`:1662` e `:1618`)
**Depends on**: T11
**Reuses**: `sendEventWithWebHook` (`wmiau.go:161`), `SendGovernor.MarkBanned` (T6)
**Requirement**: BAN-01, BAN-03, BAN-04

**Tools**: MCP: `context7` (constantes de `TempBanReason` / `ConnectFailureReason`) · Skill: NONE

**Done when**:

- [ ] `TemporaryBan` → `MarkBanned('temp', evt.Code, evt.Code.String(), now+Expire)`
- [ ] `Expire == 0` → assume 24 h (edge case da spec)
- [ ] Webhook `TemporaryBan` passa a carregar `code`, `reason` e `expire` em
      segundos — os três ausentes hoje
- [ ] `ConnectFailure` mapeado: `402 TempBanned` → `temp`; `406 UnknownLogout` →
      `perm`; `403 MainDeviceGone` → `perm`; demais códigos → estado inalterado
- [ ] Webhook `ConnectFailure` carrega `reasonCode` e `reasonName`
- [ ] `TemporaryBan` chama `client.Disconnect()`
- [ ] Após injetar `TemporaryBan`, um `POST /chat/send/text` devolve **423**
      (teste ponta a ponta do circuit breaker — Success Criteria nº 1)
- [ ] Gate: `go test ./... -race`
- [ ] Contagem de testes: +9

**Tests**: integration
**Gate**: full
**Commit**: `feat(events): persiste e propaga TemporaryBan e ConnectFailure`

---

### T13: Coletor de sinais de supressão

**What**: tratar `events.BlocklistChange` (suprime + registra em `send_events`) e
`events.Message` de entrada com `matchOptOut` (suprime + webhook `OptOut`).
**Where**: `wmiau.go` (`:1642` e `:974`), `constants.go` (novos tipos de evento)
**Depends on**: T12
**Reuses**: `SuppressionStore` (T7), `matchOptOut` (T8), `sendEventWithWebHook`
**Requirement**: BAN-16, BAN-17

**Tools**: MCP: NONE · Skill: NONE

**Done when**:

- [ ] `BlocklistChange` de bloqueio → `Suppress(reason='blocked')` +
      `send_events(kind='block')`
- [ ] `BlocklistChange` de desbloqueio **não** suprime
- [ ] Mensagem de entrada `"SAIR"` → `Suppress(reason='opt_out')` + webhook `OptOut`
- [ ] Mensagem de entrada `"não quero parar de receber"` → **não** suprime
- [ ] Mensagem de saída (`evt.Info.IsFromMe`) nunca dispara opt-out
- [ ] `"OptOut"` adicionado a `supportedEventTypes` (`constants.go`)
- [ ] Após opt-out, envio para aquele número devolve **422** (teste ponta a ponta)
- [ ] Gate: `go test ./... -race`
- [ ] Contagem de testes: +8

**Tests**: integration
**Gate**: full
**Commit**: `feat(events): supressão automática por bloqueio e opt-out`

---

### T14: Endpoints de supressão e limpeza de ban

**What**: expor `POST/GET/DELETE /user/suppress` e `POST /session/ban/clear`.
**Where**: `handlers.go`, `routes.go`
**Depends on**: T13
**Reuses**: `SuppressionStore` (T7), `ClearBan` (T6), padrão de handler +
`s.Respond` já usado em `routes.go:141-157`
**Requirement**: BAN-18, BAN-05

**Tools**: MCP: NONE · Skill: NONE

**Done when**:

- [ ] `POST /user/suppress {"phone","reason"}` → suprime; idempotente
- [ ] `GET /user/suppress` → lista da instância; `[]` quando vazia, nunca `null`
- [ ] `DELETE /user/suppress {"phone"}` → remove; 404 se não existia
- [ ] `POST /session/ban/clear` → `ClearBan`; exige token de **admin**
      (`s.authadmin`), não token de instância
- [ ] Isolamento entre instâncias verificado: token de A não lista/altera B
- [ ] E2E de cada rota: happy path + payload inválido + não autorizado
- [ ] Gate: `go test ./... -race`
- [ ] Contagem de testes: +11

**Tests**: e2e
**Gate**: full
**Commit**: `feat(api): endpoints de supressão e limpeza manual de ban`

---

## Parallel Execution Map

```
Phase 1 (Sequencial):
  T0 ──→ T1 ──→ T2

Phase 2 (Sequencial — todas em governor.go):
  T2 ──→ T3 ──→ T4 ──→ T5 ──→ T6

Phase 3 (T2 completa, então):
  ├── T7        (sequencial — testes de integração)
  ├── T8 [P]    (unitário puro — order-free)
  └── T9        (sequencial — testes de integração)

Phase 4 (T6, T7, T9 completas, então):
  T10 ──→ T11 ──→ T12 ──→ T13 ──→ T14
```

**Restrição de paralelismo:** apenas T8 leva `[P]` — é a única tarefa cujos
testes são unitários puros (ver Parallelism Assessment). T7 e T9 estão no mesmo
grupo lógico mas rodam sequencialmente porque seus testes tocam `makeTestServer`
(global `*adminToken`) e `phoneJIDCache`.

---

## Task Granularity Check

| Task | Escopo | Status |
| --- | --- | --- |
| T0: CI roda testes | 1 arquivo de config | ✅ Granular |
| T1: Migration 10 | 1 migration | ✅ Granular |
| T2: Tipos base + flags | 1 arquivo novo + flags coesas | ✅ Granular |
| T3: Reserva atômica | 1 função | ✅ Granular |
| T4: Rampa de warmup | 1 função | ✅ Granular |
| T5: Janela + poda | 2 funções coesas (mesmo arquivo, mesma preocupação temporal) | ⚠️ OK — coeso |
| T6: Estado de ban | 3 métodos de um mesmo conceito | ⚠️ OK — coeso |
| T7: SuppressionStore | 1 componente | ✅ Granular |
| T8: matchOptOut | 1 função pura | ✅ Granular |
| T9: resolveRecipientJID | 1 função | ✅ Granular |
| T10: Conversão em método | 1 refatoração mecânica | ✅ Granular |
| T11: Plugar o Acquire | 1 ponto de integração | ✅ Granular |
| T12: Eventos de ban | 2 `case` do mesmo switch | ⚠️ OK — coeso |
| T13: Eventos de supressão | 2 `case` do mesmo switch | ⚠️ OK — coeso |
| T14: Endpoints | 4 rotas do mesmo recurso | ⚠️ OK — coeso |

Nenhum ❌ — nenhuma tarefa cruza componentes não relacionados.

---

## Diagram-Definition Cross-Check

| Task | Depends On (corpo) | Diagrama mostra | Status |
| --- | --- | --- | --- |
| T0 | None | raiz da Phase 1 | ✅ Match |
| T1 | T0 | `T0 → T1` | ✅ Match |
| T2 | T1 | `T1 → T2` | ✅ Match |
| T3 | T2 | `T2 → T3` | ✅ Match |
| T4 | T3 | `T3 → T4` | ✅ Match |
| T5 | T4 | `T4 → T5` | ✅ Match |
| T6 | T5 | `T5 → T6` | ✅ Match |
| T7 | T2 | Phase 3 parte de T2 | ✅ Match |
| T8 | T2 | Phase 3 parte de T2 | ✅ Match |
| T9 | T2 | Phase 3 parte de T2 | ✅ Match |
| T10 | T6, T7, T9 | Phase 4 parte de T6/T7/T9 | ✅ Match |
| T11 | T10 | `T10 → T11` | ✅ Match |
| T12 | T11 | `T11 → T12` | ✅ Match |
| T13 | T12 | `T12 → T13` | ✅ Match |
| T14 | T13 | `T13 → T14` | ✅ Match |

T7, T8 e T9 não dependem entre si — consistente com o agrupamento em ramos
paralelos no diagrama. T8 é o único com `[P]`, e não é dependência de nenhum
irmão do mesmo grupo.

---

## Test Co-location Validation

| Task | Camada criada/modificada | Matriz exige | Tarefa diz | Status |
| --- | --- | --- | --- | --- |
| T0 | Config de CI | none | none | ✅ OK |
| T1 | Migration | none — mas `makeTestServer` depende do schema | integration | ✅ OK (acima do mínimo, deliberado) |
| T2 | Lógica pura (`WriteTo`, clamps) | unit | unit | ✅ OK |
| T3 | Acesso a dados | integration | integration | ✅ OK |
| T4 | Lógica pura | unit | unit | ✅ OK |
| T5 | Lógica pura + acesso a dados (poda) | integration (maior das duas) | integration | ✅ OK |
| T6 | Acesso a dados | integration | integration | ✅ OK |
| T7 | Acesso a dados | integration | integration | ✅ OK |
| T8 | Lógica pura | unit | unit | ✅ OK |
| T9 | Lógica + cache global | integration | integration | ✅ OK |
| T10 | Refatoração sem novo comportamento | none | none | ✅ OK — build gate + suíte existente |
| T11 | Handler HTTP | e2e | e2e | ✅ OK |
| T12 | Event handler | integration | integration | ✅ OK |
| T13 | Event handler | integration | integration | ✅ OK |
| T14 | Handler HTTP | e2e | e2e | ✅ OK |

Nenhuma ❌ VIOLATION. Nenhum `Tests: none` justificado por "coberto em outra
tarefa" — T0 é config, T10 é refatoração provada pelo build gate mais a suíte
existente inalterada.

---

## Rastreabilidade dos requisitos P1

| Requisito | Tarefa(s) |
| --- | --- |
| BAN-01 | T12 |
| BAN-02 | T6 (lógica) + T11 (wiring) |
| BAN-03 | T12 |
| BAN-04 | T12 |
| BAN-05 | T6 (expiração) + T14 (clear manual) |
| BAN-06 | T3 |
| BAN-07 | T3 |
| BAN-08 | T4 |
| BAN-09 | T5 |
| BAN-10 | T3 |
| BAN-11 | T2 + T5 |
| BAN-12 | T9 |
| BAN-13 | T9 + T11 |
| BAN-14 | T9 |
| BAN-15 | T7 + T11 |
| BAN-16 | T13 |
| BAN-17 | T8 + T13 |
| BAN-18 | T14 |

**Cobertura:** 18/18 requisitos P1 mapeados. 0 órfãos.
