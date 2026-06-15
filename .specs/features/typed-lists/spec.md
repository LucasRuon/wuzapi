# Feature: Listas tipadas (WhatsApp Lists / ListType)

## Problema

O WhatsApp converteu as "etiquetas" em **Listas** (chat lists). No protocolo, o
`LabelEditAction` ganhou um campo `type` (enum `ListType`) que classifica a entidade:
`NONE, UNREAD, GROUPS, FAVORITES, PREDEFINED, CUSTOM, COMMUNITY, SERVER_ASSIGNED,
DRAFTED, AI_HANDOFF, CHANNELS, AI_RESPONDING, ARCHIVED, LOCKED, INVITES, THIRD_PARTY`.

Hoje `/chat/label/edit` usa `appstate.BuildLabelEdit(id, name, color, deleted)`, que
**não seta `type`** — então toda lista criada pelo wuzapi nasce como `NONE` (tipo
ausente), divergindo de como a UI nova trata uma lista customizada (`CUSTOM`).

A **leitura** já expõe o tipo (`/chat/label/list` e o `labelType` do webhook de
`LabelEdit`, via `Action.GetType()`); o que falta é a **escrita** conseguir defini-lo.

## Requisitos

- **R1 (superfície)** — Novos endpoints `/chat/list/*` para escrita de listas tipadas.
  Os endpoints `/chat/label/*` existentes permanecem **intactos** (retrocompatível).
- **R2 (`/chat/list/edit`)** — Cria/edita/deleta uma lista com `type` definível.
  Payload: `{ id, name, color, deleted, type?, orderIndex?, isActive? }`. Quando `type`
  é omitido, assume **`CUSTOM`** (corrige o `NONE` atual).
- **R3 (tipos)** — Aceita **qualquer** valor do enum `ListType`, por nome
  (case-insensitive). Valor desconhecido → `400`. Lista de aceitos derivada de
  `waSyncAction.LabelEditAction_ListType_value` (não hard-coded), para acompanhar
  futuros bumps da whatsmeow sem editar o handler.
- **R4 (defaults de renderização)** — Quando `isActive`/`orderIndex` são omitidos, a
  lista nasce com `isActive=true` e `orderIndex>0` derivado do id numérico — os
  defaults que fazem a lista **colar no contato** (espelham `buildActiveLabelEdit`,
  fix do fork `1a0022d`/`97968b3`). Valores explícitos no payload sobrescrevem.
- **R5 (`/chat/list/apply`)** — Batch de mutações de lista num **único**
  `WAPatchRegular` (espelha `ApplyLabels`, evitando o descarte da 2ª mutation por
  conflito de versão de app-state quando se faz `SendAppState` sequencial). As
  associações de chat resolvem o **LID** do contato via `resolveLabelTargetJID`
  (fix do fork `a6e50f7`), não o phone JID.
- **R6 (builder local)** — Como a whatsmeow não expõe helper com `type`, a mutation é
  montada localmente replicando `newLabelEditMutation`:
  `appstate.MutationInfo{ Index: []string{appstate.IndexLabelEdit, id}, Version: 3,
  Value: &waSyncAction.SyncActionValue{ LabelEditAction: {Name, Color, Deleted, Type,
  OrderIndex, IsActive} } }`, enviada via `client.SendAppState`. **Sem fork** da lib.
- **R7 (leitura)** — Não duplicar leitura: `/chat/label/list` já devolve `labelType`.
  Adicionar apenas um **alias** de rota `/chat/list/list` → handler `ListLabels`
  existente, para simetria semântica com `/chat/list/*`.

## Decisões (discuss)

| Gray area        | Decisão                                                            |
| ---------------- | ----------------------------------------------------------------- |
| Objetivo         | Corrigir o tipo na criação — default `CUSTOM` em vez de `NONE`     |
| Superfície       | Novos endpoints `/chat/list/*` (escrita); `/chat/label/*` mantidos |
| Tipos graváveis  | Qualquer `ListType`; default `CUSTOM`; nome inválido → `400`       |
| Leitura          | Reusa `ListLabels`; alias de rota `/chat/list/list`               |
| whatsmeow        | Sem fork — `MutationInfo` montada localmente (`Version 3`, `IndexLabelEdit`) |
| Base             | whatsmeow bumpada p/ `v0.0.0-20260611094716` (ListType com `ARCHIVED..THIRD_PARTY`) |
| Reconciliação    | `buildActiveLabelEdit` (fork) vira caso especial de `buildTypedListEdit` (CUSTOM); `ApplyList` reusa `resolveLabelTargetJID` (LID) — uma só fonte de verdade |

## Verificação

- `go build ./...` e `go vet` limpos.
- Teste unitário do parse de `type`: nome válido (case-insensitive) → enum certo;
  ausente → `CUSTOM`; inválido → erro. Cobre os tipos novos (`ARCHIVED`, `LOCKED`,
  `INVITES`, `THIRD_PARTY`).
- Teste unitário do builder local: confirma `Index == [label_edit, id]`, `Version == 3`,
  `Type` setado, e que `orderIndex`/`isActive` ficam `nil` quando omitidos.
- Manual: criar lista via `/chat/list/edit` (sem `type`) e confirmar em
  `/chat/label/list` que `labelType == "CUSTOM"` (antes vinha `NONE`).
- Manual: `/chat/list/apply` com 2+ mutations aplica todas (nenhuma descartada).
</content>
</invoke>
