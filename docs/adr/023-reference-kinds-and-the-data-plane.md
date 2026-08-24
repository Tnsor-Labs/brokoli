# ADR-023: Reference kinds, so the engine can stay out of the data path

**Status:** proposed
**Date:** 2026-08-24

## Context

ADR-012 introduced references. ADR-019 made pipeline edges carry them
instead of datasets, and set the doctrine that matters here: *users
never choose a mode; streaming is an optimization the engine applies
where provably safe.* Both are done, and they worked.

What they bought is bounded memory. Measured this week on `source_file`,
1,000,000 rows of a 108 MB CSV under a 512 MiB hard container limit:

| | |
| --- | --- |
| before | OOM killed after 3s, exit 137 |
| after | completed, 17 MiB resident |

Without a cap it had peaked at 653 MiB for a 108 MB file — roughly six
times the file on disk.

What they did not buy is throughput, and the numbers say why:

| pipeline, 1,000,000 rows | time | rate |
| --- | --- | --- |
| `source_file` to `sink_file` | 22s | ~43,500 rows/sec (~5 MB/s) |
| the same with a `code` node editing every row | 93s | ~10,600 rows/sec |

Twenty-two microseconds per row on a path performing **no
transformation at all**. That is framing cost: CSV parse, a
`map[string]interface{}` per row, JSON encode, blob store, JSON decode,
CSV write. Extrapolated, a billion rows is about 6.4 hours per node, and
a chain multiplies it.

### The shape of the limit

The limit is not the encoding. It is that **every byte crosses the
worker at all**, including when both endpoints could have talked to each
other directly. A Postgres-to-Postgres copy on the same server currently
reads rows out, renders them as NDJSON into the artifact store, parses
them back, and writes them in — for data that never needed to leave the
server.

`DatasetRef` is why. It carries a `Format` (with Parquet deferred by
ADR-012), but the *kind of location* is fixed: bytes in the artifact
store, written and read by the engine.

```go
type DatasetRef struct {
	ArtifactRef            // always the artifact store
	Format   string        // "ndjson" today
	Columns  []string
	RowCount int64
}
```

So a reference can describe a different **encoding**, but not a
different **place**, and not a place the engine does not own.

### What the alternatives look like

Three ways a transfer can be arranged, only the first of which exists
here:

1. **The worker is the data pipe.** Generic, always available, and now
   memory-safe. Every byte is parsed and re-encoded.
2. **Staged in object storage.** The source unloads to S3, the
   destination bulk-loads from it. The worker moves metadata.
3. **The systems talk directly.** The worker issues an instruction — a
   query, an unload, a bulk-copy — and monitors it. The worker's network,
   CPU, and memory use are all roughly constant regardless of volume.

Orchestrators that handle large volumes without large orchestrator
machines are mostly doing (2) and (3): a task submits work to a
warehouse or a Spark cluster and waits. Their edges carry a URI, not a
dataset — which is the same idea as ADR-019's references, one step
further along, because the *producer* also stayed out of the data path.

Brokoli should not simply become that. Running a Python transform without
standing up a second system is the thing it offers that a pure
scheduler does not, and (1) is a legitimate architecture, not a
deficiency. The gap is that (1) is currently the *only* architecture,
so it gets used even where the systems involved could have done the work
themselves.

## Decision

**Make the reference kind part of the type, and let the engine choose
it.** A pipeline edge carries one of:

- **`BlobRef`** — bytes in the artifact store, in some format. What
  `DatasetRef` is today, unchanged, and the fallback that is always
  available.
- **`TableRef`** — the rows are in a database, addressed by connection
  plus a relation (a table, a temporary table, or a query).
- **`ObjectRef`** — the rows are in object storage, addressed by a URI
  both endpoints can reach.

Two rules govern their use:

**A node produces the cheapest reference its output can honestly take,
and consumes the richest reference it can use.** A `source_db` whose
rows do not need to leave the server produces a `TableRef`. A `sink_db`
handed a `TableRef` on the same server issues `INSERT INTO … SELECT`
and moves nothing. Where the two do not meet, the engine falls back
through `ObjectRef` to `BlobRef`, and `BlobRef` always works.

**The author never chooses.** This is ADR-019's doctrine applied to
placement rather than streaming: reference kind is an optimization the
engine applies where provably safe, not a setting on a node. An
operator-level override exists for incidents — the shape of
`BROKOLI_SINK_COPY=0` — but it is not a pipeline-authoring concept.

### The second axis: whether the engine reads the payload

