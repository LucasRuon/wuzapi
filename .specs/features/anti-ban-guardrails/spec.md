# Anti-Ban Guardrails — Specification

## Problem Statement

O fork da WuzAPI é hoje um gateway *burro*: aceita qualquer volume, em qualquer
ritmo, para qualquer número, a qualquer hora — e **não reage a um banimento**.
O evento `events.TemporaryBan` (que o WhatsApp emite com códigos explícitos:
`101 SentToTooManyPeople`, `102 BlockedByUsers`, `104 SentTooManySameMessage`)
é apenas logado (`wmiau.go:1662`) e nem sequer chega aos consumidores, porque o
broadcast-app assina só `Message,PairSuccess,Connected,Disconnected,LoggedOut`.
Toda a proteção real vive num loop do Next.js (`src/app/api/broadcast/route.ts:20-32`)
que qualquer segundo consumidor, retry ou requisição concorrente contorna.

Resultado: o número de um cliente pode estar banido há horas e o sistema segue
enfileirando envios, queimando reputação e sem nenhum sinal para o operador.

## Goals

- [ ] **Nenhum envio sai de uma instância banida ou em risco** — o gateway recusa,
      não o cliente. Zero envios após `TemporaryBan` até o `Expire` passar.
- [ ] **Quota e ritmo garantidos no servidor** — teto de 200 msg/dia por instância
      com rampa de warmup, piso de 45 s entre envios e janela horária, todos
      invioláveis por qualquer consumidor da API.
- [ ] **Detecção precoce** — bloqueios por usuários e falhas em sequência pausam a
      instância automaticamente *antes* do ban, com webhook de alerta.
- [ ] **Zero envio para número inexistente** — pré-validação obrigatória com cache.
- [ ] **Opt-out honrado** — lista de supressão por instância, alimentada por
      resposta do destinatário, bloqueio, e API.
- [ ] **Observabilidade** — um endpoint responde "esta instância está saudável?"
      com quota usada, estado de ban, bloqueios em 24 h e taxa de falha.

## Out of Scope

| Feature | Reason |
| ------- | ------ |
| Migração para WhatsApp Cloud API oficial | Decisão de produto/custo, não técnica. O fork continua sendo o gateway. |
| Pool de números rotativos / orquestração multi-número | Alvo travado em 200/dia por número. Só faz sentido acima de 800/dia. |
| Proxy residencial por tenant | O fork já suporta `proxy_url` por usuário (`wmiau.go:619-645`). Provisionar/contratar proxies é operação, não código. |
| Reescrita do agendador do broadcast-app para fila externa (Redis/BullMQ) | Resolvível com worker in-process + retomada no boot. Fila externa é otimização posterior. |
| Detecção de conteúdo "spam" por NLP | Heurística frágil, alto falso-positivo. Variação de conteúdo cobre o vetor real (ban 104). |
| Warmup automatizado com conversas sintéticas | Criar tráfego artificial é o padrão que o WhatsApp caça. Warmup aqui = rampa de volume real. |

---

## Assumptions & Open Questions

| Assumption / decisão | Escolha | Racional | Confirmado? |
| --- | --- | --- | --- |
| Camada onde vive a proteção | Fork Go (gateway) | Único ponto por onde todo consumidor passa. broadcast-app, CRM e apps futuros herdam de graça. | ✅ usuário |
| Modelo de uso | Multi-tenant (vários clientes, um número cada) | Exige quota/warmup/estado de ban **por instância**, não global. | ✅ usuário |
| Natureza das listas | Opt-in, base própria | Vetor de denúncia é baixo → permite os 200/dia. Se virasse lista fria, as quotas cairiam ~70 %. | ✅ usuário |
| Teto de volume | 200 msg/dia por instância, configurável | Cabe em ~3,3 h de envio a 60 s médios; folgado na janela 09–20. | ✅ usuário |
| Comportamento quando a quota/pacing é violado | HTTP 429 + `Retry-After` (rejeita), **não** enfileira | Mantém o contrato REST e o app dono do agendamento. Fila no gateway muda a semântica de todos os `/chat/send/*` e é escopo maior. | Assumido |
| Comportamento quando a instância está banida/pausada | HTTP 423 Locked + corpo com `code`/`until` | 423 distingue "instância travada" de "você foi rápido demais" (429) — o app reage diferente a cada um. | Assumido |
| Persistência do estado anti-ban | Tabelas novas no mesmo banco do wuzapi (SQLite/Postgres) via `migrations.go` | Já existe o mecanismo (`migrations.go`), e o estado precisa sobreviver a restart. | Assumido |
| Fingerprint por instância | Serializar `store.DeviceProps` com mutex durante o pareamento | `store.DeviceProps` é global de pacote no whatsmeow — não há API per-client. Só importa no registro, então o lock cobre a janela inteira. | Assumido |
| Fuso da janela de envio | `America/Sao_Paulo`, configurável por instância | Base 100 % brasileira hoje. | Assumido |

