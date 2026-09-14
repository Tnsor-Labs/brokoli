# OpenLineage facets Brokoli emits

Brokoli reports every pipeline run to an OpenLineage-compatible
catalogue when one is configured. The events are standard
[RunEvents](https://openlineage.io/docs/spec/run-event); this page
documents the two custom facets they carry and the naming Brokoli uses
for datasets, so a consumer can read them without reading the source.

It is the page the `_schemaURL` on those facets points at.

## Configuration

| Variable | Meaning |
| --- | --- |
| `BROKOLI_OPENLINEAGE_URL` | The lineage endpoint, for example `http://marquez:5000/api/v1/lineage`. Emission is off when this is unset. |
| `BROKOLI_OPENLINEAGE_NAMESPACE` | The job namespace. Defaults to `brokoli`. |
| `BROKOLI_OPENLINEAGE_API_KEY` | Sent as `Authorization: Bearer <key>` when set. |

Emission is best effort. A catalogue that is down or rejecting events
does not fail the run: the failure is logged and the run continues. A
run is the product; lineage is a report about it.

Three events per run: `START` before execution, then `COMPLETE` or
`FAIL`.

## Dataset naming

Brokoli identifies an external asset as `<kind>:<rest>`, where kind is
`file`, `table` or `api`. On the wire the kind becomes the dataset
namespace and the remainder becomes the name.

| Brokoli asset | `namespace` | `name` |
| --- | --- | --- |
| `file:/data/orders.csv` | `file` | `/data/orders.csv` |
| `table:analytics.orders` | `table` | `analytics.orders` |
| `api:https://example.com/v1/orders` | `api` | `https://example.com/v1/orders` |

The identifier is split on the **first** colon, so an API asset keeps
its scheme in the name.

### A limitation worth knowing about

For a file this matches OpenLineage's own convention. For a table it
does not. The spec's convention names the datasource in the namespace,
`postgres://host:5432`, and Brokoli's table identifier does not carry
one: it is derived from the query rather than from the connection.

So every table lands in the namespace `table`, and two deployments
reading the same warehouse table emit datasets a catalogue cannot
confidently merge, while two different tables that happen to share a
qualified name are indistinguishable.

This is stated rather than papered over. Inventing a plausible host
would be worse, because a catalogue keeps a dataset identity
indefinitely and a wrong one is not recoverable by fixing the emitter
later.

Assets the extractor cannot name are omitted rather than sent with an
empty name, which would merge every unnamed asset in the catalogue into
one node.

`inputs` and `outputs` are always present, and are empty arrays when a
run touches no external asset.

### What the datasets describe

They come from the pipeline definition: the assets the pipeline is
**declared** to read and write. A run that failed partway declares the
same assets as one that succeeded. Whether a given output was actually
written is a different question, and these events do not answer it.

## `brokoli_job` (job facet)

Carries the pipeline's stable identifier.

```json
"job": {
  "namespace": "brokoli",
  "name": "Daily orders",
  "facets": {
    "brokoli_job": {
      "_producer": "https://github.com/Tnsor-Labs/brokoli",
      "_schemaURL": "https://github.com/Tnsor-Labs/brokoli/blob/main/docs/openlineage-facets.md",
      "pipelineId": "pipe-abc123"
    }
  }
}
```

| Field | Type | Meaning |
| --- | --- | --- |
| `pipelineId` | string | The pipeline's identifier, which does not change when the pipeline is renamed. |

The job is **named** by the pipeline's display name, because that is
what a person recognises in a catalogue. A display name can be edited,
and to a catalogue a renamed job is a new job with no history. The
stable id travels alongside so a consumer that cares about continuity
can stitch the two halves together.

The facet is omitted if the pipeline has no id.

## `brokoli_run` (run facet, `COMPLETE` only)

Carries the duration the engine measured.

```json
"run": {
  "runId": "run-abc123",
  "facets": {
    "brokoli_run": {
      "_producer": "https://github.com/Tnsor-Labs/brokoli",
      "_schemaURL": "https://github.com/Tnsor-Labs/brokoli/blob/main/docs/openlineage-facets.md",
      "durationMs": 4321
    }
  }
}
```

| Field | Type | Meaning |
| --- | --- | --- |
| `durationMs` | integer | Wall-clock milliseconds the run took, as the engine measured it. |

A catalogue can derive a duration by subtracting the `START` event's
time from the `COMPLETE` event's, but that also counts however long the
events took to reach the catalogue. This is the run itself.

## `errorMessage` (run facet, `FAIL` only)

The [standard facet](https://openlineage.io/spec/facets/1-0-0/ErrorMessageRunFacet.json),
so a catalogue renders it without knowing anything about Brokoli.

```json
"run": {
  "runId": "run-abc123",
  "facets": {
    "errorMessage": {
      "_producer": "https://github.com/Tnsor-Labs/brokoli",
      "_schemaURL": "https://openlineage.io/spec/facets/1-0-0/ErrorMessageRunFacet.json",
      "message": "connection refused"
    }
  }
}
```

Omitted when a run failed with no reason recorded, rather than sent with
an empty message. An absent facet says nothing; a facet holding an empty
string says the run failed for no stated reason, which is a worse
answer.

A `FAIL` event carries the same `inputs` and `outputs` as a success. A
catalogue that hears only about successes shows a broken pipeline as
healthy, and the failed run is usually the one somebody is looking for.

## What is not emitted yet

**Column-level lineage.** The `columnLineage` facet is not sent. Brokoli
derives column edges for its own lineage view, and
[ADR-039](adr/039-lineage-that-says-how-it-knows.md) covers how those
edges are derived and the evidence each carries. Edges below the
`declared` level are not fit for a catalogue: once a name-matched guess
is in a shared catalogue it is indistinguishable from a derivation
somebody proved, and it outlives any caveat attached to it.

**Any event at all for a run that did not go through the engine's run
lifecycle.** Emission is wired into that path and nowhere else.

## Implementing the interface yourself

`extensions.OpenLineageEmitter` in this repository is the seam. The
three methods receive the pipeline's id and display name, the run id,
and the datasets the run reads and writes as `[]extensions.LineageDataset`:

```go
type LineageDataset struct {
    ID   string // "file:/path" or "table:db.table"
    Type string // "file", "table" or "api"
    Name string // display name: a filename, a table name, a URL
}
```

The namespace is left to the emitter, which knows the deployment's
catalogue naming. `LineageDataset` carries only what the engine can
know.
