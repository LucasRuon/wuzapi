# Contact Name Resolution Validation — Iteração 2

**Date**: 2026-08-21
**Spec**: `.specs/features/contact-name-resolution/spec.md` (releitura; **alterada** desde a iteração 1)
**Diff range (feature)**: `47345e5..HEAD` — `530724b`, `6578aec`, `c2630aa`, `d64e3f1`
**Diff range (esta iteração)**: `c2630aa..HEAD` — `spec.md` +162, `group_business_names_test.go` +70
**Verifier**: sub-agente independente (author ≠ verifier), evidência-ou-zero
**Verdict**: ✅ **PASS** — 16/16 ACs e 7/7 edge cases com evidência `arquivo:linha`;
sensor 5/5 mortos (incluindo os 2 sobreviventes da iteração 1); gate verde com 152
testes. Restam apenas 2 pendências **Cosmetic de documentação** (fora do código).

---

## Histórico — Iteração 1 (resumo)

Veredito: ❌ FAIL. Gate verde (150 testes), 16/16 ACs com evidência, mas o sensor
apontou **2 de 13 mutantes sobreviventes** e 1 mudança de comportamento não
reivindicada. Lacunas ranqueadas:

| # | Severidade | Lacuna |
| --- | --- | --- |
| 1 | Minor | M10 sobreviveu — guard `if participant.ContactName != ""` no loop de aplicação sem teste |
| 2 | Minor | M7 sobreviveu — degrade após erro do resolver sem asserção de "não releu o store" |
| 3 | Minor | `GET /group/list` vazio passou de `{"Groups":null}` para `{"Groups":[]}` sem reivindicação na spec |
| 4 | Cosmetic | Parte "registrar em log" de P2 AC-6 sem asserção (repo não tem harness de `zerolog`) |
| 5 | Cosmetic | `tasks.md` ainda marcado **In Progress** |

---

## Fechamento das Lacunas da Iteração 1

| # | Correção reivindicada | Evidência verificada independentemente | Status |
| --- | --- | --- | --- |
| 1 | Teste do guard anti-sobrescrita + edge case na spec | Spec: `spec.md:132-134` ("dois participantes compartilham o mesmo `PhoneNumber` mas **um deles já tem `ContactName`** ... o nome existente SHALL ser preservado"). Teste: `group_business_names_test.go:179` `TestResolveMissingBusinessNamesNeverOverwritesAnExistingName`; asserções `:197` — `groups[0].Participants[0].ContactName != "João Silva"` e `:200` — `[1].ContactName != "Padaria do Zé LTDA"`. Mutante R1 (= M10) agora **morre** neste teste. | ✅ **FECHADA** |
| 2 | Teste de degrade sem releitura do store | Spec: `spec.md:135-136` ("WHEN o resolver falha THEN o sistema SHALL NÃO reler o contact store"). Teste: `group_business_names_test.go:206` `TestResolveMissingBusinessNamesSkipsStoreReadOnResolverError`; chama `resolveMissingBusinessNames` direto com grupos já enriquecidos (`:226-233`) e asserta `:235` — `calls != 0` sobre `countingContactStore` (`group_participants_test.go:179-187`), mais `:238` — `ContactName != ""`. Mutante R2 (= M7) agora **morre**. | ✅ **FECHADA** |
| 3 | `"Groups": null` → `[]` reivindicada na spec + teste | Spec: P2 AC-2 reescrito em `spec.md:98-103` ("Única mudança de forma aceita: `GET /group/list` sem nenhum grupo passa a devolver `\"Groups\": []` em vez de `\"Groups\": null`"), com edge case dedicado em `spec.md:137`. Teste: `group_business_names_test.go:350` — `if groups == nil { t.Errorf(...) }`. Mutante N1 (`make(...)` → `var out`) **morre**. | ✅ **FECHADA** (aceita e reivindicada) |
| 4 | Log do teto aceito como assunção declarada | `spec.md:42` — linha na tabela `Assumptions & Open Questions`: "Log do teto de 64 (P2 AC-6) \| Registrar em log, sem gate automatizado \| O repo não tem harness de captura de `zerolog` ... O *comportamento* do teto ... é testado". | ✅ **FECHADA** (lacuna declarada) |
| 5 | `tasks.md` Status | `tasks.md:17` ainda diz `**Status**: In Progress`. | ❌ **ABERTA** (Cosmetic — do orquestrador) |