**Open questions:** nenhuma — tudo resolvido ou registrado acima.

---

## User Stories

### P1: Circuit breaker de banimento ⭐ MVP

**User Story**: Como operador do gateway, quero que uma instância banida pare de
enviar imediatamente e me avise, para não queimar ainda mais a reputação do número
nem entregar erro silencioso ao cliente.

**Why P1**: É o buraco mais grave. Hoje o ban é invisível e o sistema continua
disparando. Sem isso, todo o resto é otimização de margem.

**Acceptance Criteria**:

1. WHEN o whatsmeow emite `events.TemporaryBan` THEN o gateway SHALL persistir
   `ban_state='temp'`, `ban_code=evt.Code`, `ban_reason=evt.Code.String()` e
   `ban_until=now+evt.Expire` para aquela instância.
2. WHEN uma instância tem `ban_state='temp'` e `ban_until` no futuro THEN qualquer
   requisição a `/chat/send/*` SHALL responder `423` com corpo
   `{"code":423,"success":false,"error":"instance_banned","banCode":<int>,"reason":"<string>","until":"<RFC3339>"}`
   e SHALL NOT chamar `whatsmeow.SendMessage`.

   > **Corrigido em T2 (2026-08-10).** A redação original pedia o código do ban
   > no campo `code`. O envelope de `s.Respond` (`handlers.go:6199`) já usa `code`
   > para o status HTTP em **toda** resposta da API, e o broadcast-app lê
   > `payload.error`/`payload.success` (`src/lib/wuzapi.ts:53-59`). Mudar o
   > significado de `code` só nestas rotas quebraria clientes existentes, então o
   > código do ban vai em `banCode`.
3. WHEN `events.TemporaryBan` é recebido THEN o gateway SHALL despachar um webhook
   de tipo `TemporaryBan` contendo `code`, `reason` e `expire` em segundos
   (hoje o payload não carrega nenhum dos três).
4. WHEN `events.ConnectFailure` chega com `Reason` ∈ {`402 TempBanned`,
   `406 UnknownLogout`} THEN o gateway SHALL marcar `ban_state='temp'` (402) ou
   `ban_state='perm'` (406) e SHALL incluir `reasonCode` e `reasonName` no webhook.
5. WHEN `ban_until` já passou THEN a próxima requisição de envio SHALL limpar
   `ban_state` para `'ok'` e SHALL prosseguir normalmente.
6. WHEN uma instância tem `ban_state='perm'` THEN `/chat/send/*` SHALL responder
   `423` indefinidamente até um `POST /session/ban/clear` explícito do admin.

**Independent Test**: injetar `events.TemporaryBan{Code:101, Expire:24h}` no event
handler → `GET /session/health` mostra `banState:"temp"`, `banCode:101`;
`POST /chat/send/text` devolve 423; avançar o relógio além de `ban_until` → 200.

---

### P1: Governor de envio (quota, warmup, pacing, janela) ⭐ MVP

**User Story**: Como operador, quero que o gateway imponha teto diário, rampa de
aquecimento, intervalo mínimo e janela horária por instância, para que nenhum
consumidor — nem um bug meu — consiga disparar rápido ou demais.

