# Realtime architecture

Brokoli's UI receives live pipeline updates over a single WebSocket at `/api/ws`
using **SODP** (State-Oriented Data Protocol). SODP is a binary, msgpack-framed
protocol for streaming state deltas to subscribers. The Brokoli server is the
SODP authority; the Svelte UI uses the upstream `@sodp/client` TypeScript library
to subscribe.

This document covers what the realtime stack does, the keys it exposes, the wire
format, and how to integrate with it from a non-browser client.

## Why SODP

The previous implementation was an in-process WebSocket "hub" that broadcast
JSON-encoded `models.Event` to all connected clients. It worked but had three
problems:

1. Every event went to every client — no per-key subscription model, so the UI
   re-rendered the world on every keystroke from any user.
2. JSON framing was wasteful for high-frequency events (per-node logs).
3. Reconnect was a full page reload — there was no concept of "resume from
   version N" so brief network blips dropped events on the floor.

SODP fixes all three: clients subscribe to specific state keys, deltas are
binary-encoded with field-level granularity, and reconnect replays missed
deltas via the protocol's `RESUME` frame.

## Components

```
┌──────────────────┐    models.Event    ┌──────────────────┐
│  engine.Engine   │ ─────────────────► │   sodp.Bridge    │
│                  │  (channel send)    │                  │
└──────────────────┘                    └────────┬─────────┘
                                                 │ MutateAppend
                                                 │ Mutate
                                                 ▼
                                        ┌──────────────────┐
                                        │  sodp.Server     │
                                        │ ┌──────────────┐ │
                                        │ │  StateStore  │ │  versioned KV
                                        │ └──────┬───────┘ │  + delta log
                                        │        │         │
                                        │ ┌──────▼───────┐ │
                                        │ │  FanoutBus   │ │  pre-encoded
                                        │ └──────┬───────┘ │  per-key
                                        └────────┼─────────┘
                                                 │
                              ┌──────────────────┼──────────────────┐
                              │                  │                  │
                              ▼                  ▼                  ▼
                       ┌────────────┐    ┌────────────┐    ┌────────────┐
                       │ @sodp/client│   │ @sodp/client│   │ @sodp/client│
                       │  (browser) │    │  (browser) │    │  (browser) │
                       └────────────┘    └────────────┘    └────────────┘
```

| Component | File | Responsibility |
|---|---|---|
| `pkg/sodp` | `pkg/sodp/server.go`, `state.go`, `fanout.go`, `frame.go`, `session.go` | SODP protocol implementation: WebSocket handler, versioned state store, delta fanout, msgpack framing, per-session auth and rate limiting. Drop-in replacement for the old `api.Hub`. |
| `pkg/sodp/bridge.go` | `pkg/sodp/bridge.go` | Translates engine `models.Event` into state mutations on dot-separated keys (`runs.{id}`, `runs.{id}.nodes.{node}`, `runs.{id}.logs`, `_events`). |
| `api/server.go` | `startEventBusBridge()` | Distributed mode hook: subscribes to the enterprise `EventBus` (Redis pub/sub) and forwards worker-published events into the same bridge channel. |
| `ui/src/lib/ws.ts` | `ws.ts` | Thin Svelte adapter around `@sodp/client`. Subscribes to `_events`, manages a baseline counter, forwards new entries to the existing `addEvent()` store pipeline so legacy page-level handlers keep working unchanged. |

## State key model

The bridge writes events into these keys. Subscribers can `WATCH` any of them.

| Key | Shape | Mutation source | TTL |
|---|---|---|---|
| `runs.{run_id}` | `{ status, pipeline_id, org_id, started_at, finished_at, error }` | `run.started`, `run.completed`, `run.failed` | Evicted by `StateStore.EvictCompleted` 30 min after `finished_at` |
| `runs.{run_id}.nodes.{node_id}` | `{ status, started_at, finished_at, row_count, duration_ms, error }` | `node.started`, `node.completed`, `node.failed` | Cascades with parent run |
| `runs.{run_id}.logs` | `Array<{ node_id, level, message, timestamp }>` | `log` | Capped at 200 entries; cascades with parent run |
| `_events` | `Array<EventEnvelope>` | All event types | Capped at 100 entries (community mode) |
| `_events.{org_id}` | `Array<EventEnvelope>` | All event types from a specific org | Capped at 100 entries (multi-tenant) |

The `_events` stream is the key the UI watches by default. It exists because
the existing UI is event-driven (pages register `onWSEvent` callbacks); a state-
driven refactor would let pages watch specific run keys directly, which is on
the roadmap but not blocking.

### Tenant isolation

`pkg/sodp/server.go:keyAllowedForSession()` enforces that:

- Sessions with `org_id == "default"` (community mode) can watch anything.
- Other sessions can only watch `runs.*` keys whose stored `org_id` matches
  their session, and only their own `_events.{org_id}` stream.

The org comes from the JWT cookie via the `JWTAuth` middleware in `api/users.go`,
which propagates the claims into the request context before the WebSocket
upgrade reaches the SODP handler.

## Wire format

SODP frames are MessagePack 4-element arrays:

```
[ frame_type: uint8, stream_id: uint32, seq: uint64, body: any ]
```

Frame types in use:

