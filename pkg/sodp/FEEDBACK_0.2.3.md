# Bug report to SODP team — bare `WebSocket.OPEN` reference in send()

**Date drafted**: 2026-04-13
**Pinned version**: `@sodp/client@0.2.1`
**Severity**: P1 — blocks Node.js < 21 runtime, including the GitHub Actions
ubuntu-latest image at the time of writing
**Related**: [WORKAROUNDS.md](./WORKAROUNDS.md), [FEEDBACK_0.2.0.md](./FEEDBACK_0.2.0.md), [FEEDBACK_0.2.1.md](./FEEDBACK_0.2.1.md), [FEEDBACK_0.2.2.md](./FEEDBACK_0.2.2.md)

---

Subject: @sodp/client@0.2.1 — `SodpClient.send()` references `WebSocket.OPEN` as a bare global, breaks Node < 21 even when `WebSocket` is passed via options

Hey team,

Quick bug report from CI. We hit this on the GitHub Actions ubuntu-latest
runner (Node 20 LTS) right after rolling our cross-language test out across
the matrix.

## Repro

`@sodp/client@0.2.1` on Node 20.x with `ws@^8.20.0` installed:

```js
import { SodpClient } from "@sodp/client";
import WebSocket from "ws";

const client = new SodpClient("ws://localhost:8080/api/ws", {
  WebSocket,        // explicitly pass the implementation
  reconnect: false,
});

await client.ready;
client.watch("some.key", (value) => console.log(value));
// → ReferenceError: WebSocket is not defined
//   at SodpClient.send (dist/esm/index.js:256:48)
//   at SodpClient.sendWatch (dist/esm/index.js:275:14)
//   at SodpClient.watch (dist/esm/index.js:301:22)
```

The connection itself opens fine — `await client.ready` resolves and the
HELLO/AUTH handshake completes. The crash hits the moment we try to send
the first WATCH frame.

## Root cause

`dist/esm/index.js:256` (and the same line in the CJS build):

```js
send(type, streamId, body) {
    if (!this.ws || this.ws.readyState !== WebSocket.OPEN)
        return;
    this.ws.send(/* msgpack-encoded frame */);
}
```

The `WebSocket.OPEN` reference reaches for the global, not
`this.opts.WebSocket.OPEN`. On Node 21+ this works because the platform
ships a native global; on Node < 21 it doesn't, and the function throws.

The two code paths are inconsistent: the connection itself is opened via
`new this.opts.WebSocket(...)` (which honours the option), but the
readyState check inside `send()` uses the bare global. So the option is
half-supported — enough to get past the constructor and the `await ready`,
but not enough to actually send any frames.

## Suggested fix

Two clean options, either works:

**Option A — read OPEN from the user-supplied constructor:**

```js
send(type, streamId, body) {
    const WS = this.opts.WebSocket;
    if (!this.ws || this.ws.readyState !== WS.OPEN)
        return;
    this.ws.send(/* ... */);
}
```

This is the symmetric fix to whatever `this.opts.WebSocket` already does at
construction time. Same pattern, same source of truth.

**Option B — hard-code the literal `1`:**

```js
send(type, streamId, body) {
    if (!this.ws || this.ws.readyState !== 1) // 1 = OPEN
        return;
    this.ws.send(/* ... */);
}
```

The WebSocket spec defines the readyState constants as exactly:

| Constant     | Value |
| ------------ | ----- |
| `CONNECTING` | 0     |
| `OPEN`       | 1     |
| `CLOSING`    | 2     |
| `CLOSED`     | 3     |

Every conformant implementation — native browser, native Node 21+, the `ws`
package, deno, undici — uses these exact integers. The constant lookup is
gratuitous indirection.

I'd lean toward A (more readable, self-documenting) but B has the advantage
of making this class of bug structurally impossible by removing the global
reference entirely. Same one-line surface area in either case.

## Where else this pattern might exist

A grep for `WebSocket\.` in `dist/esm/index.js` would catch other bare
references. From a quick read I only see the one in `send()`, but a sweep
would be worth doing while you're in there. Same fix applies anywhere
`WebSocket.X` appears bare on the right-hand side of an expression.

## Impact for us

We hit this when CI started running our `TestCrossLanguage_SodpClient`
against the published `@sodp/client` instead of just locally on Node 24.
The matrix expansion was the trigger; the bug was always there.

We worked around it by polyfilling `globalThis.WebSocket` from the
resolved `ws` constructor before instantiating the client, in the test
script only. That's tracked in [WORKAROUNDS.md](./WORKAROUNDS.md) as
`client-ts#bare-WebSocket.OPEN-reference` and will get deleted as soon as
0.2.2 (or whatever the next release is) ships with the fix.

The polyfill is fine for the test script, but a real Node-server consumer
would have to do the same monkey-patch in their app entry point, which is
the kind of thing that breaks in subtle ways the moment someone instantiates
two SODP clients with different transports. The right place for the fix is
inside the client.

Cheers, and as always — appreciate the fast turnaround on the previous
rounds.

— Brokoli team

---

## Response status

- [ ] Acknowledged by SODP team
- [ ] Fix released (target: `@sodp/client@0.2.2`)
- [ ] Workaround removed from `pkg/sodp/testdata/sodp_client_test.mjs`
- [ ] `WORKAROUNDS.md` entry moved to "Closed"
