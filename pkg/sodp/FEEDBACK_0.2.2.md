# Feature request to SODP team — prefix watching

**Date drafted**: 2026-04-12
**Pinned version**: `@sodp/client@0.2.1`
**Related**: [WORKAROUNDS.md](./WORKAROUNDS.md), [FEEDBACK_0.2.0.md](./FEEDBACK_0.2.0.md), [FEEDBACK_0.2.1.md](./FEEDBACK_0.2.1.md)

---

Subject: SODP feature request — prefix subscriptions for dynamic key namespaces

Hey team,

We're refactoring Brokoli's UI to be fully state-driven on top of SODP — getting rid of the event-stream shim we built on the `_events` array key and watching the underlying run/node state directly. While doing this, we ran into a missing primitive that we'd like to propose as a SODP feature.

## The use case

Brokoli's data model has a dynamic namespace under `runs.*`:

```
runs.{run_id}                       → run-level state
runs.{run_id}.nodes.{node_id}       → per-node state
runs.{run_id}.logs                  → bounded log buffer
```

New `runs.{id}` keys appear at runtime as users trigger pipelines. Pages like our Dashboard need to **discover and display all currently-active runs without knowing the IDs ahead of time**.

With `WATCH { state: "exact.key" }`, we'd need to:

1. Keep a server-side index key (e.g., `runs._index`) listing all active run IDs.
2. Have the UI watch the index, then issue a separate `WATCH` for each ID it sees.
3. Manage subscribe/unsubscribe lifecycle as runs come and go.

That works (it's our current fallback plan), but it has three downsides:

- The index key is itself a list that grows and gets capped, so it has the same "is this entry new or did the cap roll over" problem we just escaped by going state-driven.
- Round-trip latency: discover-then-subscribe is two RTTs before any data flows.
- Subscriber count scales with the number of active items, and we hit `defaultMaxWatches=64` quickly on busy dashboards.

## Proposed feature

**Prefix watch.** Allow `WATCH` to take a key pattern with a trailing `.*` glob:

```ts
client.watch("runs.*", (key, value, meta) => {
  // Fires for every existing key matching the pattern (during STATE_INIT)
  // and for every future delta on any matching key.
});
```

The callback signature would need a `key` parameter so the watcher knows *which* matching key changed. Today's `watch(key, cb)` callback is `(value, meta) => void` because there's only one key — for prefix watch we'd need `(key, value, meta) => void` or a new method `watchPrefix(pattern, cb)` to keep types backward compatible.

### Wire format sketch

We sketched what the frame bodies could look like — happy to revise based on what fits the protocol best:

**WATCH** (existing frame, new body shape):
```json
{ "state": "runs.*" }
```

**STATE_INIT** for a prefix watch — one frame per matching key, all sharing the same `stream_id`:
```json
{ "state": "runs.abc", "version": 7, "value": {...}, "initialized": true, "matches_pattern": "runs.*" }
```

The `matches_pattern` field tells the client this STATE_INIT was sent in response to a prefix subscription, not a direct watch — so the client routes the callback by pattern instead of by exact key.

Alternative: a new `STATE_INIT_BATCH` frame type that carries `{ pattern, entries: [{state, version, value}] }`. Cheaper on the wire for prefix-watches with many matches; needs a new frame type opcode.

**DELTA** for a prefix watch — same body as today but the client routes it by pattern match against the key in the body. No frame format change needed.

### Pattern semantics

We'd suggest matching on **dot-prefixed components**:

- `runs.*` matches `runs.abc`, `runs.abc.nodes.n1`, `runs.abc.logs`, `runs.def`, etc.
- `runs.abc.nodes.*` matches `runs.abc.nodes.n1` and `runs.abc.nodes.n2` only.
- No multi-level glob (`**`) needed for our use case — `*` matches any number of dot segments.

The same semantics our `BroadcastAll` already uses for parent-prefix fanout in the Go server. We're already walking parent prefixes when broadcasting deltas — we just don't currently expose that on the read side.

### Authorization

Per-key tenant isolation should still apply: a session subscribed to `runs.*` only receives keys whose stored `org_id` matches the session's org. Same `keyAllowedForSession()` check as today's exact-key watch, just per matched key during STATE_INIT and per delta.

### What it lets us delete

In Brokoli's UI, prefix watching would let us delete:

- The server-side `_events` log key entirely
- The `dashboard.{org}` aggregate key (we'd compute aggregates client-side from the watched runs)
- Per-page index keys
- The dedup Set in our `ws.ts` shim (events become irrelevant — we react to state)
- The `liveRunStatuses` reconciliation we just added, because there's no way for client state to drift from server state once watches are content-addressed

## Why we can't do this fully in our Go server alone

Our Go SODP server (`pkg/sodp` in the Brokoli repo) implements the protocol you spec'd. We could add prefix-watch frame handling there — the FanoutBus already has the per-key subscriber model and the `BroadcastAll` parent-walk to leverage. But:

1. `@sodp/client@0.2.1` doesn't speak prefix watch, so the UI couldn't consume it.
2. Diverging from upstream SODP defeats the point of using a standard protocol with a maintained client.

So we'd rather wait for upstream support than fork either side. **In the meantime** we'll use the index-key fallback (option (1) above) so the OSS release can ship now, and switch to prefix watching as soon as a `@sodp/client` release supports it.

## Effort estimate

If the trickiest part is the new frame body format, the rest is small:

- **TypeScript client**: extend `watch()` to accept a glob pattern, route incoming frames by pattern match. Maintain a separate `prefixWatches` map alongside the existing per-key one. ~40 lines.
- **Rust reference server**: add prefix routing in the fanout bus and the WATCH handler. The Rust state store presumably already has prefix iteration for SegmentedLog compaction; reuse that for STATE_INIT enumeration. ~80 lines.
- **Tests**: prefix watch fires on existing matching keys at subscribe time, fires on later mutations of matching keys, doesn't fire for non-matching keys, respects ACL/tenant rules per matched key. ~6 unit tests + 2 integration tests.

We'd be happy to mirror whatever you ship in our Go implementation so the cross-language test in `pkg/sodp/crosslang_test.go` covers the behavior end-to-end.

## Workarounds we considered and rejected

1. **Maintain index keys server-side** — what we're doing in the meantime. Reintroduces the "growing array" problem we just escaped.
2. **Have the UI watch every individual key it might display** — doesn't work because the UI doesn't know the keys ahead of time, and we'd hit `maxWatches=64` quickly.
3. **Use REST polling for discovery and SODP only for live updates of known keys** — adds latency, fights the state-driven model.
4. **Push aggregates as a single key (`dashboard.{org}`) recomputed on every event** — works but the bridge doing per-event recomputes feels like the wrong layer to be doing aggregation.

Prefix watching is the primitive that makes the state-driven UI model fall out cleanly. Everything else is a workaround.

Cheers, and thanks again — the 0.2.x line has been great to work with.

— Brokoli team

---

## Response status

- [ ] Acknowledged by SODP team
- [ ] Spec / API design discussion
- [ ] Released in `@sodp/client` and reference server
- [ ] Brokoli UI switched from index-key fallback to prefix watch