Reference *kind* answers where the data is. It does not answer whether the
engine has to look at it, and that is a separate question with its own cost.

The measurement above is the argument. Twenty-two microseconds per row on a
`source_file` to `sink_file` pipeline, and that pipeline transforms nothing:
the engine decodes every row into a `map[string]interface{}` and re-encodes
it while **no node between the two ever reads a field**. The entire cost is
interpretation nobody asked for.

For scale, a message streaming server that never interprets its payloads
reports throughput three orders of magnitude above what we measured. That is
not a fair comparison — it is a different job, and it can be fast precisely
because it does not care what the bytes mean — but it does size what
interpretation costs when it is not needed.

So a reference carries a second property alongside its kind:

- **interpreted** — the engine decodes rows, because something between the
  producer and the consumer reads a column.
- **opaque** — the engine moves framed bytes and never decodes them.

**The engine can decide this statically.** Whether any operator in the segment
reads a column is a property of the segment, known before the run starts, from
the same plan that already decides streaming eligibility. It is not a hint and
not a setting.

What it unlocks, all of them cases that exist today and pay full interpretation
cost for nothing:

- `source_file` to `sink_file` in the same format with no transform between:
  copy the bytes.
- `source_db` to `sink_db` on *different* servers: `COPY TO STDOUT` piped into
  `COPY FROM STDIN`. The worker is still in the data path — this is
  unavoidable across servers — but it moves opaque frames at line rate instead
  of parsing JSON.
- an object in storage the destination can load natively: hand over the URI.

The two axes are orthogonal and compose. A `TableRef` is inherently opaque —
the engine never sees rows at all. A `BlobRef` can be either, and that is where
the decision has teeth.

