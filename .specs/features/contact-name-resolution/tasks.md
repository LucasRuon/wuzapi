# Contact Name Resolution Tasks

## Execution Protocol (MANDATORY — do not skip)

Implement these tasks with the `tlc-spec-driven-v3` skill: **activate it by name and
follow its Execute flow and Critical Rules.** Do not search for skill files by
filesystem path. The skill is the source of truth for the full flow (per-task cycle,
adequacy review, Verifier, discrimination sensor).

**If the skill cannot be activated, STOP and tell the user — do not proceed without it.**

---

**Spec**: `.specs/features/contact-name-resolution/spec.md`
**Design**: none — sem decisão arquitetural nova; segue os padrões de
`ResyncLabels` (`handlers.go:8073`) e `enrichGroupInfo` (`handlers.go:4473`).
**Status**: Done — verificação independente PASS (ver `validation.md`)

---

## Test Coverage Matrix

> Gerada do codebase, guidelines e spec. Guidelines encontradas: nenhuma
> (`CONTRIBUTING.md`/`AGENTS.md` inexistentes); comandos extraídos de
> `.github/workflows/build.yml:42-49`. Strong defaults aplicados.

| Code Layer | Required Test Type | Coverage Expectation | Location Pattern | Run Command |
| --- | --- | --- | --- | --- |
| Lógica de domínio (funções puras em `handlers.go`: `resyncContacts`, `resolveMissingBusinessNames`, `enrichGroupList`, `wantsBusinessResolve`) | unit | Todos os ramos; 1:1 com os ACs da spec; todo edge case listado tem teste dedicado | `<nome>_test.go` no pacote `main` | `go test ./... -race` |
| Rota / handler HTTP | unit via `httptest` + router real (`makeTestServer`) | Toda rota nova: registrada (não-404), auth passa (não-401) e caminho de erro documentado (`no session`) | `<nome>_test.go` no pacote `main` | `go test ./... -race` |
| Roteamento de eventos do whatsmeow (`wmiau.go`) | none | — (build gate; o repo não tem harness para `myEventHandler`, registrado como lacuna na spec) | — | build gate |
| Documentação (`API.md`) | none | — (build gate) | — | build gate |

## Parallelism Assessment

> Gerada do codebase.

| Test Type | Parallel-Safe? | Isolation Model | Evidence |
| --- | --- | --- | --- |
| unit (funções puras) | Yes | Sem estado compartilhado; fakes por teste | `group_participants_test.go:110-137` |
| unit (rota/handler) | No | `makeTestServer` escreve na global `*adminToken` | `stdio_test.go:383` — `*adminToken = testToken` |

Como T1 e T3 tocam a camada de rota, nenhuma task recebe `[P]`; a fase é sequencial.

## Gate Check Commands

| Gate Level | When to Use | Command |
| --- | --- | --- |
| Quick | Depois de tasks só com testes unitários | `go test ./... -race` |
| Full | Depois de tasks com teste de rota | `go test ./... -race` |
| Build | Última task da fase / tasks sem teste | `go vet ./... && go build -o /dev/null . && go test ./... -race` |

---

## Execution Plan

### Fase 1 (sequencial)

```
T1 → T2 → T3
```

T1 e T2 são independentes em código, mas ambos tocam a camada de rota/teste não
parallel-safe; T3 depende de T2.

---

## Task Breakdown

### T1: Endpoint `POST /user/contacts/resync`

**What**: Um endpoint que força o full sync do app-state patch da agenda e
responde com o total de contatos no store.
**Where**: `handlers.go` (novo `resyncContacts` + handler `ResyncContacts`),
`routes.go` (registro), `wmiau.go` (`case *events.Contact`), `API.md` (doc),
`contacts_resync_test.go` (novo)
**Depends on**: None
**Reuses**: `ResyncLabels` (`handlers.go:8073`) para a forma do handler;
`TestGetBlocklistEndpoint` (`blocklist_test.go:58`) para o teste de rota
**Requirement**: CNR-01, CNR-02

**Tools**: MCP: NONE · Skill: NONE

**Done when**:

- [x] `resyncContacts` chama `FetchAppState` com `appstate.WAPatchCriticalUnblockLow`, `fullSync=true`, `onlyIfNotSynced=false` (AC-1)
- [x] Devolve o total de contatos lido do store **depois** do sync (AC-2)
- [x] Handler responde `200 {"success":true,"contacts":N}` (AC-2)
- [x] Sem cliente → `500 no session` (AC-3)
- [x] Cliente desconectado → `500 not connected`, sem chamar `FetchAppState` (AC-4)
- [x] Erro do `FetchAppState` → `500` com a mensagem, sem contar contatos (AC-5)
- [x] Erro na contagem → `500` com o erro (AC-6)
- [x] Rota registrada em `routes.go` e documentada em `API.md`
- [x] Gate check passa: `go test ./... -race`
- [x] Test count: 132 testes de topo anteriores + os novos, 0 skips

