# Feature: Normalização do nono dígito (números BR)

## Problema

Números de celular brasileiros podem estar registrados no WhatsApp **com** ou **sem**
o nono dígito (o `9` extra). Hoje a API envia para o número exatamente como recebido,
então `5541999198525` (com 9) falha quando a conta está registrada como `554199198525`
(sem 9), obrigando o cliente a corrigir manualmente.

Não é possível saber deterministicamente, apenas pelo número, qual variante está
registrada — **somente o WhatsApp sabe** (via `IsOnWhatsApp`).

## Requisitos

- **R1** — Ao enviar mensagem para um celular BR (`55` + DDD + número), a API deve
  resolver automaticamente a variante correta (com ou sem o nono dígito) e enviar para
  o JID canônico confirmado pelo WhatsApp.
- **R2** — A resolução usa `client.IsOnWhatsApp` com as duas variantes; usa a que o
  WhatsApp confirma como registrada (`IsIn = true`). Quando ambas existem, prefere a
  enviada pelo cliente.
- **R3** — O resultado (número original → JID canônico) é cacheado para evitar uma
  consulta de rede a cada envio.
- **R4 (fallback)** — Se a checagem falhar (rede/erro) ou nenhuma variante estiver
  registrada, envia para o número **original** como veio (nunca piora o comportamento
  atual).
- **R5 (escopo)** — Aplica-se apenas ao envio de mensagens (ponto único
  `validateMessageFields`, que cobre todos os `/chat/send/*`). Grupos (`@g.us`),
  newsletters e JIDs não-`s.whatsapp.net` não são afetados.
- **R6** — Números não brasileiros e fixos não são alterados.

## Decisões (discuss)

| Gray area  | Decisão                                                         |
| ---------- | -------------------------------------------------------------- |
| Estratégia | Híbrida: variantes + `IsOnWhatsApp` + cache                    |
| Fallback   | Enviar como veio (preserva comportamento atual)                |
| Escopo     | Apenas envio (`validateMessageFields`)                         |

## Verificação

- Build (`go build ./...`) e `go vet` limpos.
- Teste unitário da geração de variantes (`brazilianMobileVariants`), incluindo:
  com 9 → gera sem; sem 9 (celular) → gera com; fixo → nil; não-BR → nil.
- Manual: enviar para `5541999198525` entrega na conta `554199198525` e vice-versa.