---

## Spec-Anchored Acceptance Criteria — P1: Re-sync da agenda

`contacts_resync_test.go` **não foi tocado** nesta iteração (`git diff c2630aa..HEAD`
lista só `spec.md` e `group_business_names_test.go`); as linhas abaixo foram
re-conferidas no arquivo atual.

| Criterion (WHEN X THEN Y) | Spec-defined outcome | `file:line` + assertion | Result |
| --- | --- | --- | --- |
| AC-1 (`spec.md:62`): sessão conectada → `FetchAppState` com `critical_unblock_low`, `fullSync=true`, `onlyIfNotSynced=false` | patch `WAPatchCriticalUnblockLow`, `true`, `false`; exatamente 1 chamada | `contacts_resync_test.go:63` — `len(fetcher.calls) != 1`; `:67` — `call.name != appstate.WAPatchCriticalUnblockLow`; `:70` — `!call.fullSync`; `:73` — `call.onlyIfNotSynced` | ✅ PASS |
| AC-2 domínio (`spec.md:65`): total = contatos no store **depois** do sync | `N = len(GetAllContacts())` pós-sync | `contacts_resync_test.go:76` — `total != 2`; ordenação provada por `:96` — `contacts.calls != 0` quando o sync falha | ✅ PASS |
| AC-2 payload: `{"success": true, "contacts": N}` | `success=true`, `contacts=N` | `contacts_resync_test.go:143` — `got["success"] != true`; `:146` — `got["contacts"] != 7` | ✅ PASS |
| AC-2 HTTP: responder `200` | status 200 + corpo JSON | — sem asserção HTTP de 200 | ⚠️ Lacuna estrutural **declarada** (`tasks.md:30` limita a camada de rota a não-404 / não-401 / `no session`; o 200 exige cliente whatsmeow real). Inspeção: `handlers.go:3728` `s.Respond(w, r, http.StatusOK, string(responseJson))` |
| AC-3 (`spec.md:68`): sem cliente → `500` com `no session` | status 500, corpo com `no session` | `contacts_resync_test.go:173` — `rr.Code != http.StatusInternalServerError \|\| !strings.Contains(rr.Body.String(), "no session")` (+ `:167` não-404, `:170` não-401) | ✅ PASS |
| AC-4 (`spec.md:70`): desconectado → `500 not connected`, **sem** `FetchAppState` | `errNotConnected`; zero `FetchAppState` | `contacts_resync_test.go:125` — `!errors.Is(err, errNotConnected)`; `:131` — `len(fetcher.calls) != 0`; `:134` — `contacts.calls != 0` | ✅ PASS (domínio); HTTP por inspeção `handlers.go:3721` |
| AC-5 (`spec.md:72`): erro do `FetchAppState` → `500`, **não** conta contatos | erro propagado; zero `GetAllContacts` | `contacts_resync_test.go:90` — `!errors.Is(err, fetchErr)`; `:93` — `total != 0`; `:96` — `contacts.calls != 0` | ✅ PASS |
| AC-6 (`spec.md:74`): erro na contagem pós-sync → `500` com o erro | erro propagado; o sync já rodou | `contacts_resync_test.go:108` — `!errors.Is(err, countErr)`; `:111` — `total != 0`; `:114` — `len(fetcher.calls) != 1` | ✅ PASS |

**Status P1**: ✅ 8/8 com evidência. Nenhuma regressão. 1 lacuna estrutural declarada.

---

## Spec-Anchored Acceptance Criteria — P2: Fallback opt-in de nome comercial