**Why P1**: Move a única proteção existente (jitter no Next.js) para a camada que
todo mundo atravessa. Sem isso a regra é uma convenção, não uma garantia.

**Acceptance Criteria**:

1. WHEN uma instância já enviou `daily_quota` mensagens no dia corrente (fuso da
   instância) THEN `/chat/send/*` SHALL responder `429` com header `Retry-After`
   igual aos segundos até a virada do dia.
2. WHEN o envio anterior daquela instância ocorreu há menos de `min_interval_ms`
   (padrão 45 000) THEN `/chat/send/*` SHALL responder `429` com `Retry-After`
   igual aos segundos restantes.
3. WHEN a instância foi pareada há `d` dias completos THEN `daily_quota` SHALL ser
   `min(max_daily_quota, ramp(d))`, com `ramp` = 30 (d0–1), 60 (d2–3), 100 (d4–6),
   150 (d7–13), 200 (d14+).
4. WHEN o horário local da instância está fora de `[window_start, window_end]`
   (padrão 09:00–20:00) ou é domingo THEN `/chat/send/*` SHALL responder `429`
   com `Retry-After` igual aos segundos até a próxima abertura de janela.
5. WHEN um envio é aceito pelo governor THEN o contador `sent_today` SHALL ser
   incrementado atomicamente e `last_send_at` atualizado, **antes** da chamada ao
   whatsmeow, de modo que duas requisições concorrentes nunca passem as duas.
6. WHEN a chamada ao whatsmeow falha após o governor ter aceitado THEN
   `sent_today` SHALL NOT ser decrementado (o envio consumiu tentativa de rede
   real; devolver a cota permitiria loop de retry infinito).
7. WHEN `max_daily_quota`, `min_interval_ms`, janela ou fuso não estão definidos
   para a instância THEN o governor SHALL usar os padrões globais dos flags/env.

**Independent Test**: `POST /chat/send/text` duas vezes seguidas → segunda devolve
429 com `Retry-After ≈ 45`. Setar `sent_today = daily_quota` → 429 com
`Retry-After` até meia-noite. Setar janela 09–10 e relógio 23 h → 429.

---

### P1: Pré-flight de destinatário

**User Story**: Como operador, quero que o gateway recuse enviar para números que
não existem no WhatsApp, porque tentativa de envio para número inexistente é um
dos sinais mais fortes de disparo automatizado.

**Why P1**: Hoje `IsOnWhatsApp` só roda para celular BR com ambiguidade de 9º
dígito (`wmiau.go:406-412`). Qualquer outro número inválido vai direto ao WhatsApp.

**Acceptance Criteria**:

1. WHEN um envio tem destinatário com `Server == types.DefaultUserServer` THEN o
   gateway SHALL resolver o JID via `IsOnWhatsApp` (reaproveitando `phoneJIDCache`,
   TTL 24 h) antes de enviar.
2. WHEN `IsOnWhatsApp` responde que nenhuma variante está registrada THEN
   `/chat/send/*` SHALL responder `422` com
   `{"error":"recipient_not_on_whatsapp","phone":"<digits>"}` e SHALL NOT enviar.
3. WHEN `IsOnWhatsApp` falha (timeout/erro de rede) THEN o gateway SHALL enviar
   com o JID original e SHALL registrar warning — nunca piorar o envio por falha
   de checagem.
4. WHEN o destinatário é grupo, newsletter, LID ou broadcast THEN a pré-validação
   SHALL ser ignorada (no-op).
5. WHEN um número é resolvido com sucesso THEN o resultado SHALL ser cacheado por
   24 h, de modo que uma campanha de 200 contatos gere no máximo 200 consultas.

**Independent Test**: enviar para `5511900000000` (inexistente) → 422 sem tocar o
whatsmeow. Enviar duas vezes para o mesmo número válido → apenas uma consulta
`IsOnWhatsApp` (verificar por contador/log).

---

### P1: Lista de supressão e opt-out

