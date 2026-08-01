# ADR-009: Crash-recovery baseline before durable orchestration

**Status:** accepted
**Date:** 2026-08-01

## Context

Brokoli persists mutable run and node-run snapshots, but scheduling, DAG
outputs, retries, and active-run ownership are process-local. We need
reproducible evidence of what a process death leaves behind before adding
event sourcing, recovery, or HA.

## Decision

Maintain a SQLite crash harness that terminates a child test process at named
execution boundaries in a source -> condition -> sink pipeline. The harness
asserts the persisted run/node state and whether the sink side effect occurred.

The current baseline is intentionally not recovery-safe:

- Before run creation, no run exists.
- After run creation and before terminal persistence, a run remains running.
- An attempt remains running if the process dies before its completion update.
- A sink can write its output before the attempt completion is persisted.
- After terminal persistence, the run is success.

## Consequences

### Positive

- Later durability work has deterministic regression cases for each boundary.
- The sink-side-effect case demonstrates why retries require idempotency.
- The harness uses opt-in failpoints that are inert outside go test.

### Negative

- This only establishes SQLite behavior; Postgres coverage is a follow-up.
- It does not recover, repair, or resume an interrupted run.

### Deferred

- Append-only run events and rebuildable projections (#6).
- Durable attempts, dispatch outbox, and idempotency (#7).
- Artifact semantics and startup reconciliation (#8, #9).

## Alternatives considered

- **Unit-test panic injection** — rejected because deferred cleanup differs from
  process death.
- **Manual kill testing only** — rejected because it cannot be a CI regression
  suite.

## Follow-ups

- Extend the harness to Postgres before claiming equivalent durability there.
- Keep each new scheduler/worker failure mode covered by a deterministic case.
