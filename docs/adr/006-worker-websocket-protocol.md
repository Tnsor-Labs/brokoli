# ADR-006: Worker WebSocket protocol — raw, not SODP (deferred migration)

**Status:** accepted
**Date:** 2026-04-14

## Context

ADR-001 replaces the UI event stream with SODP as the canonical
realtime transport for Brokoli. The expected next question is:
should the worker process — which receives job dispatches over a
WebSocket from the API — also speak SODP?

Today the worker uses a hand-rolled JSON-over-WebSocket protocol
at `/api/workers/ws` with a separate auth model (pool tokens in the
`Authorization` header, not the session JWT used by the UI). The
API-side handler lives in `ee/platform/handlers_workpool.go`
(`HandleWorkerWS`) and the client-side code is in
`ee/worker/worker.go` (`runWebSocket`).

It's functional but it's a second WebSocket stack to maintain:
different message format (JSON vs msgpack), different framing,
different reconnect semantics (the worker falls back to HTTP
long-polling on disconnect rather than SODP's RESUME frame), and
different testing surfaces.

## Decision

**Keep the raw WebSocket protocol for now. Do not migrate to SODP
in v0.8.0 or v0.9.0.** Revisit as part of a dedicated phase once
Phase 3+ work has flushed out the connector story.

The reasons this is the right call now, not forever:

1. **The mismatch in auth models is real.** SODP's `HELLO` / `AUTH`
   frames expect session JWTs. Workers authenticate with pool
   tokens, which are a different credential type with different
   scoping (per-pool, not per-user). Adding a worker-specific auth
   variant to SODP is possible but non-trivial.

2. **Dispatch semantics don't map cleanly.** SODP is a state-
   subscription protocol: clients watch keys, server pushes deltas
   when state changes. Worker dispatch is push-based RPC: server
   sends "execute this job" to *exactly one* worker in a pool.
   Modeling this in SODP means either (a) a state key that
   represents the work queue, with atomic pop-and-lock semantics
   via CALL frames — requires new primitives, or (b) an event
   fan-out that all workers receive, with client-side filtering —
   wrong because every worker sees every job.

3. **Nothing is broken.** The raw protocol works. It has a
   SIGTERM-to-clean-shutdown fix from v0.5.9 and a watchdog for the
   WebSocket read loop. The realtime UI bugs SODP fixed don't
   apply to the worker path because the worker isn't rendering a
   UI; it's executing jobs.

## Consequences

### Positive

- We don't spend engineering time on a refactor that doesn't fix
  a user-visible bug.
- The worker protocol is simple enough to debug — plain JSON over
  WebSocket, visible in any network tool.
- Job dispatch keeps its push-based semantics, which match the
  work-queue execution model.

### Negative

- **Two WebSocket stacks to maintain.** Bug classes don't always
  transfer between them (for example, the TLS handshake-timeout
  class we hit on the UI side needed a separate fix on the worker
  side). When we add a new feature that needs realtime feedback
  between worker and UI (e.g., live log streaming), we do it
  twice.
- **The worker WebSocket has weaker reconnect semantics** than SODP
  — no RESUME, just reconnect-from-scratch with HTTP long-polling
  as a fallback. For long-running job flows this means a dropped
  connection could re-deliver a job the worker was already
  executing. Mitigated by idempotency keys but not as clean as
  SODP's versioned state log.
- **Symbolic complexity.** New contributors have to learn that
  `/api/ws` and `/api/workers/ws` are unrelated protocols despite
  both being WebSocket endpoints on the same server.

### Deferred

- Full migration of the worker path to SODP, once the SODP
  primitive set grows to include atomic work-queue semantics
  (CALL with single-consumer delivery).

## Alternatives considered

- **Migrate to SODP now** — rejected. Large refactor, no user-
  visible benefit, would block Phase 2 and 3 plugin work.
- **Add SODP for logs only, keep raw WebSocket for dispatch** —
  considered. Would let the UI subscribe to live worker logs via
  SODP's existing state-watch semantics. Still non-trivial wiring
  (workers need to publish log deltas to SODP state keys that the
  UI already knows how to watch). Viable as an incremental step
  later, but not a priority.
- **Replace the raw WS with plain HTTP long-polling** —
  considered and rejected because the push latency would be
  meaningfully worse (seconds instead of milliseconds), and we'd
  be giving up instant dispatch on a fast path that matters.

## Follow-ups

- [Phase 2+] Add a `worker-protocol-v2` ADR when we have a clear
  path to unifying the two stacks.
- Document the distinction between `/api/ws` (SODP, UI) and
  `/api/workers/ws` (raw JSON, workers) prominently so new
  contributors don't confuse them.
