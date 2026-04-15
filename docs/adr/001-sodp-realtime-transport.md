# ADR-001: Real-time UI transport via SODP

**Status:** accepted
**Date:** 2026-04-13

## Context

The original UI used a hand-rolled WebSocket event hub: the API
published events (`run.started`, `node.completed`, log lines, etc.)
to every connected client, and the frontend maintained its own
in-memory reducer to rebuild pipeline/run state from the event
stream. Every new UI feature needed a new event type, a new client
handler, and a new set of edge cases for reconnect/replay/deduplication.

The bugs this produced — counter clobbering, duplicate increments on
reconnect, stale state after multi-tab use, "I see the run as running
but it finished 30 seconds ago" — were recurring and hard to reason
about because the client's understanding of truth depended on having
seen every event in order.

## Decision

Replace the hand-rolled event hub with
[SODP](https://github.com/orkestri/SODP) (State-Oriented Data
Protocol). The server exposes a versioned key/value state store with
per-key subscriptions; clients watch keys of interest and receive
deltas whenever the state changes, plus a full snapshot on first
subscribe. The wire format is msgpack over WebSocket, with
RESUME-on-reconnect for gap-free replay.

The OSS core includes `pkg/sodp/` as an embedded in-process
implementation — no external broker, no extra daemon to run. The
bridge layer (`pkg/sodp/bridge.go`) turns engine run events into
state mutations on per-org keys like `dashboard.{orgID}`,
`runs.{runID}`, `runs.{runID}.logs`.

## Consequences

### Positive

- Client code is straightforward: `client.watch(key, callback)` and
  the server sends deltas. No manual event correlation.
- Reconnect has gap-free replay via the versioned state log.
- Multi-tab / multi-user correctness comes for free — every watcher
  sees the same state from the same key.
- The state store is introspectable: a tripwire on `dashboard.{org}`
  is how every page gets live updates without writing custom event
  routing.
- Cross-pod realtime is just "every API pod subscribes to the same
  EventBus, every pod pushes deltas to its own connected clients" —
  see ADR-EE-001 in the EE repo.

### Negative

- Msgpack binary framing means debugging is harder than JSON. A
  dedicated CLI debugger (`pkg/sodp/cmd/sodpcat`) mitigates this.
- The state model is prescriptive: new features must be expressed as
  "this data is state X under key Y" rather than "fire an event E".
  Some workflows (especially imperative notifications) don't map
  cleanly.
- We depend on a third-party protocol spec. Upstream breakage is a
  real risk — documented in `pkg/sodp/WORKAROUNDS.md` and
  `pkg/sodp/FEEDBACK_*.md`.

### Deferred

- The worker subprocess still uses a raw WebSocket to receive job
  dispatches — it does not use SODP. See ADR-006.
- SODP does not currently model "fire and forget" notifications
  (toasts, desktop pushes). We synthesize these client-side from
  delta diffs, which works but is awkward.

## Alternatives considered

- **Keep the hand-rolled event hub** and fix the specific bugs —
  rejected because the bug class keeps returning.
- **Server-Sent Events instead of WebSocket** — rejected because we
  also need client → server writes for interactive flows (filters,
  live preview) which SSE can't do without a second channel.
- **Firebase / Supabase Realtime / Dragonfly** — rejected because
  they require external dependencies that break the single-binary
  story (see ADR-011).
- **Build our own protocol** — considered but SODP already existed
  and matched the requirements, which is a better use of our time
  than inventing a wire format.

## Follow-ups

- [ADR-006](./006-worker-websocket-protocol.md) — migrate worker
  dispatch onto SODP (tentatively deferred to v0.9).
- Upstream feedback on SODP client protocol gotchas tracked in
  `pkg/sodp/FEEDBACK_*.md` and `pkg/sodp/WORKAROUNDS.md`.
