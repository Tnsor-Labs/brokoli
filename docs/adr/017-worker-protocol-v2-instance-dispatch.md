# ADR-017: Worker protocol v2 — instance-level dispatch, lease, and fencing

**Status:** proposed
**Date:** 2026-08-12

## Context

ADR-006 deferred unifying the worker WebSocket stack with SODP and named
its own follow-up: *"Add a worker-protocol-v2 ADR when we have a clear
path to unifying the two stacks."* That follow-up is about transport. This
ADR is about something that has since become more urgent and is largely
independent of transport: **the unit of work a worker claims.**

### What "claim a job" means today

Every worker-facing dispatch path in Brokoli — the enterprise work-pool
platform's WebSocket push and HTTP long-poll paths, and the enterprise
Redis-backed `extensions.JobQueue` implementation used by
`cmd/serve.go`'s `RunMode=="worker"` — claims a **whole pipeline run**,
not a node, page, or expansion item. A worker that picks up a job runs
`engine.NewEngine(...).RunPipeline(...)` (or equivalent) start to finish
inside its own process. If that worker dies mid-run, the *entire run* is
what gets reclaimed and retried — not the one node, page, or item that was
actually in flight when it died. Everything that already succeeded before
the crash reruns too.

This was a reasonable starting point and is not a bug — it is exactly the
scope #90 M1/M2 set out to outgrow. It stops being sufficient once a
single logical node can fan out into many independently retriable
physical instances (pagination pages, `.expand()` items), which is the
entire point of ADR-015's physical plan.

### What already exists to build on

Nothing here starts from zero:

