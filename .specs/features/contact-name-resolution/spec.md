# Contact Name Resolution Specification

## Problem Statement

Participantes de grupo voltam sem nome (`ContactName`, `DisplayName` e
`RedactedPhone` vazios) porque os três campos saem exclusivamente da tabela local
`whatsmeow_contacts`, que só é populada por (a) app state sync da agenda do
celular pareado, (b) push name embutido em mensagens recebidas e (c) redacted
phone de grupos de anúncio. Quem não está na agenda e nunca falou fica só com o
número. Além disso, o fork nunca re-sincroniza a agenda: `FetchAppState` só é
chamado para `WAPatchRegular` (etiquetas), então contatos perdidos num
re-pareamento (a tabela é escopada por `our_jid`) nunca voltam.

## Goals

- [ ] Um endpoint que force o re-sync completo da agenda do celular pareado,
      repovoando `first_name`/`full_name` de todos os contatos salvos.
- [ ] Um fallback opt-in que resolva nome de conta comercial (verified name) de
      participantes de grupo sem nome nenhum, em uma única consulta em lote.

## Out of Scope

| Feature | Reason |
| --- | --- |
| Resolver push name sob demanda | O WhatsApp não expõe push name por consulta; ele só chega embutido em mensagens ou no history sync. Não há API a chamar. |
| Fallback automático (sem parâmetro) nos endpoints de grupo | Decisão do usuário: `resolveBusiness` é opt-in. Sem o parâmetro, os endpoints mantêm zero chamadas de rede. |
| Formatar o `PhoneNumber` como nome de exibição | Decisão de apresentação do consumidor; a API já devolve `PhoneNumber` cru. |
| Persistir nomes numa tabela própria do fork | `whatsmeow_contacts` já é a fonte única; duplicar cria divergência. |
| Re-sync automático no boot ou por timer | O custo/risco de um full sync periódico não foi avaliado; o endpoint dá o controle ao consumidor. |

---

## Assumptions & Open Questions

| Assumption / decision | Chosen default | Rationale | Confirmed? |
| --- | --- | --- | --- |
| Como resolver o nome comercial | `client.GetUserInfo` (usync `full`/`background`), lendo o nome do contact store depois | `IsOnWhatsApp` devolve `VerifiedName` direto no retorno, mas usa usync `query`/`interactive` — o modo que o WhatsApp associa a busca manual de número. `GetUserInfo` usa `background` e persiste o nome via `updateBusinessName` (whatsmeow `user.go:250`), então o store vira a fonte de leitura. Menor exposição anti-ban, coerente com AD-002/AD-003. | y |
| `GetUserInfo` não popula `UserInfo.VerifiedName` no retorno nessa versão do whatsmeow | Ler o nome do contact store após a chamada | Verificado em `user.go:232-252`: o campo é parseado e passado a `updateBusinessName`, nunca atribuído a `info`. Se um dia passar a ser preenchido, o caminho continua válido (store é atualizado do mesmo jeito). | y |
| Teto de JIDs por chamada | 64 por request, excedente omitido e registrado em log | Um usync em lote com centenas de JIDs é justamente o padrão que dispara heurística. 64 cobre um grupo típico inteiro. O log evita o "silent cap". | n |
| Modo de resposta do resync | Síncrono, 200 com o total de contatos | Segue o precedente de `ResyncLabels` (`handlers.go:8073`), que também faz `FetchAppState` síncrono dentro do handler. | n |
| Falha do usync | Degrada silencioso: 200 com os nomes que já existiam | AD-002 reserva o fail-closed a verificações de **proteção**; resolução de nome é melhor esforço, como `IsOnWhatsApp`/`SendChatPresence`. | n |
| Log do teto de 64 (P2 AC-6) | Registrar em log, sem gate automatizado | O repo não tem harness de captura de `zerolog`; montar um writer de teste só para isso custa mais do que o risco que cobre. O *comportamento* do teto (quem é consultado e quem fica sem nome) é testado. | n |
| Ruído de log do full sync | Adicionar `case *events.Contact` no handler de eventos, em nível debug | Um full sync de agenda re-emite um `events.Contact` por contato; hoje todos caem no `default:` e viram `Warn "Unhandled event"`. Sem webhook novo — o evento continua não sendo publicado. **Sem gate automatizado** (o repo não tem teste de roteamento de eventos); verificação por inspeção. | n |

**Open questions:** none — all resolved or logged above.

---

## User Stories

### P1: Re-sync da agenda de contatos ⭐ MVP

**User Story**: Como operador do CRM, quero forçar o re-sync da agenda do celular
pareado para que contatos salvos (e contatos perdidos num re-pareamento) voltem a
resolver nome nos grupos e nas mensagens.

**Why P1**: É a única fonte recuperável de nome — `full_name`/`first_name` da
agenda. Um único chamado repovoa todos os contatos de uma vez.

**Acceptance Criteria**:

1. WHEN `POST /user/contacts/resync` é chamado com sessão conectada THEN o sistema
   SHALL executar `FetchAppState` com patch `critical_unblock_low`, `fullSync=true`
   e `onlyIfNotSynced=false`.
2. WHEN o re-sync termina sem erro THEN o sistema SHALL responder `200` com
   `{"success": true, "contacts": N}`, onde `N` é a quantidade de contatos no
   store **depois** do sync.
3. WHEN não há cliente whatsmeow para o token THEN o sistema SHALL responder `500`
   com `no session`.