**User Story**: Como operador, quero que quem pediu para sair, bloqueou o número
ou foi marcado como suprimido nunca mais receba mensagem daquela instância.

**Why P1**: Denúncia e bloqueio são o vetor dominante do ban 102. Continuar
enviando para quem bloqueou é o pior sinal possível — e hoje nada impede.

**Acceptance Criteria**:

1. WHEN existe registro de supressão para (instância, JID) THEN `/chat/send/*`
   SHALL responder `422` com `{"error":"recipient_suppressed","reason":"<motivo>"}`.
2. WHEN chega `events.BlocklistChange` com ação de bloqueio THEN o gateway SHALL
   inserir supressão com `reason='blocked'` para aquele JID.
3. WHEN chega `events.Message` de entrada cujo texto, normalizado (trim, minúsculas,
   sem acento, sem pontuação), casa exatamente com um dos termos
   {`sair`, `parar`, `pare`, `stop`, `descadastrar`, `cancelar`, `remover`} THEN o
   gateway SHALL inserir supressão com `reason='opt_out'` e SHALL despachar webhook
   `OptOut` com o JID.
4. WHEN o admin chama `POST /user/suppress {"phone":"...","reason":"..."}` THEN o
   JID SHALL ser suprimido; `DELETE /user/suppress` SHALL removê-lo.
5. WHEN `GET /user/suppress` é chamado THEN o gateway SHALL retornar a lista de
   supressões daquela instância.
6. WHEN uma mensagem de entrada tem texto que *contém* mas não *é* um termo de
   opt-out (ex.: "não quero parar de receber") THEN o gateway SHALL NOT suprimir
   (casamento é exato sobre o texto inteiro normalizado, não substring).

**Independent Test**: enviar "SAIR" como mensagem de entrada simulada → aparece em
`GET /user/suppress`; envio subsequente para aquele número devolve 422.

---

### P2: Pausa automática por sinal de risco

**User Story**: Como operador, quero que a instância se auto-pause quando os
bloqueios ou as falhas passarem de um limiar, para reagir antes do ban, não depois.

**Why P2**: É prevenção, não contenção — vale muito, mas o P1 precisa existir
primeiro para haver estado onde gravar a pausa.

**Acceptance Criteria**:

1. WHEN os bloqueios registrados nas últimas 24 h atingem
   `max(3, 0.015 × enviados_24h)` THEN o gateway SHALL setar
   `ban_state='self_paused'` com `ban_until = now + 24h` e `ban_reason='block_rate'`.
2. WHEN 5 envios consecutivos falham na mesma instância THEN o gateway SHALL setar
   `ban_state='self_paused'` com `ban_until = now + 30min` e
   `ban_reason='consecutive_failures'`.
3. WHEN um envio é bem-sucedido THEN o contador de falhas consecutivas SHALL zerar.
4. WHEN uma auto-pausa é acionada THEN o gateway SHALL despachar webhook
   `InstancePaused` com `reason`, `until` e as métricas que a dispararam.
5. WHEN `ban_state='self_paused'` THEN `/chat/send/*` SHALL responder `423`
   (mesmo contrato do ban), distinguível pelo campo `reason`.

**Independent Test**: simular 5 falhas seguidas → `GET /session/health` mostra
`banState:"self_paused"`, `banReason:"consecutive_failures"`; envio devolve 423.

---

### P2: Sinais de comportamento humano

**User Story**: Como operador, quero que o gateway simule digitação antes de enviar
e marque conversas como lidas, para que o padrão de tráfego da instância não seja
o de um robô que só emite.

**Why P2**: Reduz a assinatura comportamental. Os endpoints já existem
(`/chat/presence`, `/chat/markread`, `/user/presence`) — falta orquestrar.

**Acceptance Criteria**:

1. WHEN a instância tem `simulate_typing=true` (padrão) e um envio de texto é
   aceito pelo governor THEN o gateway SHALL emitir `SendChatPresence(composing)`,
   aguardar `clamp(len(texto)/12 segundos, 1.5s, 6s)`, emitir
   `SendChatPresence(paused)` e só então chamar `SendMessage`.