| Criterion (WHEN X THEN Y) | Spec-defined outcome | `file:line` + assertion | Result |
| --- | --- | --- | --- |
| AC-1 (`spec.md:94`): `resolveBusiness=true` + sem `ContactName` e com `PhoneNumber` → **uma** consulta em lote, `ContactName` preenchido | exatamente 1 batch; nome comercial no `ContactName` | `group_business_names_test.go:87` — `len(resolver.batches) != 1`; `:93` — `ContactName != "Padaria do Zé LTDA"` | ✅ PASS |
| AC-2 (`spec.md:98`): sem opt-in → zero rede; nomes iguais aos de antes; **única** mudança de forma permitida `Groups: []` | resolver `nil`; 0 batches; nomes inalterados; `[]` no caso vazio | `group_business_names_test.go:375` — `businessNameResolverFor(req, client) != nil` para `""`,`=`,`false`,`1`,`TRUE`,`true` (tabela `:362-370`); `:117` — `len(resolver.batches) != 0`; `:120` — `ContactName != ""`; `:350` — `groups == nil` | ✅ PASS (era ⚠️ na iteração 1; o desvio virou requisito explícito e testado) |
| AC-3 (`spec.md:104`): já tem `ContactName` → fora do lote | batch = só o sem nome; nome existente sobrevive | `group_business_names_test.go:90` — `len(got) != 1 \|\| got[0] != want[0]`; `:96` — `ContactName[1] != "João Silva"` | ✅ PASS |
| AC-4 (`spec.md:105`): sem `PhoneNumber` → fora do lote | LID-only fora do batch, `ContactName` vazio | `group_business_names_test.go:90` — batch de tamanho 1; `:99` — `ContactName[2] != ""` | ✅ PASS |
| AC-5 (`spec.md:107`): mesmo participante em vários grupos → 1 entrada, nome em todas | 1 batch, 1 JID; ambos os grupos nomeados | `group_business_names_test.go:139` — `len(resolver.batches) != 1 \|\| len(resolver.batches[0]) != 1`; `:143` — `ContactName != "Padaria do Zé LTDA"` nos 2 grupos | ✅ PASS |
| AC-6 (`spec.md:110`): > 64 faltantes → consulta os 64 primeiros e loga o excedente | batch = 64; 64 primeiros nomeados; resto vazio; log com o excedente | `group_business_names_test.go:263` — `len(resolver.batches[0]) != maxBusinessNameLookups`; `:267` — nomes dos 64 primeiros; `:272` — `ContactName != ""` para os 6 restantes | ✅ PASS (comportamento). Parte de log = **lacuna declarada** em `spec.md:42`; inspeção `handlers.go:4639-4642` |
| AC-7 (`spec.md:112`): consulta falha → `200` com os nomes que já existiam, sem propagar erro | resposta preservada, nenhum erro retornado, e (edge `spec.md:135`) sem releitura do store | `group_business_names_test.go:296` — `ContactName[0] != ""`; `:299` — `ContactName[1] != "João Silva"`; **+** `:235` — `calls != 0` (zero `GetContact` após erro) | ✅ PASS (era ⚠️ raso; mutante R2 agora morre) |
| AC-8 (`spec.md:114`): consulta sem nome → `ContactName` vazio, sem placeholder | string vazia | `group_business_names_test.go:314` — `len(resolver.batches) != 1`; `:317` — `ContactName != ""` | ✅ PASS |

**Status P2**: ✅ 8/8 com evidência. Nenhuma ressalva de discriminação remanescente
(as 3 da iteração 1 foram fechadas ou declaradas na spec).

---

## Edge Cases (lista da spec cresceu de 4 → 7)

