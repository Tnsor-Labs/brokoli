# ADR-018: Chunked node execution and memory-aware backpressure

**Status:** proposed — Decision 2 (admission control) implemented and
live-verified with an important honest limitation found in the process;
see "Update: implemented" below. Decision 1 (chunked node-to-node
hand-off) reconsidered — see the same section.
**Date:** 2026-08-13

## Update: implemented (2026-08-13)

Shipped as #176 (memory-aware admission control + the SQLArtifactStore
spill-support fix) and #178 (a follow-up closing a burst-admission race
found immediately by redeploying #176 and re-testing), released as
v0.10.23/v0.10.24.

**What actually shipped, vs. the original Decision:**

- **Decision 2 (memory-aware admission control)** shipped close to as
  designed: a worker checks real cgroup memory usage before claiming a
  new job, plus (#178) a 3-second settle delay since its own last
  admission, closing the specific race where a cgroup reading can't see
  a job that was just admitted but hasn't started allocating yet.
- **Decision 1 (chunked node-to-node hand-off via `DataStream`/
  `RowBatch`)** did *not* ship as originally scoped. Implementing #176
  found that `node_output_store.go`'s spill mechanism already streams
  its encode via `io.Pipe` for the default `LocalDiskArtifactStore` —
  the "whole `*DataSet` handed to `nodeOutputs.Put()` in one shot"
  framing in the original Context was accurate for the in-memory
  representation, but the *chunked handoff* this Decision proposed was
  largely already-existing infrastructure, not a gap. The real, newly
  found gap was narrower and different: `SQLArtifactStore` (the
  cross-pod artifact store instance-dispatch deployments swap in)
  didn't implement `BlobStoreProvider` at all, so spilling was silently
  disabled — every node's output stayed fully in memory regardless of
  size — in exactly the deployment shape most likely to run large
  pipelines across real worker pods. Fixed by giving it a local-disk
  blob store for spill scratch space. `engine/streaming.go`'s
  `DataStream`/`RowBatch` remain unwired into any node execution path,
  same as before this ADR — genuinely finishing that integration is
  still the "full streaming rewrite" this ADR's own Alternatives
  section already scoped as future work, not something #176 needed.

**What live re-verification found, and why it doesn't mean the fix
failed:** re-running the same 10-concurrent-run / 100k-row stress test
against the same 6×512Mi worker sizing after #178 shipped still showed
4 of 6 workers OOMKilled — the same raw count as before any of this
ADR's fixes existed. Diagnosed by hand-building a debug image with
per-decision logging and testing at both 1 and 6 replicas: the
admission logic (memory check + settle delay) is provably correct
*per pod* — traced directly from debug output, each worker's own
`sinceLast` timer and headroom check behave exactly as designed. What
it cannot do anything about is *cross-pod* aggregate demand: 6
independently-scheduled pods each honoring their own 3-second settle
delay have no coordination with each other, and pods that started
around the same rollout moment naturally have similarly-timed
admission windows — so six separate, individually-correct admission
decisions can still land within a few seconds of each other in
aggregate. Separately, and more fundamentally: a single 100k-row
pipeline's own peak memory (Go's `map[string]interface{}`-per-row
`DataRow` representation is not memory-cheap) is close enough to a
512Mi ceiling that even correctly-throttled admission cannot fully
prevent occasional overshoot once several jobs are already in flight
and still growing — a point-in-time cgroup reading cannot see memory
growth that hasn't happened yet, at any timescale, no matter how
tightly the check is re-run.

