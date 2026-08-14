# ADR-019: Execution segments, streaming capabilities, and reference-passing dataflow

**Status:** accepted — Milestone 1 implemented (#190, v0.10.28), with
the engagement threshold corrected after live measurement (#192,
v0.10.29); see "Update: measured" below.
**Date:** 2026-08-13

## Update: measured (2026-08-14)

Milestone 1 shipped in two steps, and the live measurements are the
story:

- **v0.10.28 (#190)** implemented reference-passing, gated on the
  spill threshold — and the single-run measurement came back at
  **291 MiB against the 284 MiB baseline: statistically nothing.**
  Streaming never engaged: the benchmark's datasets encode to
  ~15–20 MiB, under the 64 MiB spill threshold, while costing 5–6x
  that as in-memory map rows. Spilling parks memory already paid;
  streaming prevents it being paid — the engagement floor had to be
  its own, much lower knob.
- **v0.10.29 (#192)** added `BROKOLI_STREAM_THRESHOLD_BYTES` (default
  8 MiB encoded). Re-measured, same methodology, same pipeline:
  **86 MiB peak, settling to 38 MiB after the run** — versus 284 MiB
  peak and ~283 MiB retained before. A 3.3x collapse. Engagement
  confirmed from the run's own logs ("Staged 100000 row(s) to the
  script by reference (never materialized in the engine)"), results
  byte-correct, and the resume-artifact contract intact.
- The full 10-concurrent-run burst on the same 512 MiB pods:
  **10/10 success, zero OOM kills, zero zombies, 17 seconds
  wall-clock** — down from 29.6s in the pre-streaming zero-OOM
  configuration, and from minutes-with-casualties at the start of
  this investigation.
- The residual 86 MiB peak is dominated by the Python subprocess
  materializing its `rows`/`out` lists — exactly the boundary this
  ADR assigned to Milestone 1.5, now the next lever. The
  one-run-per-512Mi concurrency divisor (ADR-018) is now measurably
  over-conservative for streamed workloads and should be re-measured,
  per this ADR's own follow-up list.

## Update: Milestone 1.5 measured (2026-08-14, v0.10.30)

Milestone 1.5 shipped (#195): lazy re-iterable `rows`, the
`emit`/`begin_emit` idiom, and generator outputs in the code-node
harness — full backward compatibility (len/indexing transparently
materialize; every pre-existing test unchanged), with `begin_emit`
added after the tests caught that a zero-match filter using bare
`emit` would have fallen back to passthrough and output its entire
input.

Measuring it forced a metrics correction worth recording: every
earlier number in these updates sampled cgroup `memory.current`,
which includes page cache — and the reference-passing path is
I/O-heavy by design, so its own temp files and blobs inflate that
counter with perfectly reclaimable cache the kernel drops under
pressure instead of OOM-killing anything. Coarse 2-second sampling
also missed short transients both ways. The number that actually
maps to OOM pressure is the cgroup's **anon** memory, densely
sampled. On that metric, cold pods, same benchmark:

- **Unchanged legacy scripts: 100 MiB anon peak** — entirely the
  Python `out` lists; the lazy `rows` input side is already free.
- **The same pipeline in the emit/generator idiom: 7 MiB anon peak.**
  The full 100k-row, three-node pipeline runs in seven megabytes of
  anonymous memory, end to end, results identical.

Against the ~284 MiB this investigation started from, that is a ~40x
reduction for streaming-idiom pipelines and ~3x for completely
untouched ones. ADR-018's one-run-per-512Mi divisor is now deeply
conservative for streamed workloads; re-measuring it (and possibly
deriving it from observed per-run anon peaks rather than a constant)
is the natural next step, alongside Milestone 2.

## Update: Milestone 2 (2026-08-14, v0.10.31)

Milestone 2 landed re-scoped, and the re-scoping is the finding:

- **Streaming hash aggregation shipped.** An `aggregate` rule was a
  hard barrier — one anywhere in a transform forced the whole node to
  materialize. Now the rule list compiles to a plan: row-local prefix
  streams per batch, the aggregate folds batches into per-group
  accumulator state (small by construction), and suffix rules —
  anything, including sort — run on the grouped output. Incremental
  accumulators replicate the batch implementation's exact semantics,
  including its edges (count counts all rows regardless of column
  values; min/max over zero accepted values return 0.0; first-seen
  group ordering; alias defaults; the agg_fields/aggregations compat
  alias), held to equivalence by test across all of them. The common
  ETL shape — filter/derive into a group-by, then sort the totals —
  now streams end to end.
- **Fan-out replay needed no new machinery** — it fell out of
  Milestone 1's design: every consumer of a ref output opens the blob
  independently, so multiple streamable consumers replay the same
  output with no shared cursor and no interference. Now pinned by a
  dedicated test rather than assumed.
- **Concurrent cross-node stages are deferred, deliberately, with a
  reason worth recording.** True time-overlap between code nodes
  requires the downstream subprocess to consume its input while the
  upstream still produces it — a FIFO, not a file. But Milestone
  1.5's compatibility contract is that `rows` is RE-ITERABLE and that
  `len(rows)`/indexing transparently materialize — both require a
  complete, seekable file; against a FIFO, a second iteration or a
  `len()` call would deadlock. The contract that made lazy rows
  safely adoptable by every existing script is exactly the contract
  that forbids pipelined subprocess input. Breaking it silently is
  unacceptable; the honest path is an explicit per-script opt-in
  (a declared strict-streaming capability that trades away
  re-iteration and len() for FIFO input), which is real protocol
  design and belongs to a future milestone if the latency win ever
  justifies it. Meanwhile the disk-pipelined form is already
  memory-optimal — at ~7 MiB anon for streaming-idiom pipelines, the
  remaining sequential-vs-overlapped latency difference on the
  benchmark is a few seconds, and nothing currently hurts for it.

## Context

ADR-018's whole arc — memory-aware admission control, GOMEMLIMIT,
memory-derived concurrency, elastic scaling — managed one unexamined
constant: what a single run *costs*. That cost was finally measured
during the zero-OOM work (see ADR-018's updates): a single 100k-row
pipeline run peaks at **~284 MiB** against its pod's cgroup, which is
why the measured production posture became one run per 512 MiB pod.
Every mechanism shipped so far arranges the system *around* that
footprint. This ADR is about reducing it.

### Where the memory actually is

The execution model materializes everything, multiple times over:

- Every node's full output is one in-memory `*common.DataSet` —
  `Rows []map[string]interface{}`, roughly 5–10× the size of its own
  NDJSON encoding once map headers, boxed values, and duplicated key
  strings are counted.
- `runTransform` **clones its entire input** before applying rules
  (`node_handlers.go`), so a transform holds two full copies at once.
- A `code` node (`ExecuteCodeNode`) holds the full input `DataSet` in
  Go, encodes it to an NDJSON temp file, runs the Python subprocess —
  whose own RSS lands in the same cgroup — then decodes the full
  output NDJSON back into a second Go `DataSet`. For a code-node
  chain, Go-side memory is input + output per hop, and none of it
  needs to exist: **the data was already sitting on disk in NDJSON on
  both sides of the subprocess.**
- The benchmark pipeline that produced every OOM finding this month
  is three `code` nodes in a line. This matters for scoping: a
  streaming design centered on transform nodes would improve that
  workload by exactly nothing.

### What already exists to build on

- **The spill mechanism is half of a streaming engine.**
  `nodeOutputs` (`node_output_store.go`) already holds outputs either
  inline or as a blob-store reference (`artifact.DatasetRef`), spills
  via a streaming `io.Pipe` encode, and shares one content-addressed
  blob store with the resume artifact plane (ADR-010/012). What it
  lacks: producers can only hand it a fully-materialized `DataSet`
  (spilling happens *after* the peak), and consumers can only get a
  fully-materialized `DataSet` back.
- **NDJSON is the universal interchange already.** Spill blobs,
  resume artifacts, and code-node temp files are all the same
  `EncodeArrowJSON` format. `DecodeArrowJSON` is the only piece that
  insists on materializing; an incremental batch decoder is the
  missing primitive.
- `engine/streaming.go`'s `DataStream`/`RowBatch` channel machinery,
  built and never wired, fits the *later* concurrent-pipelining
  milestone — not the first one.

### Two meanings of "streaming", kept apart deliberately

**Pipelined/bounded-memory execution** — batches flow, memory stops
scaling with dataset size, runs still start and finish. An engine
concern, and what this ADR mostly is.

**Continuous streaming** — unbounded sources (Kafka/CDC), runs that
never end, offsets, windows, exactly-once sinks. A different product.
Notably, its most useful 80% is already latent in this codebase:
scheduled pipelines + incremental checkpoints (the pagination
checkpoint store) *are* micro-batching. Genuine continuous runs are
deferred until micro-batch demonstrably falls short for a real user.

## Decision

Adopt a **segments-with-barriers** execution architecture, introduced
bottom-up, with **reference-passing dataflow as the first milestone**:

**1. Operator capability model.** Every operator is classified:
*streamable* (row-local: the transform rules `rename`, `add_column`,
`filter`, `apply_function`, `replace_values`, `drop_columns`;
ref-consuming/producing code-node I/O), or *blocking* (`sort`,
`dedup`, `aggregate` rules; joins; unions; dataset-level quality
checks; anything an external executor or plugin runs, until it
declares otherwise). The engine compiles each pipeline into
*segments*: maximal streamable chains, with **materialization
barriers** at blocking operators, fan-out/fan-in points, and remote
dispatch boundaries (ADR-017 results cross pods through the SQL
artifact store — a permanent barrier). A fully-batch pipeline is
segments of length one: behavior identical to today, by construction.
Users never choose a mode; streaming is an optimization the engine
applies where provably safe — the same out-of-the-box doctrine as
every ADR-018 default.

**2. Milestone 1 — reference-passing dataflow (disk-pipelined, not
yet concurrent).** The scheduler, retries, attempts, leases, events,
and per-node bookkeeping stay exactly as they are. What changes is
the *currency* nodes trade in: a handler may return a
`artifact.DatasetRef` instead of a `*DataSet`
(`nodeExecutionResult.outputRef`), and may consume its input as an
incremental batch stream decoded from a ref instead of a materialized
`DataSet`. Concretely:

- New primitive: an incremental NDJSON batch decoder (the streaming
  counterpart of `DecodeArrowJSON`).
- `nodeOutputs` accepts pre-spilled refs from producers (`PutRef`)
  and serves batch iterators to stream-capable consumers
  (`GetBatches`), while `Get` still materializes for everything else.
- **Code nodes go ref-to-ref**: input ref → stream-copy to the NDJSON
  temp file (never a Go `DataSet`); Python runs unchanged; output
  NDJSON file → stream-copy into the blob store → output ref. Go-side
  cost per hop drops from two full datasets to I/O buffers. The
  Python subprocess's own transient RSS remains — bounding it is
  Milestone 1.5, not 1.
- **Transform nodes stream** when all rules are row-local: batch in,
  rules applied per batch (no whole-input clone), batch encoded out.
- Post-processing sites become ref-aware: row counts from the ref,
  node preview from the first batch (it only keeps 50 rows), resume
  artifacts written by streaming blob-to-blob (the artifact plane and
  spill plane already share a store), the SQL artifact store's
  column-value write staying the one place a full encode is
  unavoidable (bounded, instance-dispatch only).
- Engagement rule: streaming activates when the data involved crosses
  the existing spill threshold — below it, memory doesn't matter and
  batch is faster. Streaming is, precisely, the completion of
  spilling: data large enough to spill now *lives* on disk its whole
  life instead of visiting memory first.

**3. Milestone 1.5 — the Python side.** The code-node harness feeds
`rows` lazily (a generator reading NDJSON incrementally) so row-local
scripts stop materializing their input; an incremental emit idiom
does the same for output. Needs an SDK/compatibility decision
(`len(rows)` and indexing break under a generator), so it is its own
milestone with its own compatibility notes, not smuggled into 1.

**4. Milestone 2 — concurrent segments.** Members of a segment run
simultaneously connected by bounded channels (`engine/streaming.go`
finally earns its keep), giving latency overlap on top of bounded
memory; spill-backed fan-out replay; streaming hash aggregation.
Retry granularity becomes the segment, from its written-through
input — the same semantics as today's node retry, one level up.

**5. Continuous streaming stays out.** Micro-batch via scheduler +
checkpoints first; a dedicated ADR if a real workload outgrows it.

## Consequences

### Positive

- Go-side peak memory for streamable chains stops scaling with
  dataset size — for the measured benchmark shape (three code nodes),
  the dominant Go-side copies disappear entirely.
- Composes with everything shipped this month rather than replacing
  it: admission control admits more because runs cost less; the
  memory-derived concurrency divisor can eventually shrink; elastic
  scaling handles count while this handles size.
- No new user-facing concepts, config, or pipeline changes. Resume
  (ADR-010), retries, recovery, and observability contracts preserved
  at every step — refs were already a first-class output form via
  spilling; this extends when they come into being, not what they are.
- Each milestone is independently shippable and independently
  verifiable against the same live benchmark that produced every
  number in this ADR.

### Negative

- Every intermediate crossing a segment boundary pays an encode +
  decode it might not have paid before (only above the spill
  threshold, where the alternative was holding it in memory — but
  it is real I/O and real CPU).
- Ref-awareness adds a second shape to `nodeExecutionResult` and
  every post-processing site that touches output; the type system
  won't force exhaustiveness, review has to.
- The cgroup still counts the Python subprocess: until Milestone 1.5,
  code-node-heavy pipelines keep a transient per-node RSS floor that
  Go-side streaming cannot touch. Expectations set accordingly.

### Deferred

- Milestone 1.5 (lazy `rows` harness), Milestone 2 (concurrent
  segments, streaming aggregation, fan-out replay), streamable
  sources/sinks (file/DB sources and sinks are naturally incremental
  but keep their batch handlers until the ref plumbing has soaked),
  continuous streaming, and any revision of the one-run-per-512Mi
  divisor until re-measured under Milestone 1.

## Alternatives considered

- **Wire `DataStream` channels through the scheduler now** — rejected
  as the first step: concurrent dataflow brings retry-after-partial-
  consumption, fan-out backpressure deadlock, and teardown ordering
  all at once, in the engine's most load-bearing file, for a win the
  disk-pipelined form already captures on memory (the actual
  production problem). It returns as Milestone 2 on top of proven ref
  plumbing.
- **A user-facing "streaming mode" flag per pipeline** — rejected:
  the engine knows which operators are safe to stream better than
  users do, and every misuse would be a support case. Capability
  detection with automatic barriers is strictly more honest.
- **Columnar in-memory representation (true Arrow) instead of
  streaming** — attacks the 5–10× map-row overhead rather than the
  scaling-with-size problem; a large rewrite of every handler and the
  entire plugin/code-node interchange, for a constant-factor win.
  Worth revisiting someday; not the bottleneck this ADR addresses.
- **Micro-batch everything via the scheduler today** — already
  possible, covers the continuous case, does nothing for a single
  large run's footprint, which is the measured problem.

## Follow-ups

- Milestone 1 implementation: batch decoder, `PutRef`/`GetBatches`,
  ref-to-ref code nodes, streamed row-local transforms, ref-aware
  post-processing — gated by the spill threshold, verified against
  the live benchmark with before/after cgroup peaks recorded here.
- Milestone 1.5 SDK note: lazy `rows` compatibility policy.
- Re-measure the per-run divisor (ADR-018) once Milestone 1 lands.
- Milestone 2 design note before any scheduler concurrency work.
