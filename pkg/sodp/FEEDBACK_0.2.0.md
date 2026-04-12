# Feedback sent to SODP team — `@sodp/client@0.2.0` upgrade

**Date sent**: 2026-04-12
**Pinned version at time of sending**: `@sodp/client@0.2.0`
**Related**: [WORKAROUNDS.md](./WORKAROUNDS.md) — `client-ts#applyOps-null-array-init`

---

Subject: SODP client 0.2.0 — works great, found one edge case in `applyOps`

Hey team,

Quick follow-up after upgrading to `@sodp/client@0.2.0`. All seven of the items from our last round are confirmed fixed end-to-end against our Go server — `meta.source`, `applyOps` re-export, dual CJS/ESM, the `/-` array append, the unknown-op throw, and the protocol docs link. The throw on unknown op types is especially nice; it caught a stale test of ours immediately instead of silently diverging.

While running the integration suite I hit one regression that the 0.2.0 fix didn't quite cover.

## `applyOps(null, ADD "/-")` produces an object instead of an array

The fix handles `applyOps([…], ADD "/-")` correctly, but when the cached value is `null` (the watched key has never been written), the same op falls through to the object code path and produces `{"-": value}` instead of `[value]`. A streaming client that subscribes *before* the first append then diverges from the actual server state.

**Repro** (against `@sodp/client@0.2.0`):

```js
import { applyOps } from "@sodp/client";

console.log(applyOps(null,      [{op:"ADD", path:"/-", value:"x"}])); // {"-":"x"}   ❌
console.log(applyOps(undefined, [{op:"ADD", path:"/-", value:"x"}])); // {"-":"x"}   ❌
console.log(applyOps([],        [{op:"ADD", path:"/-", value:"x"}])); // ["x"]       ✅
console.log(applyOps(["a"],     [{op:"ADD", path:"/-", value:"x"}])); // ["a","x"]   ✅
```

**How we hit it**: server appends to a previously-empty event stream key. First client (who watched before any events existed) gets STATE_INIT with `value: null`, then DELTA `ADD /- value=event1`. Cache becomes `{"-": event1}`. Subsequent appends keep mutating the `"-"` field of an object instead of growing an array.

**Suggested fix** (in `delta.js`, `applyOp`): when the last path segment is `-` and the parent state is `null`/`undefined`/non-array, initialize an empty array instead of an empty object. Roughly:

```js
const isArrayAppend = parts.length >= 1 && parts[parts.length - 1] === "-";
const root = (Array.isArray(state)
  ? structuredClone(state)
  : (typeof state === "object" && state !== null
      ? structuredClone(state)
      : (isArrayAppend ? [] : {})));
```

Same idea probably applies to the Python and Java clients if their `applyOps` equivalents have the same `state == null → new dict()` fallback.

**Our workaround** (server-side, in Go): on the first append into an empty key, emit root `UPDATE` to seed the client cache as an array. Subsequent appends use the efficient `ADD /-`. So for a 100-element event stream we now pay O(n) once, then O(1) per append. Tracked in `pkg/sodp/WORKAROUNDS.md` as `client-ts#applyOps-null-array-init` so we can drop it the moment a fix lands.

## Re: README quote

Happy to send something. Rough draft, edit freely:

> "We integrated SODP into Brokoli (an Airflow-style data orchestration platform written in Go) to replace our hand-rolled WebSocket event hub. The protocol mapped cleanly onto our run/node/log state model, the binary delta fanout cut wire bandwidth significantly compared to JSON, and `@sodp/client`'s presence + RESUME features got us reconnect-resilient dashboards essentially for free. The TypeScript client's clean `StateRef` API made the Svelte integration a one-file change."
> — Brokoli team

Let me know if you'd like more or less, or a different angle (perf numbers, multi-tenant isolation, etc — happy to dig those out).

Cheers, and thanks again — the 0.2.0 turnaround was fast and the fixes are solid.

— Brokoli team

---

## Response status

- [x] Acknowledged by SODP team (2026-04-12)
- [x] Fix released — `@sodp/client@0.2.1`, `sodp@0.2.1` (Py), `io.sodp:sodp-client:0.2.1` (Java)
- [x] `WORKAROUNDS.md` entry closed and `Append()` simplified

The team's root-cause writeup matched our diagnosis exactly: when the current
node wasn't already a container, the reducers unconditionally defaulted to
`{}`. The fix is a single rule applied at every materialization point —
"if the next path segment is `-` or purely numeric, materialize an array;
otherwise an object." Six new TS tests (plus 5 in Python and 5 in Java) lock
in the regression including our exact repro cases.
