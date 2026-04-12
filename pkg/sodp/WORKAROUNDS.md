# SODP client-ts workarounds

This file tracks every workaround in the Brokoli codebase that exists because
of issues in `@sodp/client`. When upstream fixes land, grep for
`SODP-WORKAROUND` and remove each one.

Upstream repo: https://github.com/orkestri/SODP

Current pinned version: **`@sodp/client@0.2.1`**

## Open issues

### `client-ts#bare-WebSocket.OPEN-reference` — P1

**Problem**: `SodpClient.send()` references `WebSocket.OPEN` as a bare global
constant when checking the underlying socket's readyState, even when the
caller passes a custom `WebSocket` implementation via the constructor's
`WebSocket` option. On Node.js < 21 (the global doesn't exist) the bare
lookup throws `ReferenceError: WebSocket is not defined` the moment any
operation tries to send a frame — including the very first WATCH after the
HELLO handshake completes.

Reproducible from `@sodp/client@0.2.1` source (`dist/esm/index.js:256`):

```js
send(type, streamId, body) {
    if (!this.ws || this.ws.readyState !== WebSocket.OPEN)  // ← bare global
        return;
    this.ws.send(...);
}
```

The `this.opts.WebSocket` constructor is honoured for the connection itself,
but the readyState comparison still requires the global. The two paths are
inconsistent.

**Workaround location**: `pkg/sodp/testdata/sodp_client_test.mjs`

When the native `WebSocket` is missing, after resolving the implementation
via `import("ws")`, the test script also assigns it to `globalThis.WebSocket`
so the bare lookup inside `send()` resolves to the same constructor we're
already passing through options.

**Cleanup when fixed**: Delete the `if (!usedNative) globalThis.WebSocket = ...`
block in `sodp_client_test.mjs`. The right upstream fix is one of:

1. `this.opts.WebSocket.OPEN` — read OPEN from the user-supplied constructor
2. Hard-code the literal `1` — the WebSocket spec defines OPEN as exactly 1
   on every implementation (`CONNECTING=0, OPEN=1, CLOSING=2, CLOSED=3`),
   so the constant lookup is gratuitous indirection

---

## Closed (fixed in @sodp/client 0.2.1)

### `client-ts#applyOps-null-array-init` — P1 — CLOSED

`applyOps` now materializes an array (instead of an object) when applying
`ADD "/-"` or a numeric index path against a `null`/`undefined` state. The
same fix landed in the Python and Java clients. Our server-side seed
workaround in `Append()` is removed — all appends now emit `ADD "/-"`
unconditionally with O(1) wire cost.

Reported: 2026-04-12 (see [FEEDBACK_0.2.0.md](./FEEDBACK_0.2.0.md))
Released in: `@sodp/client@0.2.1`, `sodp@0.2.1` (Python), `io.sodp:sodp-client:0.2.1` (Java)

---

## Closed (fixed in @sodp/client 0.2.0)

These were tracked while integrating `@sodp/client@0.1.1`. Upstream addressed
all of them in 0.2.0 — workarounds removed in this commit.

### `client-ts#applyOps-array-append` — P0 — CLOSED

`applyOps()` now implements RFC 6901 `-` token for arrays (when the value is
already an array — see open issue above for the null case). The server's
`Append()` no longer needs to emit a root-level `UPDATE` per append.

### `client-ts#meta.initialized-semantics` — P1 — CLOSED

`WatchMeta` gained a `source: "cache" | "init" | "delta"` field. `ws.ts` now
checks `meta.source === "init"` instead of using a local `firstCallback`
boolean.

### `client-ts#unknown-op-silent-noop` — P1 — CLOSED

`applyOps` now throws `[SODP] unknown delta op type: "<x>"` on unknown op
types instead of silently no-oping. Any future server bugs that send the
wrong op casing will surface immediately as a thrown error rather than
divergent client cache.

### `client-ts#cjs-only` — P2 — CLOSED

The package now ships dual CJS+ESM via an `exports` map. The cross-language
test script lives at `pkg/sodp/testdata/sodp_client_test.mjs`. Note that
Node ESM still resolves packages relative to the importing file's location,
so we use a `node_modules` symlink in `testdata/` pointing to
`../../../ui/node_modules`. This is a Node behavior, not a SODP issue.

### `client-ts#applyOps-not-exported` — P3 — CLOSED

`applyOps` is now re-exported from the package root: `import { applyOps }
from "@sodp/client"`. The cross-language test uses this directly to verify
op semantics.

### `client-ts#protocol-frame-docs` — P0 — CLOSED

The protocol spec at https://github.com/orkestri/SODP/blob/main/docs/protocol.md
now has a prominent link from the client-ts README, with field-name
cheat-sheet (`state`, `since_version`, `call_id`, etc.). Our `frame.go`
struct tags match the documented schema.