| # | Edge case (`spec.md`) | Spec-defined outcome | `file:line` + assertion | Result |
| --- | --- | --- | --- | --- |
| 1 | `:125` — nenhum participante sem nome → resolver **não** é chamado | zero consultas mesmo com opt-in | `group_business_names_test.go:335` — `len(resolver.batches) != 0` | ✅ PASS |
| 2 | `:127` — lista de grupos vazia → no-op | slice vazio, zero consultas | `group_business_names_test.go:346` — `len(groups) != 0`; `:353` — `len(resolver.batches) != 0` | ✅ PASS |
| 3 | `:128` — `Store.Contacts == nil` → no-op sem pânico | zero consultas, `ContactName` vazio, sem panic | `group_business_names_test.go:417` — `len(resolver.batches) != 0`; `:420` — `ContactName != ""` | ✅ PASS |
| 4 | `:130` — dois participantes com o mesmo `PhoneNumber`, **nenhum** nomeado → ambos recebem o nome | 1 consulta, 2 `ContactName` preenchidos | `group_business_names_test.go:168` — batch com o telefone uma vez; `:171` — `ContactName[i] != "Padaria do Zé LTDA"` para i=0,1 | ✅ PASS |
| 5 | `:132` — **NOVO** dois participantes com o mesmo `PhoneNumber`, **um já nomeado** → nome existente preservado | `"João Silva"` sobrevive; só o sem nome recebe `"Padaria do Zé LTDA"` | `group_business_names_test.go:197` — `ContactName[0] != "João Silva"`; `:200` — `ContactName[1] != "Padaria do Zé LTDA"` | ✅ PASS (fecha a Lacuna 1) |
| 6 | `:135` — **NOVO** resolver falha → **não** reler o contact store | zero `GetContact` após o erro | `group_business_names_test.go:235` — `calls != 0` via `countingContactStore` | ✅ PASS (fecha a Lacuna 2) |
| 7 | `:137` — **NOVO** `/group/list` sem grupos → `Groups` = `[]`, nunca `null` | slice não-nil de tamanho 0 | `group_business_names_test.go:350` — `groups == nil`; cadeia até o JSON por inspeção: `handlers.go:4601` `make([]GroupInfoWithNames, 0, len(infos))` → `:4511` `Groups: enrichGroupList(...)` → `:4488` `Groups []GroupInfoWithNames` (sem `omitempty`) | ✅ PASS (fecha a Lacuna 3) — ver nota Cosmetic 2 |

**Status edge cases**: ✅ 7/7 com evidência.

---

## Discrimination Sensor — Iteração 2

Estado descartável: mutação aplicada por script sobre `handlers.go` (backup em
scratchpad), gate completo `go test ./... -race -count=1` por mutante,
`git checkout -- handlers.go` após cada rodada com verificação
`git status --porcelain -- handlers.go` **vazia** em todas as 5 iterações.
Árvore final: `git status --short` → só `.specs/` untracked; `git diff --stat` vazio.

| # | Mutação | File:line | Descrição | Killed? | Teste que matou |
| --- | --- | --- | --- | --- | --- |
| R1 | re-run do M10 | `handlers.go:4669-4671` | remove `if participant.ContactName != "" { continue }` do loop de aplicação | ✅ **Killed** (antes SURVIVED) | `TestResolveMissingBusinessNamesNeverOverwritesAnExistingName` |
| R2 | re-run do M7 | `handlers.go:4646-4649` | remove o `return` após o erro do resolver (segue para a releitura do store) | ✅ **Killed** (antes SURVIVED) | `TestResolveMissingBusinessNamesSkipsStoreReadOnResolverError` |
| N1 | novo (edge case 7) | `handlers.go:4601` | `out := make([]GroupInfoWithNames, 0, len(infos))` → `var out []GroupInfoWithNames` | ✅ Killed | `TestEnrichGroupListWithNoGroupsIsANoOp` |
| N2 | novo (edge case 5 + AC-1) | `handlers.go:4669` | inverte o guard de aplicação: `ContactName != ""` → `== ""` | ✅ Killed | `...FillsContactName`, `...DedupesAcrossGroups`, `...AppliesToEveryOccurrenceOfAPhone`, `...NeverOverwritesAnExistingName`, `...CapsTheBatch` |
| N3 | novo (edge cases 4-5) | `handlers.go:4672` | aplica o nome por `resolved[participant.JID]` em vez de `resolved[participant.PhoneNumber]` | ✅ Killed | `...AppliesToEveryOccurrenceOfAPhone`, `...NeverOverwritesAnExistingName` |

**Sensor depth**: P0-full. Acumulado da feature: **18 mutações** (13 na iteração 1 +
5 aqui), **18/18 mortas** no estado atual do código.
**Result**: **5/5 killed** — ✅ **PASS**, zero sobreviventes.

