# ADR-039: Lineage that says how it knows

**Status:** proposed
**Date:** 2026-09-14

## Context

`GET /api/lineage` returns a graph of assets and processing nodes built
from real DAG edges, with column-level edges attached. The graph shape is
right. The column edges are not.

They are produced by `inferColumnMappings` (`engine/lineage.go:268`),
which compares the observed column names of two adjacent nodes and emits
an edge wherever a name appears on both sides:

```go
if _, ok := targetColumns[column.Name]; !ok { continue }
... Confidence: 0.7, MappingReason: "observed column name match"
```

Three things follow, and all three are visible to a user as confident
output:

- **A derived column gets no edge at all.** `total = price * qty`
  produces a column that does not exist upstream, so the name match
  fails and the strongest fact in the pipeline -- that `total` is a
  function of `price` and `qty` -- is the one thing the graph cannot
  say.
- **A coincidence gets an edge.** Two unrelated datasets that both have
  `id` are joined by an edge that asserts a derivation nobody performed.
- **`Confidence: 0.7` is a literal.** It is the same for every edge,
  carries no information, and reads as a calibrated probability. A
  number that is always 0.7 is not a confidence; it is decoration.

The question this endpoint exists to answer is "how did this field get
here". Name matching cannot answer it, and the current output does not
admit that.

### The graph is also the only consumer

Nothing outside the lineage endpoint uses any of this. The assets a
pipeline reads and writes are computed inside `buildLineageGraph` and
kept there, so any other consumer that wants them has to compute its own
-- and the moment two functions each decide what "the asset behind this
node" means, they drift, and the same table gets two names in two places.

That gap has since been closed for one consumer: #627 extracts
`PipelineAssets` from the same extractors the graph uses and reports a
run's datasets through the lifecycle. It is mentioned here because it
sets the precedent this ADR depends on -- one definition of an asset's
identity, reused rather than reimplemented.

### What the engine already knows and does not use

A transform node is not SQL text. It is a typed config in the IR:
`add_column` carries an expression, `rename` carries a pair, `select`
carries a list. The column mapping for those is not something to infer;
it is already written down, exactly, in a structure the engine parses
and validates on every save.

Separately, every execution already records what happened:
`artifact.DatasetRef` carries a content digest and row count, and
`NodeProfileStore` carries a schema snapshot and profile per
`(run, node)`. Nothing joins those two facts to the graph.

So the engine holds exact declared mappings and exact observed
execution, and reports a guess derived from neither.

### The boundary that cannot be crossed

A code node runs arbitrary Python, JavaScript or JVM bytecode. No
analysis available to this engine can say which output column derives
from which input. Today that node emits confident-looking edges for any
name that survives it, which is the worst available answer: it is a
plausible claim about a black box.

The same is true of a `source_db` node carrying user-authored SQL.

## Decision

**Lineage records how it knows, refuses to guess at the column level,
and stays complete at the node level regardless.**

Four parts.

### 1. Two granularities, and only one of them can be unknown

**Node and dataset lineage is always complete.** Which node produced
which dataset, from which inputs, in which run, is known from the DAG
and the execution record without exception. A code node is not a hole
in the graph: it is a node with known inputs, a known output and a
known identity.

**Column lineage is present only where it can be established.** Where
it cannot, the node is marked opaque and no column edges are drawn
through it.

This split matters because the two questions are different. "Which task
produced this table" is always answerable and is most of what an
on-call person needs. "Which field produced this field" is sometimes
unanswerable, and saying so is the correct answer.

### 2. Evidence levels replace the confidence float

Every edge carries how it was established, not a number:

| level | source | meaning |
| --- | --- | --- |
| `declared` | the IR: expression, rename, select, join key | exact; the mapping is written in the pipeline |
| `attested` | the execution record: digests, row counts, schema | exact, and provable after the fact |
| `parsed` | table references extracted from user SQL | table-level only, never column-level |
| `inferred` | column-name match | a guess, and labelled as one |
| `opaque` | the node cannot say | no column edges are drawn |

`inferred` survives only for adjacency where nothing better exists, and
it is now legible as the weakest class rather than presented as 0.7.

A consumer that wants only trustworthy lineage filters to `declared`
and `attested`. That is not possible today at any price.

### 3. Column mappings are declared by node type, and the absence is a gate

Each node type answers one question: given my input columns and my
config, which output column came from which inputs, and by what rule.

That answer lives with the node type, not in a switch statement in the
lineage package. A central switch is a function that silently rots as
node types are added; a per-type declaration makes the absence
detectable, and a test asserts that **every registered node type either
declares its column mapping or declares itself opaque**. Neither is a
failure. Silence is.

A node type that declares itself opaque is making a statement, and the
graph renders it as one.

### 4. Provenance is recorded per run, and purged with the run

Each node execution records what it consumed and produced: input
dataset digests, output digest, row counts, and the schema observed on
each side. That is the `attested` layer, and it is what makes lineage a
statement about a specific execution rather than about a pipeline
definition.

It is written for **every run**, and it is deleted when the run is
deleted. Retention already has a purge path
(`PurgeRunsOlderThanByOrg`, `ListRunIDsOlderThanByOrg`), and provenance
joins the set of per-run records that path removes, alongside run
attribution. A record that outlives the run it describes is an orphan
that inflates the table and answers questions about something that no
longer exists.

## Consequences

### Positive

