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

## Start here

This directory is both a decision log and an onboarding map. If you're
picking up an issue, find the ADR for the area it touches and read it
*before* writing code — it'll usually save you from re-deriving a
tradeoff we already thought through, and it'll tell you what's still
genuinely open versus already settled.

## How ADRs relate to issues

Substantial issues link the ADR that motivates them near the top, and
the ADR's Follow-ups section links back to the issue(s) that implement
it — see `#38`/`#39` and ADR-012/ADR-013 for the current example of this
pattern. Keep doing this for new substantial issues: it means a reader
never has to choose between "read the design" and "read the task."

## Proposed vs. accepted

**Proposed** means genuinely open — we picked a direction to unblock
writing code, not because every call in it is final. If you disagree
with something a `proposed` ADR says, that's exactly the moment to raise
it: before code gets written against it, not after.

**Accepted** means settled. Changing it needs a PR against the ADR
itself, or a new ADR that supersedes it — "the ADR is stale" is a review
blocker, not a nitpick, so a PR that changes how we reach or maintain a
decision must update the ADR that covers it.

## How to propose a change or disagree

- **Concrete disagreement** ("I think the decision itself is wrong, and
  here's what I'd do instead") — open a PR editing the ADR. Same review
  norm as code: tag a maintainer.
- **Still exploratory** ("I'm not sure this is right, but I don't have a
  full alternative yet") — a comment on the ADR's companion issue (or a
  new issue, if there isn't one) is the lower-friction way to start that
  conversation before it's PR-shaped.

## Keeping an ADR current after implementation

There's no separate "acceptance ceremony" beyond the PR/issue process
above. In practice, an ADR earns its `Accepted` status by staying
current as it's built: fill in a Deferred item once it's no longer
deferred, or append a dated `## Update (YYYY-MM-DD)` section when
something in Consequences turns out different in practice than
predicted. Do this in the same PR as the code change where possible.

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
| [ADR-009](./009-crash-recovery-baseline.md) | Crash-recovery baseline before durable orchestration | Accepted |
| [ADR-010](./010-run-artifact-and-resume-semantics.md) | Run definition snapshots and durable artifact/resume semantics | Accepted |
| [ADR-011](./011-postgres-scheduler-leader-election.md) | Postgres scheduler leader election via leased row + fencing generation | Accepted |
| [ADR-012](./012-artifact-and-dataset-plane.md) | Artifact and dataset plane — start local-disk, reference-based, opt-in | Accepted |
| [ADR-013](./013-connector-protocol-partition-progress-cancellation.md) | Extend the plugin protocol with work-unit planning, structured progress, and cancellation | Proposed |
| [ADR-014](./014-pipeline-ir-ownership-and-compatibility.md) | Core-owned pipeline IR and explicit compatibility negotiation | Proposed |
| [ADR-015](./015-logical-and-physical-execution-plans.md) | Persist logical and physical execution plans separately | Accepted |
| [ADR-016](./016-plugin-packaging-and-distribution.md) | Plugin packaging, runtime resolution, and distribution | Accepted |
| [ADR-017](./017-worker-protocol-v2-instance-dispatch.md) | Worker protocol v2 — instance-level dispatch, lease, and fencing | Accepted |
| [ADR-018](./018-chunked-execution-and-backpressure.md) | Chunked node execution and memory-aware backpressure | Proposed |
| [ADR-019](./019-execution-segments-and-streaming.md) | Execution segments, streaming capabilities, and reference-passing dataflow | Accepted |
| [ADR-020](./020-ecosystem-portfolio-phase-1.md) | Adopt the ecosystem framing — Phase 1 portfolio and OSS/Enterprise boundary | Proposed |
| [ADR-021](./021-cli-browser-login.md) | Session and token model — revocable sessions, refresh tokens, and browser-based CLI login | Proposed |
| [ADR-022](./022-tenant-isolation-and-outbound-request-safety.md) | Enforce tenant isolation and outbound-request safety at the data/network layer, not per-handler | Proposed |
| [ADR-023](./023-reference-kinds-and-the-data-plane.md) | Reference kinds, so the engine can stay out of the data path | Proposed |

## How to add a new ADR

1. Copy `_template.md` to `NNN-short-title.md` with the next number
2. Fill in the sections
3. Add the entry to the index above
4. Open a PR with the ADR and any code it covers

A PR that changes how we reach or maintain a decision covered by an
existing ADR must either update that ADR or supersede it with a new
one. "The ADR is stale" is a review blocker.