2. WHEN `SendChatPresence` falha THEN o gateway SHALL prosseguir com o envio e
   registrar warning — a simulação nunca bloqueia a mensagem.
3. WHEN a instância tem `auto_read=true` e chega `events.Message` de entrada THEN
   o gateway SHALL chamar `MarkRead` após um atraso aleatório de 3–15 s.
4. WHEN `simulate_typing=false` THEN o comportamento SHALL ser idêntico ao atual
   (sem presença, sem atraso adicional).

**Independent Test**: com `simulate_typing=true`, medir o tempo de resposta de
`/chat/send/text` para um corpo de 60 caracteres → entre 1,5 s e 6 s a mais que
com a flag desligada.

---

### P2: Variação de conteúdo

**User Story**: Como operador, quero detectar (e ter como evitar) o envio da mesma
mensagem byte-a-byte para muitos destinatários, porque `TempBanSentTooManySameMessage`
(104) é exatamente esse padrão.

**Why P2**: Hoje `renderMessage` só troca `{primeiro_nome}`; o corpo e o hash da
mídia são idênticos para todos os 200 contatos.

**Acceptance Criteria**:

1. WHEN um envio de texto é aceito THEN o gateway SHALL calcular o SHA-256 do
   corpo normalizado (minúsculas, espaços colapsados) e registrá-lo numa janela
   deslizante das últimas 100 mensagens daquela instância.
2. WHEN mais de 80 % das últimas 50 mensagens compartilham o mesmo hash THEN o
   gateway SHALL despachar webhook `ContentRepetitionWarning` com a taxa medida.
3. WHEN `reject_duplicate_content=true` (padrão `false`) e o limiar do AC-2 é
   ultrapassado THEN `/chat/send/*` SHALL responder `429` com
   `{"error":"content_too_repetitive"}`.
4. WHEN o broadcast-app compõe uma mensagem THEN SHALL suportar spintax
   `{opção A|opção B}` no corpo, sorteando uma variante por destinatário.
5. WHEN o corpo não contém spintax THEN o texto SHALL sair inalterado (retro-compatível).

**Independent Test**: enviar 50 mensagens idênticas → webhook `ContentRepetitionWarning`
com taxa 1.0. Corpo `"Oi {tudo bem|como vai}?"` renderizado 100 vezes → ambas as
variantes aparecem.

---

### P2: Fingerprint por instância (corrigindo a race)

**User Story**: Como operador multi-tenant, quero que cada instância se registre
com um fingerprint de dispositivo próprio e sem corrida entre pareamentos
simultâneos.

**Why P2**: `store.DeviceProps` é global de pacote no whatsmeow; `wmiau.go:585-586`
escreve nela a cada `startClient`. Dois pareamentos concorrentes podem trocar o
fingerprint um do outro, e hoje todos os tenants se apresentam como "Mac OS 10 /
DESKTOP".

**Acceptance Criteria**:

1. WHEN duas instâncias iniciam o pareamento concorrentemente THEN a escrita em
   `store.DeviceProps` e o `client.Connect()`/pareamento SHALL ocorrer sob um
   mutex global, de modo que nenhuma instância registre com os props da outra.
2. WHEN uma instância tem `device_os` / `device_platform` definidos THEN esses
   valores SHALL ser usados no lugar dos flags globais.
3. WHEN não há valores por instância THEN o gateway SHALL derivar um par
   determinístico a partir do `userID` (hash → índice num conjunto curado de
   combinações plausíveis), de modo que a mesma instância sempre pareie igual.
4. WHEN a instância já está registrada (`client.Store.ID != nil`) THEN
   `store.DeviceProps` SHALL NOT ser tocada (só importa no registro).

**Independent Test**: parear duas instâncias em paralelo com `device_os` distintos
→ cada `whatsmeow_device` no banco guarda o seu próprio; teste com `-race` passa.

---

### P2: Health e observabilidade por instância