- The question the endpoint exists for becomes answerable: a derived
  column reports the columns it derives from, exactly, because the
  expression says so.
- A wrong edge stops being indistinguishable from a right one. A
  consumer can require `declared` or `attested` and get only facts.
- "Which task produced this" is always answerable, including through
  code nodes, so the graph stays useful where column lineage stops.
- Lineage becomes a statement about a run, with digests, rather than
  about a definition that may not be what executed.
- A new node type cannot quietly ship with no lineage.

### Negative

- Every node type now owes a declaration. That is real work per type and
  a gate that will fail for somebody adding a node in a hurry. It is the
  cost of the guarantee.
- The graph will show fewer column edges than it does today, and for a
  pipeline built on code nodes, possibly none. That is a loss of
  apparent capability and a gain in truthfulness; it will read as a
  regression to anybody who trusted the old edges.
- Per-run provenance is another per-run write and another table that
  grows with run volume. The dashboard's own 200-run cap and the
  artifacts-table bloat are the precedents for taking that seriously.

### Deferred

- **Column lineage through user-authored SQL.** See Alternatives: this
  ADR extracts table references and refuses column-level. Revisit only
  with evidence that structured transforms are not covering real
  pipelines.
- **Lineage across pipeline boundaries over time**, point-in-time
  queries, and impact analysis over the asset graph. The record this
  ADR defines is the substrate for those; the queries are not in scope.
- **A declaration format for code and task nodes.** ADR-033 already
  gives a task a declared output contract, which is the natural place
  for an author to state column provenance. Widening that contract is
  its own decision and its own compatibility question.

## Alternatives considered

- **Parse SQL for column lineage.** This is what dbt does, and it is
  correct for dbt, which never sees the data and has only text. Rejected
  here: Go has no mature cross-dialect column resolver, writing one is a
  multi-quarter project, and its failures are invisible to the person
  reading the graph. The engine also does not need it for the transforms
  it generates itself, since those come from the IR. Only user-authored
  SQL is affected, and table-level extraction covers most of what that
  SQL is asked about.
- **Keep name matching, improve the heuristic.** Type-aware matching,
  cardinality checks, value overlap. Rejected: every version of this
  produces a claim about a derivation with no evidence that the
  derivation happened. A better guess is still a guess presented as
  lineage.
- **Infer column lineage from observed schema diffs.** Attractive
  because it needs no declarations. Rejected: a diff shows that a column
  appeared, never which inputs produced it, and it would relabel the
  same guess as an observation, which is worse than the current state.
- **Emit no column lineage at all.** Honest and much cheaper. Rejected
  because the exact case is genuinely common: a pipeline of structured
  transforms has complete, provable column lineage available for free
  from the IR, and withholding it to avoid the hard case would be its
  own kind of dishonesty.
- **Compute lineage on read from the IR each time.** Rejected for the
  declared layer only as an optimisation question: the IR has a
  canonical hash (ADR-032 and the IR canonicalization spec), so declared
  lineage is a pure function of that hash and can be computed once per
  pipeline version rather than per request. Attested lineage has to be
  per run because it describes one.

## Follow-ups

Landed:

- **The node-type declaration interface and the coverage gate** (00d376f).
  Every node type declares its column mapping or declares itself opaque,
  in a map keyed by node type; `TestEveryNodeTypeDeclaresItsColumnLineage`
  fails on a missing key, and `models.AllNodeTypes` is checked against the
  constants by parsing the source.
- **Evidence levels replace `inferColumnMappings` and `Confidence`**
  (00d376f), with the release note naming the breaking change.
  `inferred` is in the vocabulary and produced by nothing.
- **The per-run provenance record** and its purge. One row per (run,
  node), removed with the run by `ON DELETE CASCADE` rather than by a
  cleanup method somebody has to remember to call. Readable at
  `GET /api/runs/{id}/provenance`. A digest is recorded only for datasets
  that went through the artifact store; in-memory datasets carry row
  counts and columns without one.
- **`attested` edges.** An identity edge (the same column, from a node's
  single input) is promoted from `declared` to `attested` when the run
  its profile came from stored that input and the node's output with the
  same digest: the bytes did not change, so the column provably passed
  through untouched, whatever the node's type says. Anything short of
  that stays declared -- two inputs, a missing digest on either side,
  unequal digests, or an edge that is not identity -- and the edge's
  reason names the run that proved it. The lineage handler reads every
  profiled run's records in one query.

  This rests on the recorder capturing a node's output digest after the
  engine stores it. The first version captured it before, so most
  processing nodes recorded no output digest; that was fixed in #637,
  and measured then: a quality check that returns its input unchanged
  stores byte-identical output.

  Byte identity also needs both sides stored in one format, and the two
  were chosen independently: a source's output by the stream writer
  (from `BROKOLI_STREAM_CODEC` or its first batch), a node's output by
  the spill (Arrow whenever the whole dataset allows). With the stream
  codec on NDJSON no edge could be attested (#641). A node with a single
  stored input now stores its output in that input's format (#643), and
  the format is recorded beside each digest.

Remaining:

- Column facets for lineage consumers outside the engine. #627 carries
  datasets; the column-level half needs the declarations above, and only
  `declared` and `attested` edges should ever leave this process. An
  `inferred` edge published to a shared catalogue outlives every caveat
  attached to it.
- Table-reference extraction for `source_db`.