| Type | Hex | Direction | Body |
|---|---|---|---|
| `HELLO` | `0x01` | Server → Client | `{ protocol: "sodp", version: "0.1", server_id, auth: bool }` |
| `WATCH` | `0x02` | Client → Server | `{ state: key }` |
| `STATE_INIT` | `0x03` | Server → Client | `{ state, version, value, initialized }` |
| `DELTA` | `0x04` | Server → Client | `{ key, version, ops: [{ op, path, value? }] }` |
| `CALL` | `0x05` | Client → Server | `{ call_id, method, args }` |
| `RESULT` | `0x06` | Server → Client | `{ call_id, success, data }` |
| `ERROR` | `0x07` | Server → Client | `{ code, message }` |
| `HEARTBEAT` | `0x09` | Bidirectional | (none) |
| `RESUME` | `0x0A` | Client → Server | `{ state, since_version }` |
| `AUTH` | `0x0B` | Client → Server | `{ token }` |
| `AUTH_OK` | `0x0C` | Server → Client | `{ sub }` |
| `UNWATCH` | `0x0D` | Client → Server | `{ state }` |

Delta op types are `"ADD" | "UPDATE" | "REMOVE"` with JSON Pointer paths. Array
append uses RFC 6901 `"/-"`. Root-level updates use `"/"`.

The full upstream protocol spec (which Brokoli implements compatibly) is at
[github.com/orkestri/SODP/blob/main/docs/protocol.md](https://github.com/orkestri/SODP/blob/main/docs/protocol.md).

## Authentication

Browsers connecting to `/api/ws` carry the `brokoli_session` httpOnly cookie set
at login. The HTTP `JWTAuth` middleware validates the cookie at upgrade time
and propagates the claims (including `org_id`) into the request context before
SODP's handler runs.

Non-browser clients can pass the JWT one of three ways:

```
ws://host/api/ws?token=<jwt>           # query string
Authorization: Bearer <jwt>             # header
Cookie: brokoli_session=<jwt>           # cookie
```

If `BROKOLI_JWT_SECRET` is unset and no users exist, the server runs in open
mode and `/api/ws` is rejected with `503` until the first admin is created.

## Rate limits and resource caps

| Cap | Default | Where |
|---|---|---|
| Max concurrent sessions | 4096 | `pkg/sodp/server.go:maxSessions` |
| Max inbound frame size | 64 KiB | `pkg/sodp/server.go:maxFrameBytes` |
| Max state key length | 256 bytes | `pkg/sodp/server.go:maxKeyLen` |
| Max value size on `CALL` | 512 KiB | `pkg/sodp/server.go:maxValueBytes` |
| Per-session mutation rate | 100 ops/sec | `pkg/sodp/session.go:defaultRateLimit` |
| Per-session active watches | 64 | `pkg/sodp/session.go:defaultMaxWatches` |
| Per-key delta log length | 1000 | `pkg/sodp/state.go:defaultDeltaLogCap` |
| Total state keys | 100 000 | `pkg/sodp/state.go:defaultMaxKeys` |
| Run state TTL after completion | 30 min | `api/server.go:StartEviction` |

These are sized for OSS / single-binary deployments. Enterprise distributed
mode (Redis-backed `EventBus`) uses the same defaults.

## Talking to SODP from another language

The reference TypeScript client is `@sodp/client@0.2.1`. There are also
official Python (`sodp` on PyPI) and Java (`io.sodp:sodp-client`) clients.

A minimal browser-side subscription that mirrors what `ws.ts` does:

```ts
import { SodpClient } from "@sodp/client";

const client = new SodpClient("wss://your-host/api/ws", {
  reconnect: true,
});

client.watch("_events", (events, meta) => {
  if (meta.source === "init") {
    // STATE_INIT — set baseline, don't replay history
    return;
  }
  // DELTA — process newly appended events
  console.log("new events:", events);
});
```

For server implementors targeting Brokoli's SODP server, the field-name
cheat-sheet (Brokoli matches the upstream Rust reference exactly):

- `WATCH` body uses `state`, not `key`.
- `RESUME` body uses `state` and `since_version`.
- `STATE_INIT` body has `state`, `version`, `value`, `initialized` at the top
  level (not nested under `entries`).
- `CALL` body is `{ call_id, method, args: { state, value | patch | path } }`.
- `RESULT` body is `{ call_id, success, data }`.
- Delta op names are **uppercase** (`"ADD"`, `"UPDATE"`, `"REMOVE"`).

## Known workarounds

The integration with `@sodp/client` has surfaced a small number of upstream
issues, all currently closed. The `pkg/sodp/WORKAROUNDS.md` file tracks any
open ones — at the time of writing the list is empty.

## Tests

| Test | What it covers |
|---|---|
| `pkg/sodp/sodp_test.go` | 50+ unit tests for frame encode/decode, state store, ring buffer, delta application, fanout filtering, tenant isolation, body decoders, eviction, append semantics |
| `pkg/sodp/integration_test.go` | 12 integration tests using a real `httptest` server and `gorilla/websocket` clients: HELLO, WATCH/STATE_INIT, DELTA fanout, full run lifecycle, multi-client broadcast, disconnect cleanup, invalid keys, heartbeat echo, CALL mutations, watcher-receives-call-delta, connection limit |
| `pkg/sodp/crosslang_test.go` | Spawns Node.js running `@sodp/client@0.2.1` against a real SODP server. Validates protocol compatibility, `meta.source` semantics, `applyOps` array operations, the exact `ws.ts` baseline pattern, and `state.set` round-trip |
| `api/eventbus_bridge_test.go` | Distributed mode: worker publishes to `EventBus`, API subscribes, mutation reaches SODP, watchers receive deltas |
| `api/ws_jwt_context_test.go` | Regression: WS upgrade carries JWT, downstream handler sees `claims` and `org_id` on the request context (would have caught the multi-tenant collapse bug we shipped a workaround for once) |

Run with:

```bash
go test ./pkg/sodp/ ./api/ -count=1 -race
```

The cross-language test requires `node` in `PATH` and `@sodp/client` installed
under `ui/node_modules`. It is skipped automatically when either is missing.