This does not regress anything the original stress-testing work
(#169/#171/#173) established: recovery still resolves every OOM'd run
to a clean terminal state with zero dangling node-runs, verified fresh
against this exact test. What #176/#178 add on top is a genuine,
verified reduction in *how easily* that OOM path is hit under a
simultaneous burst — not, and this ADR was not scoped to be, a
guarantee that admission control alone eliminates OOM under a
deliberately tight sizing that this session's own stress test picked
specifically to probe the limit. Full elimination needs one of: (a)
sizing worker memory to the actual workload — an operator/deployment
decision no amount of in-process cleverness substitutes for, (b) the
deferred full streaming rewrite that lowers a single run's own peak
memory, or (c) EE-ADR-008's elastic scaling, so a demand spike is
absorbed by *more replicas* rather than stacked onto a fixed-size
fleet — implemented in this same session but not live-tested together
with this ADR's fixes, since KEDA is not installed in the local k3s
test cluster this work was verified against.

## Context

Live stress-testing a real Kubernetes deployment (10 concurrent runs of a
100k-row pipeline against 6 worker pods, 512Mi memory limit each) found
that workers get OOMKilled under ordinary concurrent load, not pathological
input. Recovery now handles the resulting orphaned runs and node-runs
correctly (#169, #171, #173) — but that is cleanup after the fact. The
actual cause was never fixed: nothing bounds how much memory one pipeline
run can consume, and nothing tells the system "I'm out of headroom, stop
handing me more work" before a worker is killed.

### Why this happens

`common.DataSet` (`pkg/common/json_utils.go`) is a plain in-memory struct
— `Columns []string` and `Rows []DataRow`. Every node's full output is
materialized as one `*DataSet` before the next node runs
(`Runner.executeNode`/`runNodeLogic`, `engine/runner.go`). A 100k-row
`gen_orders` node holds all 100k rows in memory at once; `compute_totals`
then holds its own full output *and* implicitly still touches
`gen_orders`'s while it's being read; `aggregate_by_region` holds a third.
None of this is bounded by anything except how much memory the pod
happens to have.

There is already a partial answer to this — `nodeOutputs`
(`engine/node_output_store.go`) estimates a node's encoded size by
sampling `sizeEstimateSampleRows = 32` rows, and if the estimate crosses
`BROKOLI_SPILL_THRESHOLD_BYTES` (default 64 MiB), writes the dataset to
the artifact store instead of holding it inline, re-reading it on each
downstream consumer. This helps — but it only prevents *one node's fully
materialized output* from living in memory forever; it does nothing about
the peak while that output is *being produced*, and every downstream
consumer that reads it back still materializes the whole thing again in
one shot.

There is also already an unused answer to the deeper problem.
`engine/streaming.go` — `RowBatch`, `DataStream`, `StreamTransform`,
`StreamFilter`, `DefaultStreamConfig` — is a complete, well-designed
channel-based streaming primitive (bounded-buffer channel of row
batches, with `Collect()` to materialize when a node genuinely needs
everything at once, e.g. a join or sort). It is exercised only by
`engine/streaming_test.go`. No node execution path in `runner.go` or
`codenode.go` constructs or consumes a `DataStream`. It was built and
never wired in.

### Why this is a resource-management problem, not just a memory problem

Every concurrency knob in the engine today — `maxParallel` (4 nodes per
run, `runner.go:295`, hardcoded), `BROKOLI_MAX_CONCURRENT_RUNS` (4,
`engine.go:224`), the `workerSlots` channel gating how many jobs one
worker process runs at once (`cmd/serve.go:326-327`, sized directly from
`BROKOLI_MAX_CONCURRENT_RUNS`) — bounds *how many things run*, never *how
much memory they're allowed to use*. A deployment sized for four
concurrent lightweight pipelines will OOM on four concurrent heavy ones,
and there is no signal anywhere in the system that distinguishes those
two cases before the kernel's OOM killer does. This is the wrong
abstraction for "out of the box" behavior: an operator should not have to
predict their heaviest pipeline's memory footprint and hand-tune
`BROKOLI_MAX_CONCURRENT_RUNS` around it.

## Decision

Two changes, both building on infrastructure that already exists rather
than replacing it:

**1. Chunked execution between nodes, not full node rewrite.** Every
node *type*'s own function signature stays `(*common.DataSet) →
(*common.DataSet)` — `ExecuteCodeNode`, `ExecutePartitionTransform`,
`extensions.NodeExecutor` (the enterprise K8s/Docker executor contract)
all keep their current shape. This is deliberate: a full streaming
rewrite would break the external `NodeExecutor` interface, which
third-party executors already implement. Instead, the *Runner* changes
how it feeds data between nodes: when a node's estimated output size
exceeds a chunk threshold (reusing the sizing logic `nodeOutputs`
already has), the Runner drives that node's downstream consumers through
`engine/streaming.go`'s existing `DataStream`/`RowBatch` machinery
row-batch-at-a-time, instead of handing the whole `*DataSet` to
`nodeOutputs.Put()` in one shot. A node type that genuinely needs the
whole dataset at once (join, sort, dedup) calls `DataStream.Collect()`
— explicit, visible in that node's own code, not a silent full
materialization the Runner does on every node's behalf regardless of
whether it's needed.

**2. Memory-aware, self-throttling admission control, replacing the
static count-based one.** `workerSlots`'s channel-of-`struct{}` becomes
sized dynamically from real memory headroom (`runtime.MemStats` /
cgroup memory usage, checked against `BROKOLI_K8S_MEMORY_LIMIT` when
running under the enterprise k8s topology, or `GOMEMLIMIT` otherwise) —
not just a fixed count. When a worker is near its memory ceiling, it
simply stops pulling new work: `extensions.JobQueue.Dequeue()` (or the
local `ClaimPendingRun` poll loop) is skipped for one tick rather than
called unconditionally. This is backpressure in the literal sense —
push it back onto whatever's waiting — expressed through mechanisms
that already exist (a worker that doesn't dequeue from Redis Streams
leaves the job exactly where a genuinely busy worker would; nothing new
needs inventing at the queue layer for this half of it).

A run that can't be admitted anywhere stays in `pending`, visible as
`brokoli_runs_queue_waiting` (`api/observability.go:151`, already
exposed, currently unconsumed by anything) — this is the queue the
stress test's own zombie-node investigation kept surfacing the need
for. No new "pending jobs queue" component: the queue already exists
(every run starts life as a `pending` row); what's missing is admission
control smart enough to leave things there under real pressure instead
of accepting everything and letting the kernel decide who dies.

## Consequences

### Positive

- Peak memory for a single pipeline run becomes bounded by chunk size
  (a knob, not the dataset's full row count) — the direct fix for the
  stress-test OOMs, without requiring an operator to pre-size worker
  memory around their largest expected dataset.
- `DataStream`'s bounded channel buffer is itself a backpressure
  primitive for free: a slow downstream node's `Receive` not keeping up
  makes the upstream node's `Send` block, throttling production to
  consumption speed with zero new code.
- `brokoli_runs_queue_waiting` becomes a meaningful, load-bearing signal
  instead of a metric nobody reads — the natural input for an elastic
  scaler decision, deliberately kept out of this ADR's scope (see
  Deferred below).
- Every node type's own implementation is untouched; this is a Runner
  concern, not a per-node-type migration.

### Negative

- Chunking adds real complexity to the Runner's data-handoff path — two
  code paths (chunked vs. whole-dataset) instead of one, at least until
  enough node types have an opinion on their own chunk size to make the
  whole-dataset path rare.
- Nodes that need global visibility over their whole input (sort,
  dedup, most joins) gain nothing directly from chunking — they still
  materialize via `Collect()`. The spill-to-disk path remains the
  actual memory relief for those, unchanged by this ADR.
- Memory-aware admission control needs real tuning against actual
  cgroup/GOMEMLIMIT behavior in production — a wrong safety margin
  either starves throughput (too conservative) or doesn't prevent the
  OOM it exists to prevent (too aggressive). This needs a canary
  deployment before it's trusted as the default.

### Deferred

- Elastic worker *scaling* — growing/shrinking the worker fleet itself
  in response to `brokoli_runs_queue_waiting` — is out of scope here.
  This ADR makes the signal correct and load-bearing; reacting to it by
  changing replica counts is a Kubernetes-topology concern for the
  enterprise deployment layer to pick up separately.
- Streaming all the way through every node type (true per-row
  operators end to end) is explicitly not this decision — see
  Alternatives.
- A prioritized/fair queue (so one org's burst doesn't starve another's
  pending runs) isn't addressed — today's `pending` queue is FIFO by
  creation order with no notion of priority or tenancy fairness.

## Alternatives considered

- **Full streaming rewrite (every node a true streaming operator)** —
  rejected for now: touches every node type's implementation and the
  external `NodeExecutor` interface third-party executors already
  depend on, for a memory-floor improvement chunking mostly captures
  already. Worth revisiting once chunked execution's real-world
  numbers show where it still falls short.
- **Just lower `BROKOLI_SPILL_THRESHOLD_BYTES` and call it done** —
  rejected: spilling already-fully-materialized output to disk doesn't
  bound the *peak* while that output is being produced, which is where
  the stress test's OOMs actually happened (mid-node, not between
  nodes).
- **Static per-pipeline memory budgets set by the pipeline author** —
  rejected: this is exactly the "user must determine how many resources
  they need" failure mode the whole investigation was reacting against.
  Runtime-measured backpressure means nobody has to guess right in
  advance.

## Follow-ups

Superseded by the "Update: implemented" section above where it
conflicts — kept here for what's still genuinely open:

- The full streaming rewrite (wiring `engine/streaming.go`'s
  `DataStream`/`RowBatch` into an actual node execution path, or an
  equivalent) is still not done — it's the only remaining lever that
  reduces a *single run's own* peak memory, which admission control
  structurally cannot do regardless of how it's tuned. Worth
  revisiting with real numbers from production admission-control usage
  informing where it would help most, per this ADR's original
  Alternatives section.
- Live-test admission control together with EE-ADR-008's KEDA-based
  elastic scaling — not done in this session because KEDA isn't
  installed in the local k3s cluster this work was verified against.
  This is the most likely near-term lever to actually close the
  remaining gap for this specific test's sizing, since it grows
  aggregate capacity to meet aggregate demand rather than trying to
  make a fixed-size fleet absorb an arbitrary burst.
- `memoryBackpressureSafetyMarginFraction`/`memoryBackpressureSettleDelay`
  need real tuning against production traffic patterns, not just this
  one synthetic worst-case burst test — they were picked as reasonable
  defaults, not measured ones.
- ~~Wire `runtime.MemStats`/cgroup-aware sizing into `workerSlots`~~ —
  done (#176), see Update.
- ~~Re-run the stress test and confirm no OOM kills~~ — done; found
  that "no OOM kills" was the wrong bar for what admission control
  alone can deliver at this sizing — see Update for the honest result
  (zero stuck-forever runs / zero zombies, which *is* what shipped,
  still holds).
