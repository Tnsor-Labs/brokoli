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