**User Story**: Como operador, quero um endpoint que responda "esta instância está
saudável?" para monitorar sem cavar log.

**Why P2**: Sem isso as proteções acima são invisíveis.

**Acceptance Criteria**:

1. WHEN `GET /session/health` é chamado com token de instância THEN SHALL retornar
   `{banState, banCode, banReason, banUntil, sentToday, dailyQuota, warmupDay,
   nextSendAllowedAt, windowOpen, blocked24h, suppressedCount,
   consecutiveFailures, connected, loggedIn}`.
2. WHEN a instância nunca enviou THEN os contadores SHALL retornar 0, não `null`.
3. WHEN `GET /admin/health/instances` é chamado com token de admin THEN SHALL
   retornar o mesmo resumo para todas as instâncias.

**Independent Test**: `GET /session/health` numa instância recém-pareada →
`sentToday:0`, `dailyQuota:30`, `warmupDay:0`, `banState:"ok"`.

---

### P2: Broadcast retomável (broadcast-app)

**User Story**: Como usuário do disparador, quero que um restart do servidor no
meio de uma campanha não perca a campanha nem duplique mensagens.

**Why P2**: `void runBroadcast(...)` (`src/app/api/broadcast/route.ts:150`) é um
floating promise. Restart = broadcast órfão em `status:"running"` para sempre.

**Acceptance Criteria**:

1. WHEN o broadcast-app inicia THEN SHALL retomar todo `Broadcast` com
   `status='running'`, continuando pelos `Recipient` com `status='pending'`.
2. WHEN uma mensagem é enviada THEN o app SHALL informar um `Id` determinístico
   (derivado de `broadcastId + recipientId`) no payload da WuzAPI, de modo que um
   reenvio após crash seja idempotente do lado do WhatsApp.
3. WHEN a WuzAPI responde `423` THEN o app SHALL marcar o broadcast como
   `status='paused'` com o motivo, SHALL NOT marcar os destinatários restantes
   como falhos, e SHALL reagendar a retomada para depois de `until`.
4. WHEN a WuzAPI responde `429` THEN o app SHALL aguardar `Retry-After` e repetir
   o **mesmo** destinatário, sem consumi-lo como falha.
5. WHEN a WuzAPI responde `422` THEN o destinatário SHALL ser marcado
   `status='skipped'` com o motivo, e o broadcast SHALL seguir para o próximo.
6. WHEN o broadcast termina THEN `status` SHALL ser `'done'` e todo `Recipient`
   SHALL estar em `sent`, `failed` ou `skipped` — nenhum em `pending`.

**Independent Test**: iniciar campanha de 5 contatos, matar o processo após o 2º,
reiniciar → os 3 restantes são enviados e nenhum dos 2 primeiros repete.

---

### P3: Assinatura de eventos completa (broadcast-app)

**User Story**: Como usuário, quero ver no painel que meu número foi banido ou
pausado, em vez de descobrir por mensagens que não chegam.

**Acceptance Criteria**:

1. WHEN uma instância é provisionada THEN `events` SHALL incluir `TemporaryBan`,
   `ConnectFailure`, `BlocklistChange`, `StreamError`, `ClientOutdated` além dos atuais.
2. WHEN o webhook recebe `TemporaryBan` ou `InstancePaused` THEN o app SHALL
   persistir o estado na `Instance` e exibi-lo no painel de conexão.

---

## Edge Cases

- WHEN o relógio do servidor vira o dia no meio de uma campanha THEN `sent_today`
  SHALL zerar e a campanha SHALL continuar sob a nova quota.
- WHEN duas requisições de envio chegam no mesmo milissegundo THEN exatamente uma
  SHALL passar (o incremento do contador é a seção crítica).
- WHEN a instância é despareada e repareada THEN `warmup_started_at` SHALL reiniciar
  (número que voltou do zero é tratado como novo).
- WHEN `Expire` do `TemporaryBan` vem zerado ou ausente THEN o gateway SHALL
  assumir 24 h.
- WHEN a lista de supressão tem 100 000 entradas THEN a checagem de envio SHALL
  usar índice em `(user_id, jid)` — nunca varredura.
