# Reply sent to SODP team — `@sodp/client@0.2.1` upgrade confirmation

**Date sent**: 2026-04-12
**Pinned version at time of sending**: `@sodp/client@0.2.1`
**Related**: [WORKAROUNDS.md](./WORKAROUNDS.md), [FEEDBACK_0.2.0.md](./FEEDBACK_0.2.0.md)

---

Subject: Re: Re: SODP client 0.2.0 — works great, found one edge case in applyOps

Hey team,

0.2.1 confirmed working end-to-end. All five repro cases from your reply pass against `@sodp/client@0.2.1` exactly as documented:

```
applyOps(null,      [{op:"ADD", path:"/-",       value:"x"}]) → ["x"]            ✓
applyOps(undefined, [{op:"ADD", path:"/-",       value:"x"}]) → ["x"]            ✓
applyOps(null,      [{op:"ADD", path:"/0",       value:"x"}]) → ["x"]            ✓
applyOps(null,      [{op:"ADD", path:"/items/-", value:"x"}]) → { items: ["x"] } ✓
applyOps(null,      [{op:"ADD", path:"/name",    value:"x"}]) → { name: "x" }    ✓
```

The "next path segment is `-` or purely numeric → array, otherwise object" rule applied at every materialization site is exactly the right shape. Cleaner than a dedicated isArrayAppend flag and it generalizes to nested array creation, which my suggested patch wouldn't have.

Server-side workaround removed in the same commit as the upgrade — `pkg/sodp/state.go:Append()` is now unconditionally `ADD "/-"` (plus `REMOVE "/0"` on cap-trim). Both the unit test (`TestStateAppend`) and the cross-language test (`TestCrossLanguage_SodpClient`) pass against 0.2.1 with the streaming-watcher-into-empty-key path that originally surfaced the bug. Also took your suggestion to delete the WORKAROUNDS.md entry and the workaround code in the same commit so the "why" is preserved in `git log` rather than orphaned in a tracking file.

## On the Rust reference server diff algorithm — your question

To be honest about what we actually do: our Go server **doesn't** compute element-level array diffs either. `Diff()` (`pkg/sodp/delta.go:32`) only structurally diffs nested **maps**. When either side of the diff is a non-map, we fall through to a single root-level `UPDATE` op — same atomic-array behavior as your Rust server.

The only place `/-` shows up is `StateStore.Append()`, a dedicated server-side API the engine calls when adding to an append-only state slice (we use it for the `_events` event stream and the per-run log buffer). That call site knows it's appending one element, so it skips the diff entirely and emits a single `ADD "/-"` op directly. There's no LCS, no keyed-by-id matching, no general-purpose array differ behind it.

So if you're asking "should the Rust server do general element-level array diffs to match what the Go server emits": we're not really evidence in favor, because we don't do it either. We just expose a separate `Append()` API for the append-only case where diffing is unnecessary. For general array mutations the right answer is probably workload-dependent — collaborative editing wants keyed-by-id matching, time-series wants append-only, sortable lists want LCS — and trying to pick one in the reference server might be premature.

If you're considering it anyway: I'd think hardest about whether the wire-cost savings are large enough to justify the cost of getting array diffing wrong. Atomic array replacement is at least correct under all circumstances; a smart differ that occasionally produces a sub-optimal patch is probably fine, but one that occasionally produces the *wrong* patch is a nightmare to debug. The dedicated `Append()` pattern sidesteps this entirely for the most common high-frequency case (event streams, logs).

Happy to dig deeper if useful — could share the relevant parts of `delta.go` and `state.go` if you want a concrete reference for what a "diff arrays atomically + dedicated append API" implementation looks like in practice.

## Re: README quote — perf number

Honest answer: I don't have a measured number to quote that I'd be willing to put in a public README. The "binary delta fanout cut wire bandwidth significantly" line in the original quote is a qualitative claim about expected behavior given how SODP is designed, not something we benchmarked against the old hand-rolled JSON hub.

I could go run the comparison properly — instrument both implementations, drive the same workload through each, capture wire bytes per second on a representative pipeline run. That would give us a real number for the README. It'll take a day or two to do honestly. Let me know if it's worth it for the 0.2.1 release notes timing or if you'd rather ship the README update now with the qualitative phrasing.

If you go with the qualitative version, the existing draft is fine as-is. If you want to wait for the number, here's a rephrased version that's specific about what we measured but without a fabricated multiplier:

> "We integrated SODP into Brokoli to replace our hand-rolled WebSocket event hub. The protocol mapped cleanly onto our run/node/log state model, the binary delta fanout reduced wire bandwidth on high-frequency log events, and `@sodp/client`'s presence + RESUME features got us reconnect-resilient dashboards essentially for free. The TypeScript client's clean `StateRef` API made the Svelte integration a one-file change."
> — Brokoli team

(No multiplier, "reduced" instead of "cut significantly", everything else unchanged.)

Thanks again for the fast turnaround on 0.2.1 and for asking the diff-algorithm question directly — it's a good prompt to think about whether the way we currently solve the append-only case is the right thing to standardize across the ecosystem.

— Brokoli team

---

## Response status

- [ ] Acknowledged by SODP team
- [ ] Decision on README quote phrasing (qualitative vs measured)
- [ ] Decision on whether to land an array-diff pass in the Rust reference server
