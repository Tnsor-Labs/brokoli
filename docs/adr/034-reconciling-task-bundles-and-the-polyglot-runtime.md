# ADR-034: Reconciling task bundles and the code-node protocol with the polyglot task runtime

**Status:** proposed
**Date:** 2026-09-05

## Context

ADR-032 (portable task interfaces) and ADR-033 (polyglot task runtime
protocol) were written and merged the same day, stacked on each other, with
no reference anywhere in either document to ADR-029, ADR-030, or ADR-031 —
the three ADRs that shipped the code-node worker pool, the TypeScript
dispatch path, and content-addressed task bundles in the two weeks
immediately before. This is not a citation nit. Each of the three omitted
ADRs is live, released code that ADR-032/033 either duplicate or directly
contradict:

1. **Two task-bundle formats with no stated relationship.** ADR-031 shipped
   `task-bundle/1`: a `task_bundle` object (`{"digest", "format"}`) on an
   existing `code` node's config, resolved and mounted before the engine
   dispatches to the language already selected by ADR-030. brokoli-sdk
   already emits it (`@task(package="bundle")`, `brokoli-sdk#90`), with
   contract tests pinned against a released core. ADR-033 defines
   `brokoli.task-bundle/v2` — a different manifest shape (`payloads[]`,
   `effects`, `dependency_lock`, OCI payload references) — attached to a
   **new `"type": "task"` IR node**, not the `code` node ADR-031 amended.
   ADR-033 §2 says only that "the existing Python bundle format remains a
   legacy input and is translated or executed by the Python compatibility
   adapter," without saying whether that sentence refers to ADR-031's
   `task-bundle/1` at all. A reader — or an implementor — cannot tell
   whether v1 is superseded, whether `code`+`task_bundle` pipelines deployed
   under ADR-031 keep running, or whether `@task(package="bundle")` now
   emits the wrong thing.
