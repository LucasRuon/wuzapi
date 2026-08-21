# LESSONS — auto-maintained by scripts/lessons.py

> Machine-owned. Do NOT hand-edit. Changes are overwritten on the next `lessons.py` write.
> Canonical state lives in `.specs/lessons.json`. Edit lessons only via the script.
> promote_threshold=2 distinct features · window_days=45 · quarantine_threshold=2

## Confirmed (load these at Specify/Design)

Corroborated across multiple features. Safe to apply as guidance.

_none_

## Candidates (under observation — do NOT load as guidance yet)

Seen once or not yet corroborated. Tracked, not trusted.

### L-001 — When a best-effort path degrades on error, assert the side effects it skips (call counts), not only the unchanged output values.
- signal: `surviving_mutant` · recurrence: 1 feature(s) · scope: `handlers` · harmful: 0
- features: contact-name-resolution
- evidence: M7 handlers.go:4646-4649 (handlers)
- last seen: 2026-08-21T13:11:16Z

### L-002 — Every defensive guard that protects existing data from being overwritten needs a test where the overwrite would actually happen.
- signal: `surviving_mutant` · recurrence: 1 feature(s) · scope: `handlers` · harmful: 0
- features: contact-name-resolution
- evidence: M10 handlers.go:4669-4671 (handlers)
- last seen: 2026-08-21T13:11:16Z

### L-003 — When two entities share a lookup key, spell out in the spec what happens when one of them already has the value being resolved.
- signal: `spec_precision_gap` · recurrence: 1 feature(s) · scope: `spec` · harmful: 0
- features: contact-name-resolution
- evidence: spec.md:127 Edge Cases (spec)
- last seen: 2026-08-21T13:11:16Z

### L-004 — Refactoring a slice built by append into make([]T, 0, n) changes JSON null to [], so assert the empty-collection response shape before touching a response builder.
- signal: `ac_gap` · recurrence: 1 feature(s) · scope: `handlers` · harmful: 0
- features: contact-name-resolution
- evidence: P2 AC-2 / handlers.go:4602 (handlers)
- last seen: 2026-08-21T13:11:16Z

## Quarantined (failed when applied — ignore)

A confirmed lesson that recurred alongside failure. Kept for the maintainer to review.

_none_
