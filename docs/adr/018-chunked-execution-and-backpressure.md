# ADR-018: Chunked node execution and memory-aware backpressure

**Status:** proposed
**Date:** 2026-08-13

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

- Implement chunked hand-off in the Runner behind
  `BROKOLI_CHUNK_THRESHOLD_BYTES` (default off — mirrors how
  `BROKOLI_SPILL_THRESHOLD_BYTES` shipped — until proven safe by default).
- Wire `runtime.MemStats`/cgroup-aware sizing into `workerSlots`.
- Re-run the same 10-concurrent-run / 100k-row stress test against this
  change with the same 512Mi worker limit and confirm: no OOM kills, or
  if capacity is genuinely insufficient, runs queue visibly instead of
  being admitted and killed.
- A companion enterprise-layer ADR for elastic worker scaling, consuming
  `brokoli_runs_queue_waiting` as its primary signal.
