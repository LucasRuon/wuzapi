# Feature: Múltiplos webhooks por instância

## Problema

Hoje cada instância tem **um único** webhook. A URL é guardada na coluna
`users.webhook TEXT` (uma string), lida por `getUserWebhookUrl` ([wmiau.go:158])
e entregue por `sendToUserWebHookWithHmac` ([wmiau.go:223]) num único POST. O
`SetWebhook`/`UpdateWebhook` ([handlers.go:531]/[handlers.go:459]) gravam uma
string única (`UPDATE users SET webhook=$1`).

O único "segundo destino" existente é o **global webhook** (`*globalWebhook`,
[wmiau.go:72]), mas ele é configurado por flag/env **no servidor** e dispara para
**todas** as instâncias — não dá para ter 2 URLs independentes *por* instância.

Necessidade: **≥2 webhooks por instância**, cada um com seus próprios eventos e
chave HMAC.

## Decisões (discuss)

| Gray area        | Decisão                                                                              |
| ---------------- | ----------------------------------------------------------------------------------- |
| Objetivo         | ≥2 webhooks **por instância** (independentes do global webhook)                     |
| Armazenamento    | **Reusar a coluna `users.webhook`** — sem tabela nova, sem DDL. Guarda **array JSON de objetos** |
| Config por hook  | **Independente** — cada webhook com seus próprios `events` e `hmac_key`              |
| Retrocompat      | Coluna com URL "crua" (string que não começa com `[`) = 1 webhook legado, usando `users.events` + `users.hmac_key` |
| HMAC por hook    | Reusa `encryptHMACKey`/`generateHmacSignature`; chave cifrada gravada **base64 dentro do JSON** |
| Global webhook   | **Inalterado** — continua disparando para todas as instâncias, em paralelo aos webhooks da instância |
| stdio mode       | **Inalterado** — entrega via JSON-RPC notification; o array é só config, não muda o transporte |

## Formato de armazenamento

`users.webhook` passa a aceitar dois formatos, distinguidos pelo **primeiro
caractere não-branco**:

- **Legado** (não começa com `[`): string simples = 1 webhook, herdando
  `users.events` e `users.hmac_key`. Comportamento idêntico ao atual.
- **Novo** (começa com `[`): array JSON de objetos
  ```json
  [
    {"url": "https://a.com/hook", "events": ["Message","ReadReceipt"], "hmac_key": "<base64 cifrado>", "active": true},
    {"url": "https://b.com/hook", "events": ["Message"], "active": true}
  ]
  ```
  - `url` — obrigatório, não-vazio.
  - `events` — lista própria; valores validados contra `supportedEventTypes`
    (inválidos descartados, como já é hoje). `["All"]` ou vazio → herda
    `users.events`.
  - `hmac_key` — opcional; **plaintext na entrada da API**, cifrado em repouso via
    `encryptHMACKey` e guardado base64 no JSON. Ausente → sem HMAC (não cai no
    `users.hmac_key`, para manter independência).
  - `active` — opcional, default `true`. `false` = webhook ignorado no dispatch.

## Requisitos

- **R1 (parser retrocompatível)** — Função `parseWebhooks(raw, fallbackEvents,
  fallbackHmac) []WebhookTarget` que: string vazia → `[]`; começa com `[` →
  parseia o array (JSON inválido → loga e retorna `[]`, **não** derruba o
  processo); caso contrário → 1 `WebhookTarget` legado herdando `fallbackEvents`/
  `fallbackHmac`. É a **única** fonte de verdade da interpretação da coluna.

- **R2 (dispatch fan-out)** — `sendEventWithWebHook` ([wmiau.go:169]) passa a
  iterar sobre `parseWebhooks(...)`. Para **cada** webhook `active`: aplica o
  **gating por-webhook** (só envia se o `type` do evento ∈ events do webhook,
  reusando `checkIfSubscribedToEvent`) e faz o POST com a **sua** chave HMAC.
  Cada POST mantém o `safeGo`/fire-and-forget atual ([wmiau.go:116]). O global
  webhook ([wmiau.go:226]) continua disparando **uma vez** por evento, à parte.

