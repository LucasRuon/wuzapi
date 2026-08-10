# Anti-Ban Guardrails — Context

**Gathered:** 2026-08-09
**Spec:** `.specs/features/anti-ban-guardrails/spec.md`
**Status:** Aguardando confirmação da spec

---

## Feature Boundary

Transformar o fork da WuzAPI de gateway passivo em **guardião ativo da reputação
do número**: estado de banimento, quota/ritmo/janela por instância, pré-validação
de destinatário, lista de supressão e observabilidade — tudo imposto no servidor
Go. O broadcast-app passa a *reagir* a esses sinais (423/429/422) em vez de ser a
única camada que os aplica.

Não inclui: migração para Cloud API, pool de números rotativos, contratação de
proxies, fila externa, ou NLP de conteúdo.

---

## Implementation Decisions

### Camada onde vive a proteção

- **Fork Go.** É o único ponto por onde todo consumidor passa (broadcast-app, CRM,
  apps futuros). Regra no app é convenção; regra no gateway é garantia.
- O broadcast-app **mantém** o jitter de 45–75 s — ele continua propondo o ritmo.
  O fork apenas garante que ninguém fure o piso. Na operação normal os dois nunca
  conflitam; o 429 só aparece quando há bug, concorrência ou segundo consumidor.

### Contrato de erro (decisão de API)

- `423 Locked` → instância travada (ban temporário, ban permanente, auto-pausa).
  Corpo carrega `code`, `reason`, `until`. O app pausa a campanha.
- `429 Too Many Requests` → você foi rápido/volumoso demais agora. Header
  `Retry-After`. O app espera e **repete o mesmo destinatário**.
- `422 Unprocessable Entity` → este destinatário específico é inválido
  (não existe no WhatsApp, ou está suprimido). O app pula e segue.

Rejeitar em vez de enfileirar: mantém o contrato REST atual dos `/chat/send/*`
intacto e o agendamento sob controle do app. Fila no gateway muda a semântica de
todos os endpoints de envio e fica para uma v2, se necessário.

### Multi-tenancy

- Todo estado é **por instância** (`user_id`), nunca global: quota, warmup,
  ban, supressão, contadores, janela, fingerprint.
- Fingerprint de dispositivo precisa de mutex global no pareamento porque
  `store.DeviceProps` é variável de pacote no whatsmeow — não há API per-client.

### Calibração de volume (listas opt-in, alvo 200/dia)

Rampa de warmup por dias desde o pareamento:

| Dias | Quota/dia |
| --- | --- |
| 0–1 | 30 |
| 2–3 | 60 |
| 4–6 | 100 |
| 7–13 | 150 |
| 14+ | 200 |

Piso de intervalo 45 s (só pode subir). Janela padrão 09:00–20:00
`America/Sao_Paulo`, sem domingo. 200 msg × ~60 s = ~3,3 h — cabe folgado.

Se as listas deixarem de ser opt-in, estes números precisam cair ~70 % e a
prioridade muda de "pacing" para "reduzir taxa de denúncia".

### Limiares de auto-pausa

- Bloqueios em 24 h ≥ `max(3, 1,5 % dos enviados em 24 h)` → pausa 24 h.
- 5 falhas de envio consecutivas → pausa 30 min.

Escolhidos conservadores de propósito: o custo de uma pausa falsa-positiva
(campanha atrasada algumas horas) é ordens de grandeza menor que o de um ban.

### Agent's Discretion

- Formato exato do payload dos novos webhooks (`InstancePaused`, `OptOut`,
  `ContentRepetitionWarning`) — seguir o formato já usado pelos eventos existentes.
- Conjunto curado de combinações `device_os`/`device_platform` para o fingerprint
  determinístico.
- Estrutura interna do governor (mutex por instância vs. `singleflight` vs.
  `UPDATE ... WHERE` condicional) — desde que o AC de atomicidade seja satisfeito.

---

## Specific References

- Códigos confirmados no whatsmeow (via Context7, `pkg.go.dev/go.mau.fi/whatsmeow`):
  - `events.TempBanReason`: 101 `SentToTooManyPeople`, 102 `BlockedByUsers`,
    103 `CreatedTooManyGroups`, 104 `SentTooManySameMessage`, 106 `BroadcastList`.
  - `events.ConnectFailureReason`: 401 `LoggedOut`, 402 `TempBanned`,
    403 `MainDeviceGone` (LOCKED), 406 `UnknownLogout` (BANNED no WA Web).
  - `store.DeviceProps` é `var` de pacote — confirma a race em `wmiau.go:585-586`.
- Pontos do código citados na spec:
  - `wmiau.go:1662` — `TemporaryBan` só logado, sem código nem duração.
  - `wmiau.go:406-412` — `IsOnWhatsApp` restrito a celular BR ambíguo.
  - `wmiau.go:585-586` — escrita em `store.DeviceProps` global por `startClient`.
  - `src/app/api/broadcast/route.ts:20-32` — jitter 45–75 s (única proteção hoje).
  - `src/app/api/broadcast/route.ts:150` — `void runBroadcast(...)`, floating promise.
  - `src/lib/instance.ts:24` — assinatura de eventos sem `TemporaryBan`.

---

## Deferred Ideas

- Fila de envio interna ao gateway com `202 Accepted` + `jobId` (substituiria o 429).
- Pool de números rotativos com balanceamento por reputação.
- Re-encode de mídia por destinatário para variar o hash (custo de CPU alto;
  alternativa barata: o app gera 3–5 variantes e faz round-robin).
- Métricas Prometheus além do endpoint de health.
- `InsecureSkipVerify: true` no resty client (`wmiau.go:607`) — problema de
  segurança real, mas ortogonal a banimento. Vale um issue separado.