2. **Two process-reuse models for warm execution, silently coexisting.**
   ADR-029's pool holds one long-lived worker process per
   `(interpreter, wrapper version, limits)` key, open over a framed Unix
   socket, and multiplexes many sequential `exec` frames across that one
   process before recycling it after `max_execs_per_worker`. ADR-033 §10
   describes a different shape: "each attempt runs in a fresh child/sandbox
   even when environment and supervisor caches are warm" — a
   fresh-process-per-attempt model with a *warm supervisor* underneath it
   (closer to a preforking model than to ADR-029's exec-multiplexing pool).
   Both are legitimate designs; nothing says which one governs which
   execution path, or whether ADR-033 is quietly replacing ADR-029's reuse
   strategy for every code path rather than only the new one.
3. **Two wire protocols with no stated boundary.** ADR-029's framed binary
   protocol (`uint32 BE len | version | type | JSON`) over a socket is
   implemented and shipped (`pkg/codeexec`, `codeexec: add JavaScript
   wrapper contract v1 (#428)`, `engine: dispatch TypeScript code workers
   with limits (#429)`). ADR-033 §7 defines `brokoli.task-runtime/v1` as
   duplex JSON Lines on a harness process's stdin/stdout. Neither document
   says whether the second is a new transport for a new execution path, or
   a replacement for the first.
4. **ADR-031 itself is still `Status: proposed`** in the index despite its
   implementation being merged and released (v0.11.0), which is inconsistent
   with this repository's own stated practice: an ADR earns `Accepted` by
   staying current as it is built, in the same PR as the code where
   possible (`docs/adr/README.md`, "Keeping an ADR current after
   implementation").

None of ADR-032/033's content is wrong on its own terms — the portable type
system and the adapter/capability-matching model are the right direction.
The problem is that both were merged as if the codebase they describe were
still the pre-029 codebase, when it is not. This ADR is the amendment
ADR-031 itself modeled ("Bundle loading is an ADR-030 amendment, stated here
explicitly") and that ADR-033 promises in spirit but never delivers for its
own two predecessors.

## Decision

This ADR amends ADR-029, ADR-031, ADR-032, and ADR-033. It makes no new
technology choices; it resolves the relationships the four left unstated,
so implementation of ADR-032/033 can start without contradicting what is
already running in production.

### 1. Task-bundle version relationship: v2 is additive, v1 is not silently retired

`task-bundle/1` (ADR-031) and `brokoli.task-bundle/v2` (ADR-033) are both
real formats going forward, distinguished by which IR node carries them:

- A `code` node's `task_bundle` field continues to mean `task-bundle/1`,
  resolved and mounted exactly as ADR-031 specifies, dispatched through the
  ADR-029/030 pool exactly as today. This path is not deprecated by this
  ADR and requires no server or SDK change to keep working.
- A `task` node (new, per ADR-033) carries `task-bundle/v2` exclusively.
  A server advertising `task-runtime-v1` accepts `task` nodes; a server
  advertising only `task-bundles` (ADR-031's capability name) never
  receives one — capability names are distinct on purpose, so an old
  server fails a `task`-node pipeline at deploy preflight, never at run
  time.
- ADR-033 §2's "existing Python bundle format remains a legacy input"
  sentence is amended to say explicitly what it meant: **ADR-031's
  `task-bundle/1`**, not some undefined earlier state. The Python
  compatibility adapter this sentence describes is the thing that lets a
  future `task`-node-capable worker also still execute `code`+`task_bundle`
  pipelines it encounters — it is not a claim that v1 pipelines get
  rewritten to v2.
- **Migration is SDK-driven, not server-driven, per node — not a flag day.**
  Once an SDK ships `task` node emission (ADR-031's own follow-up item,
  "add core validation and execution tests for bundle digest references,"
  generalizes to this), `@task(...)` compiles to a `task` node with
  `task-bundle/v2` when the target server advertises `task-runtime-v1`, and
  continues to compile to a `code` node with `task-bundle/1` against a
  server that only advertises `task-bundles` — the same
  capability-preflight discipline ADR-014 already establishes for every
  other feature gate. `@task(package="bundle")` (brokoli-sdk#90) does not
  change behavior until the SDK adds `task-runtime-v1` emission as its own
  follow-up; nothing here breaks it retroactively.
- A future ADR may declare `task-bundle/1` deprecated once `task` nodes have
  shipped and a retention window has passed, following ADR-033 §18's
  general migration shape. This ADR does not declare that deprecation now;
  declaring it before `task-runtime-v1` has a working adapter would leave
  bundle-authoring pipelines with no working forward path.

### 2. Process-reuse models apply to different, explicitly named paths

ADR-029's exec-multiplexing pool (one warm process serving many sequential
`exec` frames over a socket, recycled after `max_execs_per_worker`) is
unchanged and continues to govern:

- bare `code` nodes (a `script`, no `task_bundle`);
- `code` nodes carrying `task-bundle/1` (Decision 1).

ADR-033's fresh-child-per-attempt-with-warm-supervisor model governs `task`
nodes exclusively. The two are not competing descriptions of the same
mechanism; they are the reuse strategy for two different node kinds, and an
implementation must not attempt to unify them into one pool with two
personalities. Concretely: the ADR-029 `Pool`/`Worker` types keep serving
`code` nodes as they do today; a `task`-node execution path is new code
behind the same worker-registration and capability-matching surface ADR-033
§5 defines, not a modification of `pkg/codeexec`'s existing pool state
machine. If a future measurement shows the two should converge on one
reuse strategy, that is a new ADR justified by the measurement — not an
assumption either document gets to make silently.

### 3. Protocol layering: ADR-033's protocol is scoped to the task harness, not a pool replacement

`brokoli.task-runtime/v1` (duplex JSON Lines) is the protocol between a
runtime adapter and the one-shot harness process it launches for a `task`
node attempt, per Decision 2. It does not replace ADR-029's framed socket
protocol, which remains the protocol between the Go engine and a pooled
`code`-node worker. A worker process speaks at most one of these two
protocols, determined by which node kind it was launched to execute; no
process is expected to multiplex both.

### 4. ADR-031 status corrected to Accepted

ADR-031's `task-bundle/1` design shipped and released in v0.11.0
(`feat: package and execute task bundles content-addressed (ADR-031)`) and
is consumed by a released SDK version. Per this repository's own
"Keeping an ADR current after implementation" practice, its status is
corrected from `Proposed` to `Accepted` in this PR, alongside the index
entry in `docs/adr/README.md`.

### 5. What this ADR does not decide

- It does not choose `vm.SourceTextModule` vs. an alternative for `task`
  node module loading (ADR-031 Decision 3's open item) — that remains for
  the ADR-030 amendment ADR-031 already calls for.
- It does not decide whether `task-bundle/1` is ever formally deprecated —
  see Decision 1's closing paragraph.
- It does not re-litigate ADR-032's type system or ADR-033's capability
  model, both of which are sound as written; it only fixes their relationship
  to what already exists.

## Consequences

### Positive

- `code`+`task_bundle` (ADR-031) and the ADR-029 pool keep working exactly
  as released; no in-flight pipeline or shipped SDK behavior changes because
  of ADR-032/033.
- Implementation of ADR-032/033 can start from an explicit, named boundary
  (`task` node / `task-runtime-v1` / `task-bundle/v2`) instead of an
  undefined relationship to the `code` node path, removing a class of bugs
  where a server or SDK guesses which format or protocol applies.
- The ADR index and ADR-031's status now match what is actually running,
  restoring the "ADR-is-stale-is-a-review-blocker" discipline the README
  itself states.

### Negative

- Two task-bundle formats and two node kinds now coexist for the duration
  of the migration window; documentation and the pipeline editor must be
  able to show either without confusing operators about which is current.
- Two process-reuse strategies (exec-multiplexed pool vs. fresh-child warm
  supervisor) must both be maintained until or unless a future ADR justifies
  unifying them.

### Deferred

- Formal deprecation of `task-bundle/1` and the `code`+`task_bundle` path,
  once `task` nodes have a working adapter and a stated retention window.
- Any decision about whether the ADR-029 pool and the ADR-033 supervisor
  model should eventually converge on one reuse mechanism.

## Alternatives considered

- **Silently let ADR-033 supersede ADR-029/031 by implication.** Rejected:
  it is exactly the failure this ADR exists to close — an implementor would
  have to guess whether shipped `code`+`task_bundle` pipelines keep working,
  and guesses on a production execution path are how outages happen.
- **Rewrite ADR-032/033 in place to add the missing cross-references.**
  Rejected as the primary fix: both are authored, proposed documents under
  active review; a stacked amendment is this repository's own established
  pattern for exactly this situation (ADR-030 amended ADR-029 the same way,
  ADR-031 amended ADR-030 the same way) and keeps the review trail intact
  rather than editing text under someone else's name.
- **Do nothing until ADR-032/033 implementation starts.** Rejected: the
  bundle-format and protocol ambiguity is exactly the kind of thing that is
  cheap to resolve in a document today and expensive to discover mid-
  implementation, or worse, in a production incident after two silently
  incompatible paths both exist as code.

## Follow-ups

- Add the ADR-030 amendment ADR-031 already called for (bundle-scoped
  module loading) before `task` node execution is implemented.
- Add the `task-runtime-v1` / `task-bundle/v2` capability advertisement
  alongside the existing `task-bundles` capability, and a deploy-preflight
  fixture proving a server advertising only the latter rejects a `task`
  node.
- When an SDK ships `task` node emission, update its changelog to state
  explicitly that `@task(package="bundle")` continues emitting
  `task-bundle/1` against servers that have not yet advertised
  `task-runtime-v1`.
- Revisit Decision 2 with real measurements once both reuse strategies have
  running implementations, per the measurement discipline used for the
  spawn-vs-pool benchmark in ADR-029's own arc.