4. WHEN o cliente existe mas não está conectado THEN o sistema SHALL responder
   `500` com `not connected` e NÃO SHALL chamar `FetchAppState`.
5. WHEN `FetchAppState` retorna erro THEN o sistema SHALL responder `500` com a
   mensagem do erro e NÃO SHALL contar contatos.
6. WHEN a contagem de contatos falha depois de um sync bem-sucedido THEN o sistema
   SHALL responder `500` com o erro (o sync já ocorreu; o consumidor pode repetir).

**Independent Test**: chamar o endpoint com um token válido sem sessão devolve
`500 no session` (prova rota + auth); a função de re-sync é exercitada com um
fetcher fake que registra patch/flags recebidos.

---

### P2: Fallback opt-in de nome comercial em grupos

**User Story**: Como operador do CRM, quero que participantes sem nome nenhum
tenham o nome da conta comercial resolvido quando eu pedir explicitamente, para
identificar empresas em grupos sem depender de elas terem me mandado mensagem.

**Why P2**: Cobre só contas business (pessoas físicas continuam sem nome), e custa
uma consulta de rede — por isso é opt-in, não default.

**Acceptance Criteria**:

1. WHEN `GET /group/info` ou `GET /group/list` recebe `resolveBusiness=true` E há
   participantes com `ContactName` vazio e `PhoneNumber` resolvido THEN o sistema
   SHALL fazer **uma** consulta em lote com esses números e preencher
   `ContactName` de cada participante cujo nome comercial foi encontrado.
2. WHEN o parâmetro `resolveBusiness` está ausente ou tem qualquer valor diferente
   de `true` THEN o sistema SHALL NÃO fazer nenhuma consulta de rede e os nomes da
   resposta SHALL ser os mesmos de antes desta feature. Única mudança de forma
   aceita: `GET /group/list` sem nenhum grupo passa a devolver `"Groups": []` em
   vez de `"Groups": null` (mesma direção de `formatBlocklist`, que existe no repo
   justamente para nunca devolver `null`).
3. WHEN um participante já tem `ContactName` THEN ele SHALL NÃO entrar no lote.
4. WHEN um participante está sem nome mas com `PhoneNumber` vazio THEN ele SHALL
   NÃO entrar no lote (usync não aceita LID como identificador de contato).
5. WHEN o mesmo participante aparece em vários grupos em `/group/list` THEN ele
   SHALL entrar no lote uma única vez, e o nome resolvido SHALL ser aplicado a
   todas as suas ocorrências.
6. WHEN há mais de 64 participantes sem nome THEN o sistema SHALL consultar os 64
   primeiros e registrar em log quantos ficaram de fora.
7. WHEN a consulta em lote falha THEN o sistema SHALL responder `200` com os nomes
   que já existiam, sem propagar o erro.
8. WHEN a consulta não devolve nome para um participante THEN o `ContactName` dele
   SHALL permanecer vazio (nenhum placeholder é inventado).

**Independent Test**: `resolveMissingBusinessNames` é chamada com um resolver fake
que simula a persistência do nome comercial no contact store fake; os
participantes sem nome saem com `ContactName` preenchido e os demais intactos.

---

## Edge Cases

- WHEN não há nenhum participante sem nome THEN o sistema SHALL NÃO chamar o
  resolver (zero consultas de rede mesmo com `resolveBusiness=true`).
- WHEN a lista de grupos está vazia THEN o resolve SHALL ser um no-op.
- WHEN o contact store não está disponível (`Store.Contacts == nil`) THEN o
  resolve SHALL ser um no-op e não SHALL entrar em pânico.
- WHEN dois participantes distintos compartilham o mesmo `PhoneNumber` e **nenhum**
  deles tem nome THEN ambos SHALL receber o nome resolvido.
- WHEN dois participantes compartilham o mesmo `PhoneNumber` mas **um deles já tem
  `ContactName`** (resolvido pelo LID, por exemplo) THEN o nome existente SHALL ser
  preservado e só o participante sem nome SHALL receber o nome comercial.
- WHEN o resolver falha THEN o sistema SHALL NÃO reler o contact store (nada foi
  resolvido para ler).
- WHEN `/group/list` não tem nenhum grupo THEN `Groups` SHALL ser `[]`, nunca `null`.

---

## Requirement Traceability

| Requirement ID | Story | Phase | Status |
| --- | --- | --- | --- |
| CNR-01 | P1: resync da agenda (AC 1-2) | Tasks | Pending |
| CNR-02 | P1: erros e guardas do endpoint (AC 3-6) | Tasks | Pending |
| CNR-03 | P2: resolve em lote opt-in (AC 1-5) | Tasks | Pending |
| CNR-04 | P2: teto, falha e ausência de nome (AC 6-8) | Tasks | Pending |
| CNR-05 | Edge cases (no-op, store nil, phone repetido) | Tasks | Pending |

**Coverage:** 5 total, 5 mapeados para tasks, 0 sem mapeamento.

---

## Success Criteria

- [ ] Um `POST /user/contacts/resync` repovoa `whatsmeow_contacts` com a agenda e
      devolve quantos contatos ficaram no store.
- [ ] `GET /group/info?resolveBusiness=true` preenche `ContactName` de
      participantes business que antes voltavam vazios.
- [ ] Sem `resolveBusiness`, os endpoints de grupo continuam sem tráfego extra.
- [ ] `go vet && go build && go test ./... -race` verde.