- **R3 (HMAC por webhook)** — No POST de cada webhook, o `encryptedHmacKey`
  passado a `callHookWithHmac`/`callHookFileWithHmac` ([helpers.go:260]/[helpers.go:416])
  é o do **próprio** webhook (base64-decode do JSON). Webhook sem `hmac_key` →
  sem assinatura. O caso legado usa `users.hmac_key` como hoje.

- **R4 (escrita — PUT/POST array)** — `SetWebhook`/`UpdateWebhook` aceitam, **além**
  do payload legado (`webhookurl`/`webhook` + `events`), um campo
  `webhooks: [{url, events?, hmac_key?, active?}]`. Quando presente, **substitui**
  o conjunto inteiro: serializa o array (cifrando cada `hmac_key`) e grava na
  coluna `webhook`. Sem `webhooks` → comportamento legado intacto (grava string
  única). `users.events` passa a guardar a **união** dos events de todos os
  webhooks (mantém o filtro de topo e o `GET` legado coerentes).

- **R5 (leitura — GET)** — `GetWebhook` ([handlers.go:391]) retorna, além de
  `{webhook, subscribe}` (legado = 1º webhook, para clientes antigos), o campo
  `webhooks: [{url, events, active, hmac_configured}]`. A chave HMAC **nunca** é
  devolvida em claro — só o booleano `hmac_configured` (espelha [handlers.go:819]).

- **R6 (delete)** — `DeleteWebhook` ([handlers.go:431]) limpa **todos** (zera
  `webhook` e `events`), como hoje. Opcional: `DELETE /webhook?url=<u>` remove
  **um** webhook do array (read-modify-write) — flag de escopo, pode ficar fora do
  MVP.

- **R7 (cache/conexão)** — O `userinfocache` continua guardando a coluna `webhook`
  **crua** (string JSON ou legado) em `Values["Webhook"]`; o parse acontece no
  dispatch. `connectOnStartup` ([wmiau.go:245]) e `SetWebhook` só precisam manter o
  `Values["Webhook"]` e `Values["Events"]` (união) coerentes — **sem** mudança no
  schema do cache.

- **R8 (limite)** — Cap de **10** webhooks por instância na escrita (evita abuso);
  acima disso → `400`. URL vazia no array → `400`.

- **R9 (sem DDL)** — **Nenhuma** migration nova. A coluna `webhook TEXT NOT NULL
  DEFAULT ''` já comporta o JSON. Linhas existentes (URL crua) seguem válidas via R1.

## Verificação

- `go build ./...` e `go vet` limpos.
- **Teste — parser (R1):** `""` → `[]`; `"https://x"` → 1 target legado herdando
  events/hmac; array JSON válido → N targets com config própria; JSON malformado
  começando com `[` → `[]` + log (não panica); `active:false` filtrado no consumo.
- **Teste — fan-out (R2):** evento que casa events de 2 webhooks → 2 POSTs; evento
  que casa só 1 → 1 POST; webhook `active:false` → 0 POSTs.
- **Teste — HMAC por webhook (R3):** dois webhooks com chaves distintas → cada POST
  assinado com a chave certa; webhook sem chave → sem header de assinatura.
- **Teste — escrita/leitura (R4/R5):** `PUT /webhook` com `webhooks:[a,b]` grava
  array; `GET` devolve `webhooks` com 2 entradas + `hmac_configured` correto e
  **sem** vazar chave; `users.events` = união.
- **Manual:** configurar 2 webhooks via `PUT /webhook`, enviar 1 mensagem e
  confirmar que **ambos** os endpoints recebem, cada um com sua própria assinatura.
- **Retrocompat:** instância com webhook legado (URL crua) continua entregando 1
  POST usando `users.events`/`users.hmac_key`, sem reconfiguração.
</content>
</invoke>
