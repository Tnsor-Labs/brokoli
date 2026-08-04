# ADR-013: Extend the plugin protocol with work-unit planning, structured progress, and cancellation

**Status:** proposed
**Date:** 2026-08-04

## Context

Brokoli already made the core decision the RFC's "polyglot connector protocol" (§8) is asking for. ADR-002 through ADR-005 already chose a small, language-neutral protocol for connectors: a plugin is any executable that supports a handful of subcommands (`spec`, `check`, `discover`, `read`, `write`) and speaks newline-delimited JSON over stdin/stdout — modeled on Airbyte's connector protocol (`pkg/plugins/protocol.go`'s package doc comment). This already means a connector can be written in any language that can read and write a line of JSON. That's the hard part, and it's done.

What it doesn't give us yet is what the RFC's *physical planning* vision (§18.2, "pagination expansion, dataset partition discovery, dynamic instance expansion") needs from a connector:

- **No way for a connector to describe independent work units.** `discover` returns a list of streams (tables, endpoints); `read` then reads one stream start-to-finish as a single blocking invocation. There's no equivalent of "here are the 100 pages of this API call, each of which the host can schedule, retry, and report progress on independently" — which is exactly the shape [#30](https://github.com/Tnsor-Labs/brokoli/issues/30)'s in-process pagination loop and [#31](https://github.com/Tnsor-Labs/brokoli/issues/31)'s in-node `.expand()` had to build inside the *engine* instead, because the *protocol* has nowhere to express it for an external plugin.
- **No structured progress, only logs.** `MsgLog` (`pkg/plugins/protocol.go`) carries a level and a free-text message. There's no typed message carrying the RFC §17 dimensions — current/total/unit, rows in/out, bytes in/out, rate — that a UI could render as a real progress bar instead of a scrolling log.
- **No way to ask a running plugin to stop.** A plugin invocation blocks until it exits on its own or the host's overall timeout fires. [Tnsor-Labs/brokoli#29](https://github.com/Tnsor-Labs/brokoli/pull/29) (merged) threaded a real, cancellable `context.Context` all the way through `extensions.ExecutionContext` for in-process executors — but `pkg/plugins`'s subprocess runner doesn't use it to signal a running plugin process yet.

One thing worth calling out as a genuine, already-working win, not a gap: `MsgState` already gives plugins a real checkpoint/resume mechanism — a source can emit an incremental-sync cursor that the host persists and hands back on the next invocation of the same stream. That's most of what RFC §16.1 asks for; it just isn't named "checkpoint" in this codebase.

## Decision

Extend the existing protocol rather than design a new one:

1. **Add a `plan` command.** Given a config, a source plugin returns a list of independent work units (the RFC's "partitions" — a page range, a file list, a table's partitions, whatever makes sense for that connector) instead of just a list of streams. The host can then schedule, retry, and track each unit the way [#31](https://github.com/Tnsor-Labs/brokoli/issues/31)'s `expansion_instances` table already does for in-process `.expand()` — this is the natural generalization of that mechanism to external plugins, not a separate design.
2. **Add `MsgProgress`.** A new JSONL message type alongside the existing `MsgRecord`/`MsgState`/`MsgStream`/`MsgLog`/`MsgStatus`/`MsgError`, carrying the RFC §17 progress dimensions. Kept as an *addition* to the message set — old plugins that never emit it keep working exactly as today, per the protocol doc comment's own stated compatibility rule ("Unknown types are ignored by the host so old plugins don't break new hosts and vice versa").
3. **Wire cancellation through to the subprocess.** `pkg/plugins`'s runner already gets a real `context.Context` from the engine as of #29; use it to signal the child process (e.g. terminate on context cancellation) instead of only relying on the plugin exiting on its own.

## Consequences

### Positive

- Pagination and dynamic expansion become something a *plugin author* can implement for their own connector, using the same work-unit model the engine now uses internally for `.expand()` — instead of every such capability having to be built into the Go engine directly, as `#30`/`#31` had to do because the protocol had no other way to express it.
- A UI (whenever that work happens) has a real, structured signal to render instead of parsing log lines.
- Cancelling a run can actually stop a long-running external fetch instead of waiting for it to finish or time out.

### Negative

- `plan`/`discover` overlap conceptually (both describe "what can this connector produce") and need a clear, documented relationship rather than becoming two competing ways to ask the same question — this is the first concrete design question anyone picking this up should resolve, ideally by making `plan` an optional refinement step after `discover` rather than a replacement for it.
- Existing plugins don't gain planning/progress/cancellation for free — a plugin has to be updated to implement `plan` and emit `MsgProgress` to benefit. That's expected and consistent with how `MsgState` adoption already works (some plugins support incremental sync, some don't).

### Deferred

- Additional connector transports beyond local subprocess — a gRPC-based long-running connector service, or a container-job transport, for connectors that shouldn't be spawned as a short-lived process per invocation (RFC §8.2 lists both). ADR-004 already chose to keep the plugin runtime simple and in OSS; revisit this only if a real need for long-running/remote connectors shows up, not speculatively.
- Generated multi-language protocol bindings and a formal conformance test suite (RFC §20) — valuable, but a larger, separate effort once the protocol additions above have shipped and stabilized.
- Renaming `MsgState` to something more obviously "checkpoint" — not worth a breaking rename for a naming preference; document the mapping instead.

## Alternatives considered

- **Design a brand-new protocol from the RFC's §8 sketch (Protobuf/gRPC, generated bindings for Go/Python/Rust/Java) instead of extending the existing one.** Rejected — the existing subprocess/JSONL protocol already delivers the actual goal (language-neutral connectors) and has four accepted ADRs and real plugins built against it. Replacing it would be a much larger, riskier effort for a benefit (a different transport) that isn't the thing actually blocking anyone today.
- **Fold `plan` into `discover` instead of adding a new command.** Considered, but `discover` already has a settled meaning (list the available streams) and reusing it for "and also break each stream into schedulable work units" would overload it in a way that's hard to version safely. A new, optional command is a smaller, additive change.

## Follow-ups

- Tracked in [Polyglot connector protocol: work-unit planning, progress, and cancellation](https://github.com/Tnsor-Labs/brokoli/issues) (see the companion issue opened alongside this ADR).
- Whoever picks up a milestone from that issue should come back and update this ADR — resolve the `plan`/`discover` relationship question explicitly once decided, and add a dated "Update" section (see ADR-006 in `brokoli-ee` for the pattern) once the additions have shipped.
