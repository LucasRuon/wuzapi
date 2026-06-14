---
date: 2026-06-12T16:43:48-03:00
researcher: Codex
git_commit: 48d053310377d4736911ccca9e306941aadb41da
branch: feat/wa-labels
repository: wuzapi-spike
topic: "Verificar se o fork WuzAPI atual expoe /chat/label/apply e se a URL publica esta usando uma imagem com a rota"
tags: [research, codebase, wuzapi, labels]
status: complete
last_updated: 2026-06-12
last_updated_by: Codex
---

# Research: WuzAPI /chat/label/apply

**Date**: 2026-06-12T16:43:48-03:00
**Researcher**: Codex
**Git Commit**: 48d053310377d4736911ccca9e306941aadb41da
**Branch**: feat/wa-labels
**Repository**: wuzapi-spike

## Research Question

Verificar se o endpoint batch `/chat/label/apply` existe no fork WuzAPI local e se a instancia publica `https://wuzapi.opinaja.com.br/` aparenta estar rodando uma imagem que contem essa rota.

## Scope

Included:
- Local route registration and handler implementation for `/chat/label/apply`.
- Git commit and branch metadata for the local fork.
- Docker/GHCR image manifest availability for `ghcr.io/lucasruon/wuzapi:latest`.
- Public HTTP behavior of `https://wuzapi.opinaja.com.br/health` and unauthenticated `POST /chat/label/apply`.

Excluded:
- Authenticated label mutation execution, because no production token was provided and applying labels would be state-changing.
- Direct container inspection on the server, because no SSH/Docker access to the server was available.

## Summary

The local fork at commit `48d0533` contains the batch label endpoint. The public URL `https://wuzapi.opinaja.com.br/` is reachable and `POST /chat/label/apply` returns `401 unauthorized`, while a made-up route under the same prefix returns `404 page not found`. That behavior indicates `/chat/label/apply` is registered in the running API and protected by WuzAPI auth middleware, not missing from the deployed image.

The GHCR tag `ghcr.io/lucasruon/wuzapi:latest` exists and publishes a multi-architecture OCI image index for `linux/amd64` and `linux/arm64`.

## Detailed Findings

### Local Endpoint Registration

- `routes.go:124` registers `POST /chat/label/edit`.
- `routes.go:125` registers `POST /chat/label/chat`.
- `routes.go:126` registers `POST /chat/label/apply` and wires it to `s.ApplyLabels()`.
- `routes.go:127` registers `POST /chat/label/resync`.

### Local Handler Behavior

- `handlers.go:7353` documents `ApplyLabels` as batching WhatsApp Business label mutations into a single app-state patch.
- `handlers.go:7365` defines mutation payload fields for both `"edit"` and `"chat"` mutations.
- `handlers.go:7405` creates `appstate.PatchInfo{Type: appstate.WAPatchRegular}`.
- `handlers.go:7413` appends `appstate.BuildLabelEdit(...)` mutations for `"edit"`.
- `handlers.go:7431` appends `appstate.BuildLabelChat(...)` mutations for `"chat"`.
- `handlers.go:7441` sends the whole batch with one `client.SendAppState(ctx, patch)`.
- `handlers.go:7447` returns a success payload with `"message": "Labels applied"` and `"count": len(patch.Mutations)`.

### Git State

- Current branch: `feat/wa-labels`.
- Current commit: `48d053310377d4736911ccca9e306941aadb41da`.
- `git log` shows HEAD as `48d0533 feat(label): aplica etiquetas em lote num unico app-state patch (corrige descarte da 2a)`.
- `git branch -a --contains 48d0533` shows the commit is present on `feat/wa-labels`, `fork/feat/wa-labels`, and `fork/HEAD`.
- `git merge-base --is-ancestor 48d0533 origin/main` exited `1`, so the batch endpoint commit is not in upstream `origin/main`.
- `git merge-base --is-ancestor 48d0533 fork/feat/wa-labels` exited `0`, so the batch endpoint commit is in the fork feature branch.

### Image And Deployment Clues

- `.github/workflows/docker-publish.yml:41` publishes images to `ghcr.io/lucasruon/wuzapi`.
- `.github/workflows/docker-publish.yml:43` publishes the raw `latest` tag.
- `.github/workflows/docker-publish.yml:44` also publishes short SHA tags.
- `docker manifest inspect ghcr.io/lucasruon/wuzapi:latest` succeeded and returned an OCI image index with `linux/amd64` and `linux/arm64` manifests.
- The local `docker-compose.yml:3` builds from the local Dockerfile, so a local compose build from this checkout would include the route.
- The local `docker-compose-swarm.yaml:5` still references `asternic/wuzapi:latest`; this file does not represent the user's stated current server image.

### Public Runtime Checks

Commands run against `https://wuzapi.opinaja.com.br/`:

- `GET /health` returned HTTP `200` with JSON including `"status":"ok"`, `"version":"1.0.6"`, and uptime.
- Unauthenticated `POST /chat/label/apply` returned HTTP `401` with `{"code":401,"error":"unauthorized","success":false}`.
- Unauthenticated `POST /chat/label/edit` also returned HTTP `401`.
- Unauthenticated `POST /chat/label/not-real` returned HTTP `404` with `404 page not found`.

The status contrast matters: a registered protected route reaches the auth middleware and returns `401`; a route that does not exist returns `404`.

### Verification

- `GOCACHE=/private/tmp/wuzapi-go-cache go test ./... -run TestDoesNotExist` passed, compiling the package without running tests.

## Code References

- `routes.go:126` - Public route registration for `POST /chat/label/apply`.
- `handlers.go:7353` - `ApplyLabels` handler start and batch-label rationale.
- `handlers.go:7405` - Single `WAPatchRegular` patch construction.
- `handlers.go:7413` - Label edit mutation appended to the batch patch.
- `handlers.go:7431` - Label chat association mutation appended to the batch patch.
- `handlers.go:7441` - Single `SendAppState` call for the full batch.
- `.github/workflows/docker-publish.yml:41` - GHCR image repository.
- `.github/workflows/docker-publish.yml:43` - `latest` image tag publication.
- `docker-compose-swarm.yaml:5` - Older/sample swarm image reference to `asternic/wuzapi:latest`.

## Architecture Documentation

The route is authenticated through the same `authalice` middleware chain used by other chat endpoints. A request without a valid `token` header or query parameter returns `401` before reaching the handler. This is why unauthenticated production probing can confirm route registration but cannot confirm the handler's successful mutation path.

## Historical Context

Commit `48d0533` introduced the batch route and changed only `handlers.go` and `routes.go`. The commit message states that the CRM previously issued separate `/chat/label/edit` and `/chat/label/chat` calls, and that batching avoids the second-label discard caused by sequential `SendAppState` app-state version conflicts.

## Open Questions

- Whether the production container digest currently running on the server exactly matches the latest GHCR digest was not directly verified, because server-side Docker/SSH access was not available.
- Authenticated execution of `/chat/label/apply` was not performed to avoid state changes without a production token and explicit test payload.