---

## Gate Check

- **Gate command**: `go vet ./... && go build -o /dev/null . && go test ./... -race`
- **`go vet ./...`**: ✅ exit 0, sem diagnósticos
- **`go build -o /dev/null .`**: ✅ OK
- **`go test ./... -race -count=1 -v`**: ✅ `ok wuzapi 8.006s`
- **Resultado**: **152 testes de topo — 152 PASS, 0 FAIL, 0 SKIP** (79 subtestes, todos PASS)
- **Test count antes da feature**: 132 (`tasks.md:95`)
- **Iteração 1**: 150 · **Iteração 2**: 152
- **Delta total**: **+20** (6 em `contacts_resync_test.go`, 14 em `group_business_names_test.go`)
- **Integridade**: contagem só cresceu; nenhuma asserção existente foi removida ou
  enfraquecida — `git diff c2630aa..HEAD -- group_business_names_test.go` é 100%
  inserção (70 linhas `+`, 0 `-`).
- **Skipped / Failures**: nenhum.

---

## Checagem de Necessidade — testes novos desta iteração

| Teste / asserção nova | Âncora na spec | OK? |
| --- | --- | --- |
| `TestResolveMissingBusinessNamesNeverOverwritesAnExistingName` (`:179`) | Edge case `spec.md:132-134` (+ reforça P2 AC-3) | ✅ |
| `TestResolveMissingBusinessNamesSkipsStoreReadOnResolverError` (`:206`) | Edge case `spec.md:135-136` (+ P2 AC-7) | ✅ |
| asserção `groups == nil` em `TestEnrichGroupListWithNoGroupsIsANoOp` (`:350`) | Edge case `spec.md:137` (+ P2 AC-2, `spec.md:100-103`) | ✅ |

**Scope creep**: nenhum. 20/20 testes da feature ancorados em AC, edge case listado
ou done-when. Nenhum teste candidato a remoção.

---

## Code Quality

| Princípio | Status |
| --- | --- |
| Minimum code | ✅ — nenhuma linha de produção mudou nesta iteração; os testes reusam `countingContactStore` (`group_participants_test.go:179`) e `newBusinessResolverClient` já existentes |
| Surgical changes | ✅ — só `group_business_names_test.go` e `spec.md` |
| No scope creep | ✅ — a divergência `{"Groups":[]}` da iteração 1 deixou de ser não reivindicada: virou requisito explícito (`spec.md:100-103`, `:137`) com teste e mutante |
| Matches patterns | ✅ — mesmo estilo dos testes vizinhos (fakes locais, `t.Errorf` com `got`/`want`) |
| Spec-anchored outcome check | ✅ — 16/16 ACs com asserção no valor exato da spec; 0 ressalvas de discriminação |
| Per-layer Coverage Expectation met | ✅ — domínio 1:1 com ACs e 7/7 edge cases; rotas cobrem registrada + auth + `no session` conforme `tasks.md:30` |
| Every test maps to a spec requirement | ✅ — 20/20 |
| Documented guidelines followed | ✅ — sem `CONTRIBUTING.md`/`AGENTS.md`; comandos de `.github/workflows/build.yml:42-49` aplicados |

---

## Lacunas Declaradas — Reconfirmadas

| Lacuna | Onde foi declarada | Confirmada? |
| --- | --- | --- |
| `case *events.Contact` em `wmiau.go` sem gate automatizado | `spec.md:43`, `tasks.md:31` | ✅ Inspeção: `wmiau.go:1750-1755`, único `case *events.Contact`, imediatamente antes do `default:`; nenhum `dowebhook` novo |
| Caminhos HTTP que exigem cliente whatsmeow real (200 e os 500 de sync/contagem) | `tasks.md:30` | ✅ Confirmada |
| Parte "registrar em log" de P2 AC-6 sem asserção | `spec.md:42` (**novo** nesta iteração) | ✅ Confirmada — assunção agora explícita na tabela `Assumptions & Open Questions` |
| `API.md` sem gate | `tasks.md:32` | ✅ Confirmada (`API.md:646-681`, `1151-1156`, `1245-1250`, `1299-1327`) |