- WHEN o gateway reinicia THEN todo estado anti-ban (quota, ban, supressão,
  contadores) SHALL ser lido do banco, não reiniciado em memória.
- WHEN `min_interval_ms` configurado é menor que 45 000 THEN o gateway SHALL usar
  45 000 (o piso só pode subir — mesma regra já adotada no broadcast-app).

---

## Requirement Traceability

| ID | Story | Fase | Status |
| --- | --- | --- | --- |
| BAN-01 | P1: Circuit breaker — persistir TemporaryBan | Design | Pending |
| BAN-02 | P1: Circuit breaker — 423 em instância banida | Design | Pending |
| BAN-03 | P1: Circuit breaker — webhook com code/reason/expire | Design | Pending |
| BAN-04 | P1: Circuit breaker — ConnectFailure 402/406 | Design | Pending |
| BAN-05 | P1: Circuit breaker — expiração e clear manual | Design | Pending |
| BAN-06 | P1: Governor — quota diária | Design | Pending |
| BAN-07 | P1: Governor — piso de intervalo | Design | Pending |
| BAN-08 | P1: Governor — rampa de warmup | Design | Pending |
| BAN-09 | P1: Governor — janela horária | Design | Pending |
| BAN-10 | P1: Governor — incremento atômico | Design | Pending |
| BAN-11 | P1: Governor — padrões globais | Design | Pending |
| BAN-12 | P1: Pré-flight — IsOnWhatsApp universal | Design | Pending |
| BAN-13 | P1: Pré-flight — 422 para inexistente | Design | Pending |
| BAN-14 | P1: Pré-flight — fallback e cache | Design | Pending |
| BAN-15 | P1: Supressão — bloqueio de envio | Design | Pending |
| BAN-16 | P1: Supressão — via BlocklistChange | Design | Pending |
| BAN-17 | P1: Supressão — via opt-out inbound | Design | Pending |
| BAN-18 | P1: Supressão — CRUD por API | Design | Pending |
| BAN-19 | P2: Auto-pausa por taxa de bloqueio | - | Pending |
| BAN-20 | P2: Auto-pausa por falhas consecutivas | - | Pending |
| BAN-21 | P2: Webhook InstancePaused | - | Pending |
| BAN-22 | P2: Simulação de digitação | - | Pending |
| BAN-23 | P2: Auto-read com atraso | - | Pending |
| BAN-24 | P2: Detector de repetição de conteúdo | - | Pending |
| BAN-25 | P2: Spintax no broadcast-app | - | Pending |
| BAN-26 | P2: Fingerprint por instância sob mutex | - | Pending |
| BAN-27 | P2: GET /session/health | - | Pending |
| BAN-28 | P2: GET /admin/health/instances | - | Pending |
| BAN-29 | P2: Retomada de broadcast no boot | - | Pending |
| BAN-30 | P2: Idempotência por Id determinístico | - | Pending |
| BAN-31 | P2: Reação a 423/429/422 no app | - | Pending |
| BAN-32 | P3: Assinatura de eventos completa | - | Pending |
| BAN-33 | P3: Estado de ban no painel | - | Pending |

**Cobertura:** 33 requisitos; 18 em P1 (MVP).

---

## Success Criteria

- [ ] Injetar `TemporaryBan` numa instância → **zero** envios subsequentes até o
      `Expire`, verificado por teste automatizado.
- [ ] Um cliente malicioso/bugado que chame `/chat/send/text` em loop apertado
      recebe 429 e **não** consegue enviar mais que 1 msg / 45 s / 200 por dia.
- [ ] Campanha de 200 contatos com 5 números inexistentes → 195 enviados,
      5 recusados com 422, **zero** tentativas ao WhatsApp para os inexistentes.
- [ ] Matar o processo do broadcast-app no meio de uma campanha e reiniciar →
      campanha completa sem duplicata.
- [ ] `GET /session/health` responde em < 50 ms e reflete o estado real.
- [ ] `go test ./... -race` passa.
