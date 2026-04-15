# Architecture Decision Records

This directory holds the record of non-trivial technical decisions in
the Brokoli OSS core. Each ADR captures a single decision, why we made
it, what alternatives we rejected, and what it punts to future work.
New decisions get a new file with the next sequential number;
superseded decisions are marked in-place with a link to the ADR that
replaced them rather than being rewritten, so the history stays
auditable.

**Format (per file):**

- **Status** — proposed / accepted / superseded / deprecated
- **Context** — what problem we were looking at
- **Decision** — what we chose
- **Consequences** — positive and negative, both
- **Alternatives considered** — what we ruled out and why
- **Follow-ups** — what this decision defers to future work

## Index

| # | Title | Status |
|---|---|---|
| [ADR-001](./001-sodp-realtime-transport.md) | Real-time UI transport via SODP | Accepted |
| [ADR-002](./002-plugin-protocol.md) | Plugin protocol — Airbyte-style JSONL over subprocess | Accepted |
| [ADR-003](./003-plugin-hybrid-model.md) | Plugin hybrid model — Go first-class + subprocess long-tail | Accepted |
| [ADR-004](./004-plugin-runtime-in-oss.md) | Plugin runtime lives in OSS, not EE | Accepted |
| [ADR-005](./005-plugin-distribution-shared-volume.md) | Plugin distribution via shared volume | Accepted |
| [ADR-006](./006-worker-websocket-protocol.md) | Worker WebSocket protocol — raw, not SODP (deferred migration) | Accepted |
| [ADR-007](./007-install-ux-one-liner.md) | Install UX — one-liner with interactive admin setup | Accepted |
| [ADR-008](./008-documentation-framework.md) | Documentation framework — fumadocs over MkDocs | Accepted |

## How to add a new ADR

1. Copy `_template.md` to `NNN-short-title.md` with the next number
2. Fill in the sections
3. Add the entry to the index above
4. Open a PR with the ADR and any code it covers

A PR that changes how we reach or maintain a decision covered by an
existing ADR must either update that ADR or supersede it with a new
one. "The ADR is stale" is a review blocker.
