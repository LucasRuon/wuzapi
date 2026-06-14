---
date: 2026-06-12T17:25:51-03:00
researcher: Codex
git_commit: 9e2b6b8b354db5d7105f85c646651997694baeef
branch: feat/wa-labels
repository: wuzapi-spike
topic: "tem algum endpoint na biblioteca GO que não temos ainda na Wuzapi e que seja importante ter?"
tags: [research, codebase, whatsmeow, endpoints]
status: complete
last_updated: 2026-06-12
last_updated_by: Codex
---

# Research: Whatsmeow Endpoint Gap Analysis

**Date**: 2026-06-12T17:25:51-03:00
**Researcher**: Codex
**Git Commit**: 9e2b6b8b354db5d7105f85c646651997694baeef
**Branch**: feat/wa-labels
**Repository**: wuzapi-spike

## Research Question

tem algum endpoint na biblioteca GO que não temos ainda na Wuzapi e que seja importante ter?

## Scope

Included: current WuzAPI HTTP routes, current handler usage, and public `*whatsmeow.Client` capabilities from the pinned local dependency `go.mau.fi/whatsmeow v0.0.0-20260516102357-8d3700152a69`.

Excluded: upstream changes newer than the pinned module, low-level internals, push notification device registration, and raw protocol helpers that are not good public HTTP API candidates.

Assumption: "importante" means useful for CRM and operational automation, especially contact enrichment, presence monitoring, labels, channels/newsletters, and groups.

## Summary

WuzAPI already exposes the main flows for session management, webhook config, sending messages/media, labels, contact checks, user info, avatar, privacy, group management, calls, chat history, and basic newsletter listing. The most useful missing `whatsmeow` capabilities are:

- Presence subscription for a specific contact.
- WhatsApp Business profile lookup.
- Contact QR and business short-link resolution.
- More complete newsletter/channel operations.
- Community/subgroup management endpoints.
- Status privacy and default disappearing timer endpoints.

## Detailed Findings

### Current WuzAPI Surface

- Routes are registered centrally in `routes.go`, including session, webhook, chat send, labels, status text, calls, user, privacy, chat, group, and newsletter routes (`routes.go:81` through `routes.go:173`).
- Label endpoints now include edit, chat association, batch apply, list, and resync (`routes.go:124` through `routes.go:128`).
- User endpoints include presence set, user info, check, avatar, contacts, block/unblock, blocklist, LID, and privacy settings (`routes.go:134` through `routes.go:144`).
- Group endpoints cover creation, listing, info, invite link, photo, leave, name/topic, announce/locked, disappearing timer, join/invite info, participant updates, join requests, and join approval mode (`routes.go:154` through `routes.go:171`).
- Newsletter support currently exposes only subscribed newsletter listing (`routes.go:173`, `handlers.go:5187` through `handlers.go:5226`).

### Presence Subscription

- WuzAPI has `SendPresence` for global presence and `ChatPresence` for typing/recording state (`routes.go:134`, `routes.go:146`).
- The pinned `whatsmeow` dependency also exposes `SubscribePresence`, which asks WhatsApp to send presence updates for a specific user (`presence.go:89` through `presence.go:125`).
- Webhook support already includes `Presence` and `ChatPresence` event types (`constants.go:55` through `constants.go:57`), and the event handler emits `Presence` events (`wmiau.go:1254`).
- There is no route or handler referencing `SubscribePresence`.

This is high value for CRM if the product wants reliable online/offline presence monitoring for selected contacts.

### Business Profile Lookup

- WuzAPI exposes `GetUserInfo` through `/user/info`, backed by `client.GetUserInfo` (`handlers.go:3316` through `handlers.go:3379`).
- WuzAPI exposes profile photo lookup through `/user/avatar`, backed by `client.GetProfilePictureInfo` (`handlers.go:3438` through `handlers.go:3498`).
- The pinned `whatsmeow` dependency also exposes `GetBusinessProfile`, returning WhatsApp Business profile fields such as email, address, categories, business hours, and profile options (`user.go:412` through `user.go:439`).
- There is no route or handler referencing `GetBusinessProfile`.

This is high value for lead enrichment and CRM data quality.

### Contact QR And Business Link Resolution

- The pinned `whatsmeow` dependency exposes:
  - `ResolveBusinessMessageLink` for `wa.me/message/<code>` or `api.whatsapp.com/message/<code>` (`user.go:35` through `user.go:81`).
  - `ResolveContactQRLink` for `wa.me/qr/<code>` or `api.whatsapp.com/qr/<code>` (`user.go:83` through `user.go:116`).
  - `GetContactQRLink` to generate or revoke the current account's contact QR link (`user.go:118` through `user.go:140`).
- There are no WuzAPI routes or handlers for these operations.

This is medium-to-high value for lead capture, QR-code contact flows, and resolving WhatsApp short links into JIDs before starting CRM workflows.

### Newsletter / Channels

- WuzAPI currently exposes only `/newsletter/list`, which calls `GetSubscribedNewsletters` (`routes.go:173`, `handlers.go:5187` through `handlers.go:5226`).
- The pinned `whatsmeow` dependency exposes additional newsletter operations:
  - Subscribe to live updates (`newsletter.go:27` through `newsletter.go:43`).
  - Mark channel messages as viewed (`newsletter.go:45` through `newsletter.go:82`).
  - Send or remove reactions to channel messages (`newsletter.go:84` through `newsletter.go:112`).
  - Get newsletter info by JID or invite (`newsletter.go:266` through `newsletter.go:284`).
  - Create newsletter, accept TOS notice, mute/unmute, follow/unfollow (`newsletter.go:313` through `newsletter.go:376`).
  - Fetch newsletter messages and message updates (`newsletter.go:378` through `newsletter.go:425`).