- **`store.ExecutionAttemptStore`** (Tnsor-Labs/brokoli#7) is a full
  claim/lease/fencing contract — `ClaimAttempt`, `RenewLease`,
  `AckAttempt`, `CompleteAttempt`, `FailAttempt`, keyed by
  `(run_id, node_id, attempt)` with a fencing generation bumped on every
  claim. #144 wired this into node-level dispatch, but **only for the
  engine's own in-process claim of its own just-created attempt** —
  nothing else can claim node-level work yet. That gap is exactly this
  ADR's subject.
- **`models.PhysicalPlan`** (ADR-015 M2) already classifies a node's work
  unit as `WorkUnitSingle`, `WorkUnitPagination`, or `WorkUnitExpansion`,
  each with a deterministic `InstanceKeyTemplate`, a `RetryScope`, and a
  `MaxConcurrency`. This is the identity scheme a v2 protocol should
  dispatch against — it already exists, is already tested, and does not
  need reinventing.
- **`store.PhysicalInstanceStore`** persists durable instance rows for
  `single` and `expansion` work units today (`pagination` is not yet
  wired — #143). This is the row a remote claim needs to reference.
- **`extensions.RunJob`** (the `JobQueue` interface's dispatch payload)
  already carries `NodeID`, `Attempt`, `IdempotencyKey`, and
  `FencingGeneration` fields — all currently always zero-valued, with
  `NodeID`'s doc comment stating plainly: *"empty for pipeline-level jobs
  (today's only kind)."* Whoever defined this interface already
  anticipated instance-level dispatch. Nothing about the `JobQueue`
  interface itself needs to change shape to carry an instance identity —
  only its caller (today, only whole-run enqueue) and its enterprise
  implementation need to.
- **A worker→server write-forwarding revision already shipped once.**
  Alerts, batched run events, and pagination checkpoint state were all
  previously silently dropped when a run executed on a remote worker.
  These were closed as one coordinated protocol revision (job-scoped
  identity enforced server-side across all three — the server derives
  identity from the job/pool, never trusts the payload), not three
  one-offs. That revision's endpoint and batching shapes are the closest
  prior art for how this ADR's write path should look, and the same
  "server derives identity, never trusts the payload" rule applies
  directly to instance claims.

### What does not carry over cleanly

The enterprise work-pool platform's existing worker-liveness model is
**worker-level**, not job-level: a background sweep marks a worker
offline once its heartbeat goes stale past a threshold, and reclaims
*everything that worker held* in one pass. There is no per-job lease
expiry independent of the owning worker's own heartbeat.

`store.ExecutionAttemptStore`'s model is the opposite: **every attempt
has its own lease**, renewed independently (#144 renews on a
15s-lease/5s-tick cadence scoped to that one attempt's own execution
context). A worker could be alive and heartbeating fine while one
specific instance it's running has gone stale for an unrelated reason
(stuck subprocess, deadlock), and today's worker-level model would never
notice.

These two liveness models need to be reconciled, not have one deleted in
favor of the other — see Decision.

### RFC's own sketch of a richer lease

`docs/rfcs/brokoli-next-architecture-v2.md` §18.5 ("Worker lease model")
already sketches a lease payload broader than what
`models.ExecutionAttempt` carries today: connector/runtime, input
references, config, checkpoint, resource policy, a progress endpoint, and
a cancellation token. `ExecutionAttempt` today only has the claim/lease
bookkeeping fields (status, claimed_by, lease_expires_at,
fencing_generation, idempotency_key) — it does not carry the actual work
description. That has been fine because the only claimant has been the
engine's own in-process dispatch, which already has the full node/pipeline
in memory. A remote worker claiming an attempt over the wire needs the
work description delivered *with* the claim, not assumed.

### Why this needs enterprise co-design, not a unilateral core decision

Every concrete claimant today — the work-pool platform's worker pools,
its Redis-backed `JobQueue` implementation, the standalone worker client
— lives in the enterprise codebase. Core can propose the shape of the
identity and lease contract (it already has, incrementally, via
`ExecutionAttemptStore` and `PhysicalPlan`), but the actual dispatch
protocol change — what a worker asks for, what it gets back, how it
reports partial progress, how reclaim-on-death behaves at instance
granularity — has to be designed against real constraints only visible
from that side: existing pool capacity/concurrency accounting, the
existing worker-offline sweep, and the WebSocket-vs-poll dual transport.
This document proposes a direction and enumerates the open questions;
it is not final until reviewed against those constraints.

## Decision

Move the unit of worker claim from **whole pipeline run** to **physical
instance** (`(run_id, node_id, attempt, instance_key)`, matching
`models.PhysicalPlan`'s existing identity scheme), while keeping
whole-pipeline dispatch working as a migration path, not deleting it
outright in the same change.

Concretely, in dependency order:

1. **Extend `models.ExecutionAttempt` (or a new, adjacent model — open
   question below) to carry an `InstanceKey`** alongside its existing
   `(run_id, node_id, attempt)` key, so a `WorkUnitPagination` page or
   `WorkUnitExpansion` item can be claimed, leased, and fenced
   independently — the same contract #144 already gave whole-node
   attempts, extended one level deeper. This is a core-side, additive
   change; #143 and the expansion per-instance-retry work already
   established the identity scheme this needs (`deriveInstanceKey`,
   `InstanceKeyTemplate`).
2. **Give the claim payload a work-description envelope**, per the RFC
   §18.5 sketch: what to run (node config, script/connector reference),
   what to run it against (resolved input references — this is where
   ADR-012's artifact/dataset plane and this ADR meet), and where to send
   checkpoints/progress/cancellation. `extensions.RunJob`'s existing
   dormant `NodeID`/`Attempt`/`IdempotencyKey`/`FencingGeneration` fields
   are the starting shape; they need company, not replacement.
3. **Reconcile the two liveness models** rather than picking one:
   worker-level heartbeat stays the cheap, coarse, already-working "is
   this process even alive" signal; every instance-level write (claim,
   heartbeat, complete, fail) is fencing-checked exactly like #144's
   in-process version already is, so a worker that a heartbeat sweep
   incorrectly reclaimed and that later wakes up cannot corrupt state
   with a stale write — its fencing generation is already gone. Neither
   layer has to be perfect alone; together they give both fast practical
   reclaim and a hard safety net.
4. **Migration path, not a flag day.** A worker (or `JobQueue`
   implementation) that only understands whole-pipeline claims keeps
   working exactly as today — instance-level claiming is additive
   capability, not a breaking protocol version bump forced on every
   deployment simultaneously. `/api/capabilities`-style negotiation
   (already the pattern for IR version and runtime-class support) is the
   natural fit for a worker to advertise which claim granularity it
   understands.

## Open questions for enterprise co-design

These are genuinely open — this ADR does not answer them unilaterally:

- Does the work-pool platform's job model gain new columns
  (`node_id`/`attempt`/`instance_key`) on the existing job table, or is
  instance-level dispatch a parallel, separate table/concept alongside
  today's whole-pipeline jobs? The existing job model's capacity/
  concurrency accounting (`active_count`, `max_concurrency`) is
  worker-scoped, not job-scoped — does that stay true when one worker
  might hold many concurrent instance claims from the same run?
- Does the Redis-backed `JobQueue` implementation and the WebSocket/
  poll-based work-pool path converge on one claim model, or do they stay
  two implementations of the same `JobQueue`/instance-claim interface,
  as they already are two implementations of today's whole-pipeline
  dispatch?
- What is the actual reclaim latency budget once liveness is
  reconciled (Decision point 3)? #144 picked a 15s lease / 5s renewal
  for in-process node attempts, mirroring the existing scheduler-
  leader-election tradeoff — does a real network hop (worker to server,
  not in-process) change that budget?
- ~~Where does resolved-input-reference delivery actually live~~ —
  answered for the input half: `extensions.RunJob.WorkOrder`
  (`InstanceWorkOrder`, #150) inlines a dynamic-expansion item's small
  input row directly in the claim payload. The seam with ADR-012 remains
  for large inputs and is now symmetric with output delivery — see the
  2026-08-12 Update below.
- Does this land alongside, or separately from, ADR-006's still-open
  SODP-unification question? This ADR takes no position on transport
  (WebSocket vs. long-poll vs. SODP) — it is entirely about dispatch
  granularity, which is orthogonal. Answering both in one change is
  probably too much at once; recommend keeping them decoupled unless
  co-design surfaces a reason they're not.

## Consequences

### Positive

- A lost worker no longer costs an entire run's already-successful work —
  only the in-flight instance(s) it held, matching the guarantee #144 and
  the expansion-retry work already give in-process.
- Multiple workers can share one run's fan-out (many pagination pages or
  expansion items dispatched concurrently across a pool), not just many
  runs sharing a pool.
- The fencing-generation safety net makes the reconciled liveness model
  strictly safer than either layer alone, without having to fully trust
  heartbeat timing for correctness.

### Negative

- Real, non-trivial protocol surface growth on the side that has already
  broken from interface growth twice in two days during M2 (per the
  standing enterprise tracking issue for this roadmap). Needs the
  composed-store-capability-interface pattern core already adopted for
  `ExecutionAttemptStore`/`PhysicalInstanceStore` to keep applying, so
  this doesn't force a matching implementation obligation the moment the
  interface grows.
- Two liveness models running concurrently (worker heartbeat + per-
  instance lease) is more moving parts than either alone, and the
  interaction between them is exactly the kind of thing that needs
  adversarial testing (a worker whose heartbeat is healthy but whose one
  instance is actually stuck; a worker reclaimed by heartbeat sweep whose
  in-flight instance writes land late) before this is trusted in
  production.
- Whole-pipeline dispatch has to keep working throughout the migration —
  this is strictly additive complexity until it's eventually retired, not
  a swap.

### Deferred

- The actual reclaim-on-lost-worker crash-harness proof (#90 M3's stated
  acceptance gate: a worker killed mid-instance, fenced out, its instance
  retried by a *different* worker without disturbing successful
  siblings) — that's implementation against this decision, not part of
  scoping it.
- ADR-006's SODP-unification question (see open questions above).
- Wiring `WorkUnitPagination` into actual dispatch (#143) and durable
  per-item output storage for expansion instances (noted as a gap in the
  expansion-retry work) both depend on this ADR landing first — they are
  the concrete consumers, not prerequisites.

## Alternatives considered

- **Keep whole-pipeline dispatch, add finer-grained retry only inside
  the worker process (extend what #144/#145 already did, but never let a
  *different* worker pick up a partial run).** Rejected as the long-term
  answer: it doesn't solve the actual acceptance gate (cross-worker
  reclaim), and pagination/expansion fan-out inside one worker process
  doesn't get you real concurrency across a pool, only inside one
  process's own goroutines — which is what already exists today and is
  explicitly named as a scope limitation in both the pagination and
  expansion work.
- **Replace worker-level heartbeat entirely with per-instance leases.**
  Rejected for now: worker-level offline detection is cheap, already
  built, already correct for its own scope, and losing it entirely means
  losing a fast, simple "is this process gone" signal in favor of
  inferring it from N independent instance leases going stale one at a
  time — worse UX for operators watching pool health, not obviously
  better for correctness once fencing exists anyway.
- **A completely new job/claim table separate from
  `ExecutionAttemptStore`, purpose-built for remote dispatch.** Not
  rejected outright — this is one shape of the first open question above
  — but not chosen by default either, since it would mean two parallel
  claim/lease/fencing implementations (in-process via
  `ExecutionAttemptStore`, remote via the new table) that need to agree
  on identity and generation semantics without sharing code. Extending
  the existing contract is the default lean; co-design may still land on
  a separate table for good reasons (e.g. different retention/volume
  characteristics) — that's exactly the kind of thing this section exists
  to flag rather than decide.

## Follow-ups

- Enterprise-side review of this document against the open questions
  above, per the standing "participate in the worker-protocol-v2 ADR
  before core M3 implementation starts" commitment.
- Once co-design resolves the open questions, split implementation into
  the same kind of small, independently-mergeable slices #144/#145 used
  rather than one large change: (1) `InstanceKey` on the claim contract,
  (2) claim-payload work-description envelope, (3) liveness
  reconciliation, (4) migration/capability negotiation, (5) the
  crash-harness acceptance-gate proof last, once every prior piece is in
  place to make it meaningful.
- #143 (paginated `source_api` dispatch) and durable per-item expansion
  output storage both resume once this ADR's identity/claim contract is
  settled.

## Update — 2026-08-12: output-reference delivery design

Answers the symmetric half of the open question above — not "how does a
remote claimant get its input" (settled by #150's `WorkOrder`) but "how
does a completed instance's *output* get back to whatever is waiting on
it." Grounded in two things that already exist rather than a new
mechanism:

**ADR-012 already named this exact gap.** Its own Negative consequences
section says local-disk-only `ArtifactStore` "doesn't solve the problem
for a real multi-replica or Kubernetes deployment — a worker on pod B
can't read a file spilled by pod A." That is precisely the cross-worker
output-delivery problem, flagged and deferred before anything needed it.
This update is that gap materializing for real, not a new one.

**ee#55's worker-forwarding endpoints are the proven shape for the
transport half.** Alerts, run events, and pagination checkpoints already
cross the worker→server boundary the same way: `WorkerTokenAuth` +
job-scoped lookup, identity forced from the job row and never trusted
from the payload, writing into the core engine's own store
(`eng.PaginationCheckpointStore`, by direct analogy `eng.ArtifactStore`).
Output-result delivery reuses that shape rather than inventing a fourth
variant of it.

**Design:**

- **Push, not pull.** The worker delivers the result; the server/engine
  never reaches into a worker — consistent with every existing
  worker↔server interaction (workers connect outward only).
- **`ArtifactStore` gains an `instanceKey` dimension** —
  `WriteArtifact`/`ReadArtifact` take an `instanceKey string` parameter,
  empty for today's whole-node case. Same additive-key-widening move
  already made for `execution_attempts` (#147) and `WorkPoolJob` (#79):
  zero behavior change for every existing caller.
- **`ExecutionAttempt` stays control-plane only** — no result payload
  added to it. The data channel is `ArtifactStore`; the is-it-done
  channel stays `GetExecutionAttempt`, unchanged. Keeps the same
  separation of concerns `ExpansionInstance` vs. `ExecutionAttempt`
  already established: one table, one job.
- **A new worker→server endpoint modeled directly on the checkpoint
  handlers**: job-scoped, identity from the job row, writes through the
  engine's own (now instance-aware) `ArtifactStore`, then calls the
  already-existing `CompleteAttempt`/`FailAttempt`.
- **Reuses ADR-012's existing inline-vs-spill threshold** rather than a
  second one — a small result (the common case) rides inline in the
  same POST; a large one spills through the artifact store first and the
  POST carries a reference.
- **The waiting side polls `GetExecutionAttempt`** (already built and
  tested) until terminal, bounded by the same `timeoutSec`-derived
  deadline the lease already uses. On timeout it calls `FailAttempt`
  itself, fencing-checked — a late result from an actually-still-alive
  worker is safely rejected on arrival, the same safety net every other
  settlement path in this ADR already relies on.
- **Cloud/shared artifact backend stays exactly as deferred as ADR-012
  already left it** — this design doesn't newly introduce that gap, it
  only needs the *server* process to be the one persisting the pushed
  result, which it already is here.

**Rejected alternative:** carrying the result on `ExecutionAttempt`
itself (a `ResultRef` column). Rejected for the same reason
`ExpansionInstance` and `ExecutionAttempt` were kept as two tables
instead of one — overloading a claim/lease/fencing record with a second,
unrelated concern (the data plane) is the exact anti-pattern that
decision already argued against.

Implementation lands as the same kind of independently-mergeable slices
as everything else in this ADR: (1) `ArtifactStore` key-widening (core),
(2) the worker-side result endpoint (enterprise), (3) the engine's
wait-and-fetch loop.

## Update — 2026-08-12: dispatcher-side wait-and-fetch loop design

Slices 1 and 2 above are merged (core#152, ee#81). This is the design for
slice 3, scoped narrowly: **the dispatcher side only** — the engine
deciding to enqueue an instance and wait for its result. Actually
executing a `WorkOrder` on a worker (`cmd/serve.go`'s Redis-worker loop
unconditionally treats every dequeued job as "run this whole pipeline"
today — confirmed by reading it, there is no `job.WorkOrder != nil`
branch) is real, separate work this design depends on but does not
include, scoped as its own follow-on slice rather than folded in here.

**Who claims the attempt stays exactly as it already is.** The dispatcher
claims its own just-created `ExecutionAttempt` row itself, synchronously,
before dispatching — identical to #144/#149. The only change is what
happens after `AckAttempt`: instead of calling `ExecuteCodeNode`
in-process, it builds the `InstanceWorkOrder` (#150) and enqueues a
`RunJob` carrying it plus the `FencingGeneration` its own claim already
produced, then waits. This is precisely why the instance-result endpoint
(ee#81) was already designed to read `FencingGeneration` from the job row
rather than have the worker claim anything itself: the worker never
claims, it executes on behalf of the dispatcher's existing claim and
reports back holding the same token. The two pieces were designed to
click together without either changing.

**The wait polls `GetExecutionAttempt`** — already built, cheap,
DB-native — rather than inventing a new signal; `JobQueue` is push/pull,
not request/response, so there is nothing to block on there. *(Landed
as an immediate check plus its own `remoteInstanceStatusPollInterval`,
1s by default — not `store.DefaultRenewInterval` as first sketched here.
The two cadences are legitimately different concerns: lease renewal can
stay coarse, but coupling user-facing result latency to it meant a
worker responding in milliseconds still wasn't noticed for a full lease
interval — caught by `TestPipeline_ExpandRemoteDispatch_Succeeds` timing
out in exactly that way.)*

**A renewal goroutine is now required**, unlike #149's synchronous
same-process case: the wait is no longer bounded by one blocking call, so
the lease needs active keep-alive for however long dispatch + queueing +
remote execution actually takes. Reuses the exact renewal pattern
`runner.go` already has for node-level attempts, applied to the waiting
call instead of the executing one.

**Deadline = `timeoutSec` + a dispatch/queueing margin** (`+2 minutes`
as a starting default — an explicit tunable, not a permanent constant),
larger than #149's synchronous case since network and queue wait now sit
on top of actual execution time. On timeout, the dispatcher calls
`FailAttempt` itself, fencing-checked — a result that arrives late loses
the settlement race honestly (fencing mismatch, not a silent overwrite),
the same safety net every other settlement path in this ADR relies on.
On success, `ReadArtifact(runID, nodeID, instanceKey)` — the exact call
core#152 was built for — retrieves what the worker's
`POST .../instance-result` wrote.

**The opt-in gate is a new, separate field: `Engine.InstanceJobQueue`.**
Not a reuse of the existing `Engine.JobQueue` — a deployment already
running distributed whole-pipeline dispatch today must not silently start
attempting instance-level remote dispatch the moment this ships. Nil by
default everywhere, including existing distributed deployments; nothing
sets it in this slice, so `runCodeExpansion` keeps taking the existing
local-execution path unconditionally. Zero behavior change on merge,
same as every prior slice in this ADR.

**Remaining after this slice:** the worker-side execution of a
`WorkOrder` (teaching a worker to recognize `job.WorkOrder != nil` and
run the script instead of a whole pipeline) — nothing can dispatch
remotely end-to-end without it, and it needs its own design pass, not a
continuation of momentum from this one.

## Update — 2026-08-12: worker-side execution, end-to-end verified

Slice 3's dispatcher half landed as core#155. This closes the loop:
worker-side execution of a `WorkOrder` (core#156), the piece the previous
update explicitly scoped out. `dispatchExpansionInstanceRemotely` →
`ExecuteInstanceJob` → `dispatchExpansionInstanceRemotely` picking the
result back up is now real and covered by a genuine end-to-end test
(`TestPipeline_ExpandRemoteDispatch_TrueEndToEnd`): a real dispatch
enqueues onto a real (channel-backed, not simulated) `JobQueue`, a real
worker loop dequeues and actually runs the script — no canned response —
and settles through the same store-level primitives the enterprise
result endpoint uses, and the dispatcher's own wait-and-fetch loop reads
the real result back out through `ArtifactStore`.

**Two shared primitives, not one.** `ExecuteInstanceWorkOrder` just runs
a `WorkOrder`'s script (only `NodeTypeCode` today — anything else is a
named, explicit error, so a future `WorkOrder` producer for another node
type finds out immediately rather than getting silently mishandled).
`ExecuteInstanceJob` wraps it with settlement (`CompleteAttempt`/
`FailAttempt`, `ArtifactStore.WriteArtifact`) for a worker that shares
the *same store* as the dispatcher — `cmd/serve.go`'s Redis-`JobQueue`
worker loop is the first caller. An enterprise WorkPool worker that does
*not* share the store reaches `ExecuteInstanceWorkOrder` directly and
reports back over the instance-result HTTP endpoint (ee#81) instead of
calling `ExecuteInstanceJob`'s settlement half — same execution
primitive, two different settlement transports, exactly as this ADR's
two-tables/two-transports shape elsewhere already anticipated.

**A script failure settles the attempt as failed but is not itself a
job-queue-level failure.** The job's own task — durably record this
instance's outcome — succeeded even when the script didn't; this mirrors
the existing whole-pipeline convention of `Ack`ing a job whose run
failed. Only an infrastructure failure on the worker's own side (can't
reach the store) is surfaced as a job failure, so the caller can `Fail`
the job for redelivery — safe to retry with the same `FencingGeneration`
because the underlying lease keeps getting renewed independently by the
dispatcher's own goroutine throughout, so it hasn't gone stale.

**`cmd/serve.go` needed its own identity check, not a relaxation of the
existing one.** The worker loop's pre-existing validation
(`job.ID == "" || job.RunID == "" || job.ID != job.RunID`) is correct for
whole-pipeline jobs but would incorrectly reject every `WorkOrder` job,
whose `job.ID` is a fresh UUID rather than equal to `RunID`. Fixed with a
distinct `job.WorkOrder != nil` branch ahead of that check, with its own
validation (`job.ID`, `job.RunID`, `job.NodeID`, `job.InstanceKey` all
required), rather than loosening the whole-pipeline check to tolerate a
case it was never meant to describe.

**A pre-existing race in `executeNode`'s own cancellation handling
surfaced during testing, not introduced by this slice.** `executeNode`'s
outer `select` races its `attemptCtx.Done()` case against the goroutine
actually doing the work; on cancellation that outer select can return
"pipeline cancelled" for the node *before* the inner
`dispatchExpansionInstanceRemotely` goroutine reaches its own
`waitCtx.Done()` case and calls `FailAttempt` — leaving that goroutine to
settle the attempt after `RunPipeline` has already returned. This was
always true of `executeNode`, remote dispatch just made it observable:
the local-execution path has no analogous "goroutine still running
after the caller returns" shape. Not fixed here — settlement is still
correct, just not synchronous with the run's own terminal status — but
the corresponding test
(`TestPipeline_ExpandRemoteDispatch_CancellationSettlesAsFailed`) had to
be written to poll for the attempt reaching terminal status instead of
asserting immediately, and that eventually-consistent framing is worth
carrying forward into any future work that touches this cancellation
path.

**ADR-017's remote instance dispatch mechanism is now feature-complete
and verified end-to-end** for the core (self-hosted Redis-`JobQueue`)
transport: dispatch → real worker execution → real result delivery →
dispatcher picks it up, opt-in only via `Engine.InstanceJobQueue`, zero
behavior change for any deployment that doesn't set it. The enterprise
WorkPool transport (ee#81's HTTP result endpoint, a worker that does not
share the dispatcher's store) is designed to compose with the same
`ExecuteInstanceWorkOrder` primitive but is tracked and verified
separately in the enterprise repo.

## Update — 2026-08-12: two real bugs found deploying this for real, both fixed

"Verified end-to-end" above meant verified against Go tests — including
a genuine channel-backed `JobQueue` and a real worker-loop goroutine
(`TestPipeline_ExpandRemoteDispatch_TrueEndToEnd`), but still all in one
process, sharing one `store.Store` and one `ArtifactStore` instance. That
is a materially different thing from a real distributed deployment, and
running this in one — separate `api`/`scheduler`/`worker` pods in
Kubernetes, the enterprise binary, its real Redis-backed `JobQueue` — hit
two bugs neither the unit tests nor the in-process end-to-end test could
ever have caught, because neither exercises what only a real transport
and real separate filesystems introduce:

1. **`Engine.InstanceJobQueue` was never actually wired up** in either
   binary's real `cmd/serve.go` — #155/#156 built the whole mechanism,
   but nothing ever set the field outside of tests, so
   `dispatchExpansionInstanceRemotely` was dead code in production.
   Fixed in #158 (`BROKOLI_INSTANCE_DISPATCH=1`, opt-in, same reasoning
   as the field itself).
2. **The enterprise `RedisJobQueue` overwrote `RunJob.Attempt`** with its
   own delivery-count on every dequeue — a field collision invisible to
   any test using a simplified fake queue, since only a *real* queue
   transport has its own delivery-count bookkeeping to (wrongly) fold
   into the same field. Every remote instance dispatch failed to settle,
   retried, and failed identically until the node's own timeout fired.
   Fixed by giving the queue-transport concept its own field,
   `RunJob.DeliveryCount` (core#159, brokoli-ee#82).
3. **`ArtifactStore` defaults to local disk, invisible across pods** —
   filed as core#161, not yet fixed. The worker's `WriteArtifact` and the
   dispatcher's later `ReadArtifact` land on two different filesystems in
   any real multi-pod deployment, so the settled-completed instance's
   result can never be read back. Worked around locally with a shared
   hostPath volume (proven to work end-to-end once mounted at the same
   path in every pod) — viable for a single-node deployment, not a
   general fix. A real fix needs a network/object-store-backed
   `ArtifactStore`, which is exactly the gap this ADR's own
   2026-08-12 output-reference delivery update already named and
   deferred, on an assumption (the process persisting a pushed result is
   also the one reading it back) that doesn't hold for this transport.

With 1 and 2 fixed and 3 worked around by a shared volume, the mechanism
was proven genuinely working end-to-end in Kubernetes: a real pipeline
dispatched three dynamic-expansion instances, all three executed on
worker pods distinct from the dispatcher pod (confirmed by each
instance's result carrying the executing pod's own hostname), and the
run completed successfully. The general lesson, worth stating plainly:
an in-process integration test proves the *logic* is correct; it does
not prove the *deployment* works, whenever real transports and real
filesystem boundaries are involved. Both are worth having, but they
prove different things, and this ADR now has both.