**Equivalence is proven, not assumed.** A path that keeps data in the
database must produce the same rows as the path that moves it through
the engine, and that must be demonstrated by comparison against a live
system rather than argued. This is the discipline the SQL pushdown work
established (#331): both backends run, the rows are compared, and a
capability that cannot be shown equivalent is refused rather than
enabled optimistically. It found four real bugs in three rounds, every
one at a boundary that had looked obviously safe.

## Resolutions

The first draft of this ADR listed its hardest problems as open costs. They
are not open; each has an answer, and most of the answers are patterns this
codebase already runs in production. They are recorded here as decisions, so
that implementing #324 is engineering rather than design.

### TableRef lifetime: write-ahead registration, eager drop, sweep as backstop

Every staging relation lives in one dedicated schema, `brokoli_staging`,
created on first use. Names are deterministic — derived from run ID and node
ID, truncated to the backend's identifier limit — so a retry of the same node
collides with its own leftover rather than creating a sibling.

The ordering is the dispatch-intent pattern from the queue-durability work
(`engine/redispatch.go`): **the ref is registered in the store before the
relation is created.** A crash between the two leaves a harmless intent with
nothing behind it; a crash after leaves a relation the sweep can find, because
the intent names it. At no point does a relation exist that nothing on record
knows about.

Cleanup is two-layered, like run retention (`engine/retention.go`):

- **Eager:** the consumer drops the relation the moment it has consumed it,
  in the same transaction as the consuming write where the backend allows it.
- **Sweep:** every queue-connected instance periodically scans registered
  staging refs whose run is terminal, or whose TTL has lapsed, connects with
  the stored connection, issues `DROP TABLE IF EXISTS`, and deletes the
  registration. Idempotent, safe to run everywhere at once — the redispatch
  sweep's properties, for the same reasons.

If the sweep cannot reach the database — credentials rotated, host gone — it
logs, retries with backoff, and surfaces a metric. The worst case is bounded
and documented: everything Brokoli ever creates in a customer database is
inside `brokoli_staging`, so the manual recovery is one statement, `DROP
SCHEMA brokoli_staging CASCADE`, and it cannot touch customer objects.

Relations are **unlogged tables**, not temporary tables. Temporary tables are
per-session and die with the connection, which breaks any consumer that
connects separately from the producer; unlogged tables skip WAL (this data is
reconstructible by definition) and survive across connections. Per-backend
equivalents are chosen when a backend is added, under the same lifetime rules.

### Resume: the segment is the resume unit, and the commit is the artifact

Resume never depends on a `TableRef` surviving. A `TableRef` is valid only
inside its run's execution; ADR-010's durable-artifact contract applies at
segment *barriers*, which stay `BlobRef`, exactly as ADR-019 placed them.

Inside a pushed-down segment the guarantee is better than artifact replay,
not worse: the consuming write is one statement in one transaction on the
destination. Either it committed — the node is complete and there is nothing
to resume — or it did not, and resume re-runs the segment from its upstream
barrier. Re-running is the cheap case *because* the segment is pushed down;
that is the point of pushing it down.

One window needs closing explicitly: the destination transaction can commit
and the engine can crash before recording the node as complete, and a naive
re-run would then append twice. So the consuming transaction also writes a
completion marker — run ID, node ID, attempt — into a `brokoli_staging`
bookkeeping table, **in the same transaction as the data**. A resume checks
the marker before re-running: marker present means the commit happened and
the node is complete regardless of what the engine recorded. The marker rides
the same lifetime rules as every other staging object. This is the
transactional-outbox shape, pointed inward, and it is what makes the claim
"either it committed or it did not" actually decidable from the engine's
side.

So the earlier framing — "either barriers materialize to BlobRef, giving up
the benefit, or resume is not offered" — was a false choice. Barriers keep
durability; segments get atomicity; nothing is given up.

### ObjectRef: the engine only hands out URIs it minted

The netguard gap closes by construction, not by extending netguard into
databases we do not control:

- An `ObjectRef` may only point at the **operator-configured artifact object
  store** — the endpoint already in the deployment's configuration. Never at
  a pipeline-author-supplied URI. The allowlist has exactly one entry and the
  operator wrote it.
- URIs are presigned, read-only, single-object, with a TTL bounded by the
  node timeout.
- The plan-time check refuses to emit a load-from-URI instruction for any
  other destination, and that refusal is pinned by a test in the same style
  as the netguard analyzer: the guard is on what the engine will *say*, since
  it cannot control what a remote database will *do*.

Under this rule, the customer's warehouse fetching the URI is the same trust
relationship as the worker writing to that bucket today — same endpoint,
same credentials' scope, narrower grant.

### Row counts: a count source is an eligibility condition

No run ever reports an unknown row count. Instead, a path is opaque-eligible
**only if it has a free count source**, of which there are three:

- **Command tags.** `COPY` and `INSERT … SELECT` report rows affected; the
  engine already reads this (`engine/copy_postgres.go`,
  `tag.RowsAffected()`).
- **Frame counting.** NDJSON rows are newline-framed; counting newlines in
  the buffer already flowing through the copy is not parsing and costs
  nearly nothing.
- **Producer metadata.** An object written by a Brokoli node records its row
  count at write time; handing over the URI hands over the count.

A format with no cheap frame count — CSV once quoting is involved — is simply
not opaque-eligible and falls back to the interpreted path. The trade is
deliberate: fewer opaque paths, never a degraded run view. This also keeps
the count-in/count-out invariant checkable on every opaque transfer, which is
the integrity check that replaces incidental parsing (next section).

### Validation: declared, not incidental — and the static rule already does it

Today a malformed row fails the run only because something happens to decode
it. That validation was never declared anywhere; it is a side effect of the
data path, and it already varies — a pushed-down segment (#324) would skip it
too, TableRef or not.

The resolution falls out of the interpreted/opaque rule with no special case:
**a `quality_check` node reads columns, so its presence makes the segment
interpreted.** Validation is therefore something an author declares by adding
the node that does it, and the plan honours the declaration automatically.

A pure copy's contract is integrity, not validity: count-in equals count-out
(both ends have count sources, per the eligibility rule) plus a checksum over
the byte stream. A copy that moves malformed bytes faithfully is a *correct
copy*. Detecting malformed data is a quality check, and writing one is what
makes the engine look.

### The test matrix: a capability registry, refusing by default

The matrix does not multiply freely, because cells are closed until proven.
The same discipline as the SQL compiler (#331): a kind-by-operator capability
table where **every enabled cell carries a differential test against the
`BlobRef` path over a shared fixture corpus, and every disabled cell is
asserted disabled** — so a capability cannot turn on without a passing
comparison, and cannot silently regress off. Format properties (framable,
countable) are declared once per format with their own tests, not per cell,
which keeps the matrix two-dimensional.

### The operator escape hatch

`BROKOLI_DATA_PLANE=interpreted` forces every edge onto the interpreted
`BlobRef` path — today's behaviour exactly — for incident response, in the
shape of `BROKOLI_SINK_COPY=0`. It is an operator setting, not a pipeline
concept, per ADR-019's doctrine.

## Consequences

### Positive

- The engine leaves the data path entirely for the cases where the
  systems can do the work. Same-server transfers stop costing the
  22 µs/row framing tax because there is no framing.
- It generalizes the pushdown work rather than sitting beside it. #324
  currently describes fusing a transform into one query; under this
  decision that is one consequence of a more useful rule, and **skipping
  the transfer entirely applies even when there is no transform at all**.
- Partitioning composes instead of being a separate mechanism: a
  partitioned source produces N references and N workers consume them,
  whatever kind those references are.
- ADR-012's deferred Parquet becomes an orthogonal change — a `Format`
  on `BlobRef` — rather than something entangled with placement.
- The reference already records `Columns` and `RowCount`, so a consumer
  can size and plan work without fetching, whichever kind it is.
- Opaque passthrough removes the framing cost from the cases that never
  needed it, without needing a faster encoding to do it. It is complementary
  to a columnar format rather than an alternative: a columnar `BlobRef` makes
  interpretation cheaper, opaque passthrough removes it.

### Negative

Each of these is paid rather than open — the mechanism is in Resolutions —
but paying it is real work and real constraints:

- **Lifetime costs a sweep and a registry.** Staging relations need
  write-ahead registration, an eager drop path, and a janitor sweep with
  metrics — new moving parts in customer databases, even if the pattern is
  proven here. The worst case is bounded to one schema and one documented
  statement, but it exists.
- **Resume narrows inside segments.** Mid-segment resume is gone by design:
  a pushed segment re-runs from its barrier. That is the right trade — the
  re-run is cheap — but it is a semantic change to ADR-010's contract and
  must be amended there, not just here.
- **The ObjectRef rule limits reach.** Only the operator's own object store
  can be a direct-load source. A warehouse that could load from a customer's
  other buckets will not be asked to. Correct, and still a limitation.
- **Fewer opaque paths than theoretically possible.** The count-source
  eligibility rule excludes formats without cheap framing (quoted CSV), and
  the quality-check rule flips segments interpreted. Both give up speed for
  properties we refuse to lose. That is the intended shape of the system,
  and it should be defended in review when someone proposes relaxing it.
- **The capability registry is a standing tax.** Every new kind, operator,
  or format pays a differential-test toll before it turns on. This is the
  point, and it is also permanent overhead.

### Deferred

- **`StreamReference`** for topic-shaped sources. Plausible, unmotivated
  by anything measured, and better designed once the three kinds above
  have been used in anger.
- **Parquet as a `BlobRef` format**, still ADR-012's deferral, now
  independent of this one.
- **Intra-node partitioning.** One node still runs in one worker over
  one stream. This decision makes partitioning expressible; it does not
  implement it (#330).
- **Non-Postgres staging backends.** The lifetime rules are decided
  (Resolutions); which relation type plays the unlogged-table role in each
  other backend is settled when that backend is added.

## Alternatives considered

- **Keep one reference kind and make the bytes cheaper.** A columnar
  frame across the boundary is worth doing (#330) and would help every
  path, but it lowers a constant. The worker stays in the data path,
  reading and writing every byte, for transfers that never needed it.
- **Push everything down and stop moving data.** This is what a
  scheduler-shaped orchestrator does, and it is coherent. It also gives
  up running a Python transform without a second system, which is the
  thing Brokoli offers that they do not.
- **Let the pipeline author pick the transfer mode.** Contradicts
  ADR-018 and ADR-019's doctrine, and asks an author to reason about
  where their rows physically are — the low-level engineering this
  product exists to remove. The engine has the connection identities and
  the column types; it is better placed to decide, and can be checked.
- **Special-case same-connection transfers without a reference
  taxonomy.** Cheaper, and enough for the immediate win. Rejected
  because the next case — a file already in the object store a warehouse
  can load from — needs the same shape, and would arrive as a second
  special case rather than a second reference kind.

## Follow-ups

- #331 — tracking issue for the pushdown arc; this ADR is the frame it
  sits in.
- #324 — routing a same-connection segment. Should be rewritten as
  "produce and consume a `TableRef`", of which fusing the transform is
  one part.
- #327 — `sink_api` and `source_api` streaming, which stays a `BlobRef`
  story.
- #330 — the per-row framing cost, and intra-node partitioning. Opaque
  passthrough addresses the same measurement from the other side: a columnar
  format makes interpretation cheaper, this removes it where it was never
  needed. Both are worth having.
- #325 — order-independent summation. Not obviously related, but it gates
  #330's partitioning: splitting a source across workers makes aggregation
  order depend on scheduling, so a `sum` that already varies with order would
  stop being reproducible between runs over identical data.
- ADR-010 needs an amendment recording the settled answer: durability at
  barriers, atomicity inside segments, no mid-segment resume. ADR-019's
  barrier placement is unchanged.