- Webhook support already includes newsletter events (`constants.go:65` through `constants.go:69`), and the event handler emits join/leave/mute/live update events (`wmiau.go:1657` through `wmiau.go:1672`).

This is important if WhatsApp Channels are part of the product. For a pure CRM label/message use case, it is secondary.

### Communities And Group Gaps

- WuzAPI already exposes most group operations (`routes.go:154` through `routes.go:171`).
- The pinned `whatsmeow` dependency includes additional community/subgroup operations:
  - `LinkGroup` and `UnlinkGroup` for community child groups (`group.go:136` through `group.go:165`).
  - `GetSubGroups` and `GetLinkedGroupsParticipants` (`group.go:547` through `group.go:588`).
  - `SetGroupMemberAddMode` (`group.go:1038` through `group.go:1051`).
  - `SetGroupDescription` (`group.go:1053` through `group.go:1065`).
- There are no WuzAPI routes for these exact operations.

This is important only if the product manages WhatsApp communities or large group structures.

### Status Privacy And Default Disappearing Timer

- WuzAPI exposes text status update via `/status/set/text` (`routes.go:130`).
- WuzAPI exposes generic account privacy settings via `/user/privacy` (`routes.go:143` through `routes.go:144`, `handlers.go:6997` through `handlers.go:7055`).
- The pinned `whatsmeow` dependency exposes `GetStatusPrivacy`, which specifically reads who can receive status broadcasts (`broadcast.go:90` through `broadcast.go:115`).
- The pinned `whatsmeow` dependency exposes `SetDefaultDisappearingTimer`, which changes the account-level default disappearing message timer (`privacysettings.go:107` through `privacysettings.go:120`).
- There are no WuzAPI routes for these exact operations.

These are useful for operational completeness, but lower priority than presence subscription and business profile lookup for CRM.

## Code References

- `go.mod:13` - Pinned `go.mau.fi/whatsmeow` dependency used for comparison.
- `routes.go:81` - Start of authenticated API route registration.
- `routes.go:124` - Label endpoint group.
- `routes.go:134` - User endpoint group.
- `routes.go:154` - Group endpoint group.
- `routes.go:173` - Current newsletter list endpoint.
- `handlers.go:3316` - `/user/info` uses `GetUserInfo`.
- `handlers.go:3438` - `/user/avatar` uses `GetProfilePictureInfo`.
- `handlers.go:5187` - `/newsletter/list` uses `GetSubscribedNewsletters`.
- `handlers.go:6997` - `/user/privacy` uses `TryFetchPrivacySettings` and `SetPrivacySetting`.
- `constants.go:55` - Presence event types are supported.
- `constants.go:65` - Newsletter event types are supported.
- `/Users/lucasruon/go/pkg/mod/go.mau.fi/whatsmeow@v0.0.0-20260516102357-8d3700152a69/presence.go:89` - `SubscribePresence`.
- `/Users/lucasruon/go/pkg/mod/go.mau.fi/whatsmeow@v0.0.0-20260516102357-8d3700152a69/user.go:35` - business/contact QR link helpers.
- `/Users/lucasruon/go/pkg/mod/go.mau.fi/whatsmeow@v0.0.0-20260516102357-8d3700152a69/user.go:412` - `GetBusinessProfile`.
- `/Users/lucasruon/go/pkg/mod/go.mau.fi/whatsmeow@v0.0.0-20260516102357-8d3700152a69/newsletter.go:266` - newsletter info and actions.
- `/Users/lucasruon/go/pkg/mod/go.mau.fi/whatsmeow@v0.0.0-20260516102357-8d3700152a69/group.go:136` - community group link/unlink.
- `/Users/lucasruon/go/pkg/mod/go.mau.fi/whatsmeow@v0.0.0-20260516102357-8d3700152a69/group.go:547` - community subgroup listing.
- `/Users/lucasruon/go/pkg/mod/go.mau.fi/whatsmeow@v0.0.0-20260516102357-8d3700152a69/broadcast.go:90` - `GetStatusPrivacy`.
- `/Users/lucasruon/go/pkg/mod/go.mau.fi/whatsmeow@v0.0.0-20260516102357-8d3700152a69/privacysettings.go:107` - `SetDefaultDisappearingTimer`.

## Architecture Documentation

WuzAPI exposes `whatsmeow` features through HTTP handlers in `handlers.go`, with routes declared in `routes.go`. Authenticated routes use the `authalice` middleware and retrieve the current user/session through `r.Context().Value("userinfo")`, then call `clientManager.GetWhatsmeowClient(txtid)` before invoking `whatsmeow` client methods. Event support is declared in `constants.go` and emitted from the central whatsmeow event handler in `wmiau.go`.

## Historical Context

An existing research note documents label/batch endpoint production validation at `docs/research/2026-06-12-chat-label-apply-wuzapi-production.md`. That note is related to label operations, but this research focuses on broader endpoint gaps.

## Open Questions

- Whether newsletter/channel operations are in current product scope.
- Whether presence monitoring is desired for all contacts or only CRM-selected contacts.
- Whether business profile lookup should be a standalone endpoint or an optional enrichment flag on `/user/info`.
