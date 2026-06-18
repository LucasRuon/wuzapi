# Tasks: Múltiplos webhooks por instância

Spec: [spec.md](spec.md) — design embutido nas Decisões (discuss) da spec; sem `design.md` separado.

Sem DDL (R9): nenhuma migration. A coluna `users.webhook TEXT` já comporta o JSON.

| ID  | Task                                                                 | Requisitos     | Arquivos                       | Status |
| --- | -------------------------------------------------------------------- | -------------- | ------------------------------ | ------ |
| T1  | `WebhookTarget` + `parseWebhooks` + validação events + união + serialização | R1, R7, R9     | `webhooks.go` (novo)           | done   |
| T2  | Fan-out em `sendEventWithWebHook`: gating + HMAC por webhook          | R2, R3, R7     | `wmiau.go`                     | done   |
| T3  | `SetWebhook`/`UpdateWebhook` aceitam `webhooks[]`; cap 10; cifra HMAC; grava JSON + união | R4, R8         | `handlers.go`                  | done   |
| T4  | `GetWebhook` devolve `webhooks[]` com `hmac_configured` (sem vazar chave) | R5             | `handlers.go`                  | done   |
| T5  | Testes: parser, fan-out (active/gating/HMAC), união, handlers Set/Get | Verificação    | `webhooks_test.go` (novo)      | done   |
| T6  | `go build ./...`, `go vet`, `go test`                                | Verificação    | —                              | done   |
| T7  | `DELETE /webhook?url=<u>` remove um webhook do array (read-modify-write) | R6 (opcional)  | `webhooks.go`, `handlers.go`   | done   |

## R6 — delete escopado (implementado)

- `DELETE /webhook` (sem query) → zera **todos** (comportamento original).
- `DELETE /webhook?url=<u>` → remove **um** webhook do array, preservando os
  demais verbatim (events herdados, chave cifrada, `active`); recomputa a união
  de events. URL inexistente → `404`. Remover o último → limpa a linha
  (`webhook=''`, `events=''`). Coluna legada só casa a URL armazenada.