**Tests**: unit (domínio + rota)
**Gate**: full

**Commit**: `feat(contacts): endpoint de re-sync da agenda do celular pareado`

---

### T2: Resolve em lote de nome comercial

**What**: `resolveMissingBusinessNames` (coleta faltantes, dedupe, teto, consulta
em lote, releitura do store) e `enrichGroupList`, que aplica o enrich a uma lista
de grupos com resolver opcional.
**Where**: `handlers.go`, `group_business_names_test.go` (novo)
**Depends on**: None (código); executada após T1 pela restrição de paralelismo
**Reuses**: `enrichGroupInfo` / `resolveGroupParticipantNames` (`handlers.go:4473-4589`),
fakes de `group_participants_test.go:12-66`
**Requirement**: CNR-03, CNR-04, CNR-05

**Tools**: MCP: NONE · Skill: NONE

**Done when**:

- [x] Participantes sem `ContactName` e com `PhoneNumber` entram em **uma** consulta em lote e recebem o nome comercial resolvido (AC P2-1)
- [x] Resolver `nil` → nenhuma consulta e resposta idêntica ao enrich atual (AC P2-2)
- [x] Participante que já tem `ContactName` não entra no lote (AC P2-3)
- [x] Participante sem `PhoneNumber` não entra no lote (AC P2-4)
- [x] O mesmo participante em vários grupos entra uma vez e o nome é aplicado a todas as ocorrências (AC P2-5)
- [x] Acima de 64 faltantes, consulta os 64 primeiros e loga quantos ficaram de fora (AC P2-6)
- [x] Erro do resolver → nomes existentes preservados, sem propagar erro (AC P2-7)
- [x] Sem nome devolvido → `ContactName` permanece vazio (AC P2-8)
- [x] Edge cases: zero faltantes → resolver não é chamado; lista vazia → no-op; `Store.Contacts == nil` → no-op sem panic; dois participantes com o mesmo `PhoneNumber` → ambos recebem o nome (Edge Cases)
- [x] Gate check passa: `go test ./... -race`
- [x] Test count: os testes de T1 + os novos, 0 skips

**Tests**: unit
**Gate**: quick

**Commit**: `feat(groups): resolve nome comercial de participantes sem nome`

---

### T3: Opt-in `resolveBusiness` nos endpoints de grupo

**What**: Ligar o resolver de T2 em `GET /group/info` e `GET /group/list` somente
quando `resolveBusiness=true`, via `wantsBusinessResolve`.
**Where**: `handlers.go` (`GetGroupInfo`, `ListGroups`, `wantsBusinessResolve`),
`API.md`, `group_business_names_test.go` (adicionar)
**Depends on**: T2
**Reuses**: `enrichGroupList` (T2); `TestGetBlocklistEndpoint` para o teste de rota
**Requirement**: CNR-03 (AC-2)

**Tools**: MCP: NONE · Skill: NONE

**Done when**:

- [x] `wantsBusinessResolve` devolve `true` só para `resolveBusiness=true`; `false` para ausente, vazio, `false`, `1`, `TRUE` (AC P2-2)
- [x] `GetGroupInfo` e `ListGroups` passam `client.GetUserInfo` como resolver apenas no opt-in, `nil` caso contrário (AC P2-2)
- [x] `GET /group/info?resolveBusiness=true` continua respondendo `500 no session` sem sessão (rota íntegra)
- [x] `API.md` documenta o parâmetro nos dois endpoints
- [x] Gate check passa: `go vet ./... && go build -o /dev/null . && go test ./... -race`
- [x] Test count: os testes de T2 + os novos, 0 skips

**Tests**: unit (domínio + rota)
**Gate**: build

**Commit**: `feat(groups): parametro resolveBusiness em /group/info e /group/list`

---

## Task Granularity Check

| Task | Scope | Status |
| --- | --- | --- |
| T1: endpoint de resync | 1 endpoint (+ rota + doc) | ✅ Granular |
| T2: resolve em lote | 2 funções coesas no mesmo arquivo | ✅ Granular (2-3 coisas relacionadas) |
| T3: opt-in nos handlers | 1 parser + 2 call sites | ✅ Granular |

## Diagram-Definition Cross-Check

| Task | Depends On (corpo) | Diagrama mostra | Status |
| --- | --- | --- | --- |
| T1 | None | (início) | ✅ Match |
| T2 | None (ordem por paralelismo) | T1 → T2 | ✅ Match (ordem, não dependência de código) |
| T3 | T2 | T2 → T3 | ✅ Match |

## Test Co-location Validation

| Task | Camada criada/modificada | Matriz exige | Task diz | Status |
| --- | --- | --- | --- | --- |
| T1 | domínio + rota (+ `wmiau.go`: none, + doc: none) | unit | unit | ✅ OK |
| T2 | domínio | unit | unit | ✅ OK |
| T3 | domínio + rota | unit | unit | ✅ OK |