---

## Lacunas Remanescentes (ranqueadas)

### Cosmetic 1 — `tasks.md` ainda marcado "In Progress" (Lacuna 5 da iteração 1)

- **Onde**: `tasks.md:17` — `**Status**: In Progress`, com T1/T2/T3 todos concluídos
  e verificados.
- **Impacto**: zero em runtime; documento de acompanhamento desatualizado.
- **Fix**: orquestrador atualiza para `Complete` e marca as Success Criteria da
  `spec.md:156-162`.
- **Priority**: Cosmetic

### Cosmetic 2 — Traceability da spec ainda toda "Pending"

- **Onde**: `spec.md:145-149` — CNR-01..CNR-05 seguem com `Status: Pending`, e os
  checkboxes de `Goals` (`spec.md:16-19`) e `Success Criteria` (`spec.md:156-162`)
  seguem desmarcados, embora todos estejam agora verificados.
- **Impacto**: documental. Não afeta cobertura nem discriminação.
- **Fix**: aplicar a tabela `Requirement Traceability Update` abaixo.
- **Priority**: Cosmetic

### Nota (não é lacuna) — o edge case 7 é asserido no domínio, não no JSON

`group_business_names_test.go:350` asserta `groups != nil` na saída de
`enrichGroupList`, não o literal `{"Groups":[]}` de `json.Marshal`. A cadeia é
direta e verificada por inspeção (`handlers.go:4601` → `:4511` → `:4488`, sem
`omitempty`) e o mutante N1 prova a discriminação, então a evidência é suficiente;
um teste de serialização seria estritamente mais forte, mas não é exigido pela spec.

---

## Requirement Traceability Update

| Requirement | Previous Status (iter. 1) | New Status |
| --- | --- | --- |
| CNR-01 (P1 AC 1-2) | ✅ Verified | ✅ Verified |
| CNR-02 (P1 AC 3-6) | ✅ Verified | ✅ Verified |
| CNR-03 (P2 AC 1-5) | ⚠️ com ressalva (AC-2) | ✅ Verified |
| CNR-04 (P2 AC 6-8) | ⚠️ com ressalva (AC-6, AC-7) | ✅ Verified (AC-6 log = lacuna declarada `spec.md:42`) |
| CNR-05 (Edge cases) | ⚠️ com ressalva | ✅ Verified (7/7) |

---

## Summary

**Overall**: ✅ **Ready**

**Spec-anchored check**: 16/16 ACs (P1 8/8, P2 8/8) + 7/7 edge cases com evidência
`arquivo:linha`; 0 spec-precision gaps abertos (o da iteração 1 virou o edge case
`spec.md:132`)
**Sensor**: 5/5 mutantes mortos nesta iteração (R1 e R2, os sobreviventes da
iteração 1, agora morrem); 18/18 acumulados sobre o código atual
**Gate**: `go vet` ✅ · `go build` ✅ · 152 testes, 152 PASS, 0 falhas, 0 skips (+20 vs. 132 pré-feature)

**O que funciona**:
- P1 completo — patch/flags, ordenação sync→contagem, todos os caminhos de erro e a
  integridade da rota
- P2 completo — batch único, dedupe, filtros de coleta, teto de 64, leitura do
  `BusinessName` do store, opt-in exato, degrade sem releitura, guard anti-sobrescrita
- Os 3 desvios/ressalvas da iteração 1 foram fechados no lugar certo: 2 viraram
  teste + edge case na spec, 1 virou assunção declarada
- Zero scope creep: 20/20 testes ancorados; zero linha de produção alterada para
  fechar lacunas de teste

**Issues remanescentes**: 2 itens Cosmetic, ambos **de documentação** e fora do
código (`tasks.md:17` e a traceability de `spec.md:145-149`).

**Next steps**: orquestrador fecha os 2 Cosmetic (Status → Complete, CNR-01..05 →
Verified, checkboxes de Goals/Success Criteria). Nenhuma fix task de código.
