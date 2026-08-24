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

- **Lifetime becomes a real problem.** A `BlobRef` is cleaned up by run
  retention. A `TableRef` may point at a temporary table someone has to
  drop, and a failed or abandoned run leaks it into a customer's
  database. Each kind needs an owner and a cleanup path, and "the engine
  crashed" must not mean "the warehouse now has orphan tables".
- **Resume semantics get harder.** ADR-010 assumes a node's output can
  be read back. A temporary table may not survive the gap before a
  resume. Either barriers materialize to `BlobRef`, giving up some of
  the benefit exactly where durability is needed, or resume from a
  `TableRef` node is not offered and says so.
- **A new trust relationship.** An `ObjectRef` both endpoints can read
  means the customer's warehouse is reaching a bucket we named. That is
  a credential and access-grant question, not just an addressing one.
- **A gap in outbound safety.** `pkg/netguard` filters HTTP the *worker*
  makes. If the engine instead asks a database to fetch from a URI, that
  request leaves from the database and passes through nothing we
  control. ADR-022's outbound policy does not extend there, and this
  decision creates the first path where it needs to.
- **Row counts stop being free.** The engine records `RowCount` on every
  reference and logs it per node, and lineage and the run view both use it.
  An opaque copy does not know how many rows it moved without parsing, which
  is the thing being avoided. Either the format is framed in a way that allows
  counting without decoding, or a run that took the opaque path reports an
  unknown row count — and "faster, but the UI now says unknown" is a product
  decision, not an implementation detail.

- **Parsing is currently doing validation nobody declared.** A malformed row
  fails the run today because something decodes it. An opaque copy moves
  corrupt bytes through without noticing, and the failure surfaces later, in
  the destination, detached from the pipeline that carried it. Anything
  relying on that incidental validation — quality checks in particular — needs
  to keep an interpreted path.

- **The test matrix multiplies.** Every reference kind must be compared
  against `BlobRef` for every operator that can produce or consume it.
  That cost is the point, but it is a real cost.

### Deferred

- **`StreamReference`** for topic-shaped sources. Plausible, unmotivated
  by anything measured, and better designed once the three kinds above
  have been used in anger.
- **Parquet as a `BlobRef` format**, still ADR-012's deferral, now
  independent of this one.
- **Intra-node partitioning.** One node still runs in one worker over
  one stream. This decision makes partitioning expressible; it does not
  implement it (#330).
- **How a format declares itself countable and framable.** Deciding whether
  a given format can be moved opaquely, and whether rows can be counted
  without decoding, is per-format work. NDJSON is trivially framed by
  newlines; CSV is not, once quoting is involved.

- **Which relation a `TableRef` names** — a real temporary table, an
  unlogged staging table, or a query held as text — is an
  implementation question with different lifetime and resume answers,
  settled per backend rather than here.

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
- ADR-010 and ADR-019 need an amendment once resume-from-`TableRef` is
  settled either way.
