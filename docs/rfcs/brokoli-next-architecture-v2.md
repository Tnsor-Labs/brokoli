# RFC: Brokoli Next Architecture
## A Language-Neutral, Distributed Data Orchestrator with a Python-First SDK

**Status:** Draft for engineering review — living document, tracked in pieces (see below)
**Revision:** 2.0 — architecture-aware rewrite
**Primary audience:** Brokoli backend, SDK, connector, worker, and UI engineers
**Date:** August 2026

> **Implementation status (updated 2026-08-04):** This RFC is a proposal, not a spec of what already exists — read it as direction, not documentation. Large parts of it have since shipped; others haven't been started. Rather than editing this document in place as things land (which would make it hard to tell what was originally proposed vs. amended after the fact), each numbered section below is tracked through individual GitHub issues and ADRs as it's picked up:
>
> - **§5.1–§5.4, §5.12, §5.13, §7.3 (partial), §11.4 `union` (Phase 0)** — done: `Tnsor-Labs/brokoli#20`, `Tnsor-Labs/brokoli-sdk` Phase 0.
> - **§5.6, §14.1–§14.3 (pagination and response contracts)** — done: `Tnsor-Labs/brokoli-sdk#1`, `Tnsor-Labs/brokoli#30`.
> - **§5.7, §11 (dynamic node expansion, `.expand()`)** — done, with a documented design deviation from §11's original per-instance model: `Tnsor-Labs/brokoli-sdk#2`, `Tnsor-Labs/brokoli#31`.
> - **§5.8 (`union`/dataset ops)** — done: `Tnsor-Labs/brokoli#32`.
> - **§5.5 (code packaging), §19.1 (typed references)** — module-context packaging done (`Tnsor-Labs/brokoli-sdk#3`); `image=`/`runtime=` packaging modes not started.
> - **§5.9, §9, Phase 2 (artifact and dataset plane)** — not started. Proposed direction: [ADR-012](../adr/012-artifact-and-dataset-plane.md), tracked in a companion issue.
> - **§8, §20, Phase 3 connector-protocol pieces** — the core decision (a language-neutral subprocess protocol) already shipped as `ADR-002`–`ADR-005`, predating this RFC. What's still open (work-unit planning, structured progress, cancellation) is proposed in [ADR-013](../adr/013-connector-protocol-partition-progress-cancellation.md).
> - **§12, §13, §15, §16, §17, §18.2–§18.5, §21 (partition planner, external compute adapters, sensors, checkpointing beyond pagination, structured progress protocol, physical planner as a general concept, UI redesign)** — not started.
>
> If you're picking up a piece of this RFC, check whether an issue or ADR already exists before starting — search closed and open issues across this repo and `brokoli-sdk` first.

---

## 1. Executive summary

Brokoli already has the foundations of a modern orchestration platform:

- a Go backend that owns orchestration and runtime state;
- a Python SDK that compiles readable pipeline definitions;
- a visual editor generated from the same pipeline model;
- connector implementations that may be written in any language;
- built-in nodes and decorator-based custom nodes;
- remote deployment, validation, schedules, retries, and execution logs.

The next step is not to turn the Python SDK into a distributed compute engine.

The next step is to make the **Go backend understand richer execution intent** produced by the SDK and fulfilled by polyglot connectors and workers.

The central architectural rule should be:

> The SDK defines a logical pipeline.  
> The Go backend creates and manages the physical execution plan.

For example, an engineer should write one logical ingestion node:

```python
occurrences = source_api(
    "GBIF Occurrences",
    url="https://api.gbif.org/v1/occurrence/search",
    records="results",
    pagination=offset_pages(
        page_size=300,
        max_records=30_000,
        end_flag="endOfRecords",
    ),
)
```

At runtime, the Go backend may create 100 page instances, distribute them across workers, apply concurrency and rate limits, retry only failed pages, persist checkpoints, and combine the resulting partitions.

The engineer should not write ten nearly identical task functions or a 100-page loop.

This proposal recommends five major product changes:

1. **A versioned, language-neutral pipeline IR** shared by SDKs, backend, UI, and connectors.
2. **Dynamic runtime node instances** created by the Go scheduler from logical nodes.
3. **A first-class data and artifact plane** so large results move by reference rather than through orchestration metadata.
4. **A capability-based connector protocol** that works across languages.
5. **A simpler Python SDK** that expresses pagination, partitioning, mapping, aggregation, quality, and compute policies declaratively.

The intended differentiation is:

> Brokoli should provide the shortest path from readable Python to observable, scalable, polyglot data execution.

---

## 2. Confirmed current architecture

This revision is based on the architecture confirmed by the Brokoli team and on the current Python SDK code shared for review.

### 2.1 Go backend

The backend is written in Go and is responsible for the orchestration system.

The Go backend should remain the source of truth for:

- pipeline and run state;
- scheduling;
- retries;
- node readiness;
- dynamic expansion;
- worker and connector dispatch;
- progress events;
- cancellation;
- timeouts;
- persistence;
- API access;
- UI event delivery.

### 2.2 Python SDK

The Python SDK is an authoring and compilation layer.

It currently:

- provides the `Pipeline` context manager;
- registers logical nodes and edges;
- supports built-in nodes;
- supports decorators for custom code;
- validates pipeline definitions;
- serializes pipelines;
- deploys them to the backend.

It should not become the owner of distributed scheduling.

### 2.3 Polyglot connectors

Connectors may be written in any language.

Therefore, connector contracts must not depend on:

- Python classes;
- Python pickling;
- Python decorators;
- Python exceptions;
- Python-specific task-result objects.

The connector boundary must be language-neutral.

### 2.4 Visual editor

The UI is another client of the pipeline model.

The UI should render:

- the logical pipeline authored by the engineer;
- the physical execution created by the backend;
- connector capabilities;
- datasets and artifacts;
- progress;
- runtime instances;
- retries and failures;
- lineage and quality.

### 2.5 Existing SDK node model

The current built-in SDK functions register a node type, name, config, and upstream edges, returning a `NodeRef`.

This is a useful logical graph model and should be preserved, but expanded into a richer typed IR.

---

## 3. Product goal

Brokoli should make common pipelines dramatically shorter than comparable orchestrators while remaining suitable for serious workloads.

The product promise should be:

> Write the business flow once.  
> Brokoli handles pagination, partitioning, placement, retries, checkpoints, artifacts, and runtime visibility.

### 3.1 Simple first experience

```python
from brokoli import Pipeline, source_api, sink_file

with Pipeline("earthquake-watch", schedule="*/15 * * * *"):
    events = source_api(
        "USGS",
        url="https://earthquake.usgs.gov/earthquakes/feed/v1.0/"
            "summary/2.5_day.geojson",
        records="features",
    )

    events >> sink_file(
        "Archive",
        path="/data/earthquakes/{{ ds }}.parquet",
    )
```

### 3.2 Scalable experience without rewriting the pipeline

```python
events = source_api(
    "Global Events",
    url="https://api.example.com/events",
    records="data.items",
    pagination=cursor_pages(
        cursor_path="meta.next_cursor",
        cursor_param="cursor",
    ),
    execution=distributed(
        max_workers=32,
        retry_scope="page",
    ),
)
```

The second example adds execution intent, not orchestration boilerplate.

---

## 4. Architectural diagnosis

The current architecture is capable of defining and running small and medium DAGs, but several problems become visible when data volume, connector behavior, and runtime duration increase.

---

## 5. Current SDK and protocol flaws

## 5.1 Ambiguous source output contracts

A built-in API source may return:

- one JSON object represented as one row;
- a list of JSON objects;
- a nested object containing the real records;
- a scalar response;
- a file response.

The downstream task currently has to detect these shapes manually.

### Consequences

- Repeated defensive parsing.
- Hard-to-read pipelines.
- Runtime-only shape failures.
- Poor schema inference.
- UI previews cannot reliably identify the data.
- Connectors may behave differently for equivalent payloads.

### Required improvement

Sources must declare output kind and record selection explicitly:

```python
source_api(
    "USGS",
    url="...",
    response="dataset",
    records="features",
)
```

Or:

```python
source_api(
    "Create Export",
    url="...",
    response="scalar",
    value_path="downloadKey",
)
```

Or:

```python
source_api(
    "Download Archive",
    url="...",
    response="artifact",
)
```

---

## 5.2 Node identity is too dependent on string names

The runtime rejected a custom decorator source because it recognized only a hard-coded set of built-in source node type strings.

### Consequences

- SDK and backend can disagree.
- New connectors require changes in multiple validators.
- Custom nodes can deploy but fail at runtime.
- The server does not reason about capabilities.

### Required improvement

Every node specification must include capabilities:

```json
{
  "kind": "python.source",
  "capabilities": [
    "source",
    "dataset-output"
  ]
}
```

Validation should ask:

```text
Does the pipeline contain a node with capability "source"?
```

not:

```text
Is a node type exactly source_file, source_api, source_db, dbt, or migrate?
```

---

## 5.3 SDK/server validation drift

The SDK validates locally, but the backend can still reject equivalent semantics.

### Consequences

- "All checks passed" does not guarantee executable compatibility.
- Server upgrades and SDK upgrades may silently diverge.
- Connector schemas may differ between clients and runtime.

### Required improvement

Create one versioned schema package for:

- pipeline IR;
- node specifications;
- connector manifests;
- retry policy;
- schedules;
- resources;
- data contracts;
- progress events.

The Go backend owns the canonical protocol. SDK models should be generated from that protocol where practical.

---

## 5.4 Decorator wrappers and node instances are inconsistent

Some decorator wrappers can participate in a chain but cannot be reused as output references for fan-out.

### Consequences

- Symbols change meaning depending on syntax.
- `a >> decorator >> sink` may work while `decorator >> sink` fails.
- IDE typing is unclear.
- The graph API feels magical rather than predictable.

### Required improvement

Separate definitions from invocations:

```python
@task
def clean(rows):
    ...

clean        # TaskDef
cleaned = clean(raw)  # DatasetRef or NodeRef
```

Operator syntax should be equivalent:

```python
cleaned = raw >> clean
```

Both forms must return the same reusable reference.

---

## 5.5 Function source extraction loses project context

Custom task source is extracted and executed remotely without module-level constants and helpers.

### Consequences

- `NameError` for module constants.
- Repetition of imports and constants inside every task.
- Shared helpers are unsafe.
- Large functions replace modular code.
- Local tests may pass while remote execution fails.

### Required improvement

The SDK should deploy a **code package**, not only an isolated function string.

Supported execution packaging modes:

```python
@task(package="module")
def normalize(...):
    ...
```

```python
@task(image="registry.example.com/jobs:v12")
def train(...):
    ...
```

```python
@task(runtime="python:3.15", requirements=["pandas==3.0"])
def enrich(...):
    ...
```

The IR should reference:

- package digest;
- callable path;
- runtime type;
- dependency digest;
- optional container image.

The Go backend dispatches the correct runtime or connector.

---

## 5.6 Pagination is written as user business logic

The GBIF pipeline required:

- page loops;
- offsets;
- request construction;
- retries;
- `429` handling;
- sleeps;
- progress printing;
- deduplication;
- timeouts;
- manual chunking.

### Consequences

- Common ingestion becomes hundreds of lines.
- Every engineer invents a slightly different implementation.
- Retry granularity is too large.
- The backend cannot see pages as units of work.
- The UI cannot show reliable progress.
- Connector-specific rate limiting is not centrally enforced.

### Required improvement

Pagination belongs to the logical source specification and physical planner.

---

## 5.7 `parallel()` is graph syntax, not a workload model

The existing helper can build multi-node edges, but it does not define:

- data partitioning;
- runtime expansion;
- concurrency;
- collection;
- union;
- partial retry;
- ordering;
- backpressure;
- result manifests.

### Required improvement

Keep static fan-out, but add dynamic execution primitives:

```python
results = process.expand(items)
combined = results.collect()
```

and partitioned dataset transformations:

```python
clean = dataset.map_partitions(normalize)
```

---

## 5.8 No general union/concat primitive

Independent data partitions need to be combined without a relational join.

### Required improvement

Add:

```python
combined = union("Combine Pages", page_a, page_b, page_c)
```

For dynamic collections:

```python
combined = pages.collect(mode="union")
```

The backend should preferably represent this as a dataset manifest operation rather than copying every row.

---

## 5.9 Large data is treated like a normal task return value

Passing large row collections through ordinary task-result serialization does not scale.

### Consequences

- Memory duplication.
- Large JSON serialization.
- Control-plane database growth.
- Expensive retries.
- No data locality.
- No partition awareness.
- Worker and API limits become accidental data limits.

### Required improvement

Introduce a data plane and immutable references.

---

## 5.10 Retry scope is too coarse

A task fetching 100 pages can run for 15 minutes and then restart from page one.

### Required improvement

Retry must be configurable at the smallest meaningful unit:

```python
pagination=offset_pages(
    ...,
    retry_scope="page",
)
```

```python
execution=partitioned(
    retry_scope="partition",
)
```

The backend—not the SDK loop—owns those units.

---

## 5.11 Progress is informal

`print(..., flush=True)` helps, but it is not a runtime protocol.

### Required improvement

Every connector and worker runtime should emit structured progress events.

---

## 5.12 Built-in configuration omits falsy values

The current SDK helper omits optional values when they are falsy.

This can make valid values impossible to express:

```python
timeout=0
enabled=False
offset=0
ascending=False
```

### Required improvement

Optional-field handling must distinguish:

- omitted;
- explicitly `None`;
- valid falsy values.

Use a sentinel:

```python
UNSET = object()
```

or generated optional models.

---

## 5.13 Public package namespace collisions

A public decorator name can collide with an internal module of the same name.

### Required improvement

Internal package modules must not share names with exported callables unless the package explicitly protects those exports.

Prefer:

```text
brokoli.validation_engine
brokoli.decorators.validate
```

over:

```text
brokoli.validate module
brokoli.validate decorator
```

---

## 6. Target architecture

```text
┌───────────────────────────────────────────────────────────────┐
│ Authoring SDKs                                                │
│ Python first; future TypeScript, Go, or generated SDKs         │
└─────────────────────────────┬─────────────────────────────────┘
                              │ compile
                              ▼
┌───────────────────────────────────────────────────────────────┐
│ Versioned Logical Pipeline IR                                 │
│ nodes, edges, capabilities, schemas, policies, code refs       │
└─────────────────────────────┬─────────────────────────────────┘
                              │ deploy
                              ▼
┌───────────────────────────────────────────────────────────────┐
│ Go Control Plane                                              │
│ API, validation, scheduler, planner, state, leases, events     │
└──────────────┬────────────────────────────┬───────────────────┘
               │ plan                       │ metadata
               ▼                            ▼
┌──────────────────────────────┐  ┌─────────────────────────────┐
│ Physical Execution Plan      │  │ Data / Artifact Plane       │
│ stages, instances, partitions│  │ manifests, Parquet, files   │
└──────────────┬───────────────┘  └─────────────────────────────┘
               │ dispatch
               ▼
┌───────────────────────────────────────────────────────────────┐
│ Polyglot Connector and Worker Runtimes                        │
│ Go | Python | Rust | Java | containers | Spark | Ray | SQL     │
└───────────────────────────────────────────────────────────────┘
```

---

## 7. Versioned logical pipeline IR

The logical IR is the most important shared contract in Brokoli.

It must be independent of the authoring language and execution language.

### 7.1 Example logical node

```json
{
  "id": "gbif_occurrences",
  "kind": "source.http",
  "version": "2.0",
  "name": "GBIF Occurrences",
  "capabilities": [
    "source",
    "dataset-output",
    "pagination",
    "checkpointable"
  ],
  "connector": {
    "name": "http",
    "version": ">=2.1,<3"
  },
  "config": {
    "url": "https://api.gbif.org/v1/occurrence/search",
    "response": {
      "kind": "dataset",
      "records_path": "results"
    },
    "pagination": {
      "strategy": "offset",
      "page_size": 300,
      "max_records": 30000,
      "end_flag_path": "endOfRecords"
    }
  },
  "execution": {
    "max_concurrency": 8,
    "retry_scope": "page"
  }
}
```

### 7.2 IR requirements

The IR must support:

- semantic node kinds;
- connector references;
- input and output kinds;
- schemas;
- capabilities;
- resource requirements;
- timeout and retry policies;
- dynamic expansion policies;
- partitioning;
- code package references;
- secret and connection references;
- caching;
- checkpoints;
- materialization;
- progress dimensions;
- protocol version.

### 7.3 Capability negotiation

On deployment, the server should return:

```json
{
  "protocol_version": "2.3",
  "supported_capabilities": [
    "dynamic-map",
    "artifact-ref",
    "offset-pagination",
    "partition-retry"
  ],
  "connectors": {
    "http": ["2.1.0"],
    "postgres": ["3.0.2"]
  }
}
```

The CLI should fail before deployment with a precise message when the server cannot execute the plan.

---

## 8. Polyglot connector protocol

Connectors need a formal protocol rather than implementation-language assumptions.

## 8.1 Connector manifest

```yaml
name: http
version: 2.1.0
runtime: go
entrypoint: /connectors/http
capabilities:
  - source
  - sink
  - pagination
  - streaming
  - checkpoint
inputs:
  config_schema: schemas/http-config.json
outputs:
  - scalar
  - dataset
  - artifact
progress:
  dimensions:
    - pages
    - rows
    - bytes
```

## 8.2 Connector invocation

The Go backend may support one or more transport adapters:

- in-process Go interface for trusted native connectors;
- gRPC for long-running external connector services;
- local process protocol over stdin/stdout;
- container job protocol;
- HTTP callback protocol for remote connectors.

The logical connector contract should remain the same.

## 8.3 Suggested connector lifecycle

```text
Describe
Validate
Plan
Start
Heartbeat / Progress
Checkpoint
Complete
Cancel
Resume
```

### Describe

Returns capabilities and schemas.

### Validate

Checks configuration without running the workload.

### Plan

Optionally returns partitions or physical work units.

### Start

Executes a work unit.

### Heartbeat / Progress

Emits structured state.

### Checkpoint

Returns durable resume information.

### Complete

Returns output references and metrics.

### Cancel

Requests cancellation.

### Resume

Continues from checkpoint state.

## 8.4 Generated connector SDKs

Define the protocol in Protobuf or another IDL and generate language bindings for:

- Go;
- Python;
- Rust;
- Java;
- TypeScript if needed.

A connector author should implement an interface, not manually construct undocumented JSON.

---

## 9. Data kinds and references

The IR should distinguish control values from data values.

## 9.1 Scalar

```json
{
  "kind": "scalar",
  "value": "download-key-123"
}
```

Only small values should be inline.

## 9.2 Artifact

```json
{
  "kind": "artifact",
  "uri": "s3://brokoli-runs/123/export.zip",
  "media_type": "application/zip",
  "size_bytes": 819200000,
  "checksum": "sha256:..."
}
```

## 9.3 Dataset

```json
{
  "kind": "dataset",
  "manifest_uri": "s3://brokoli-runs/123/dataset/manifest.json",
  "format": "parquet",
  "schema": {
    "fields": []
  },
  "partitions": 128,
  "rows": 42000000,
  "size_bytes": 8400000000
}
```

## 9.4 Dynamic collection

```json
{
  "kind": "collection",
  "element_kind": "artifact",
  "manifest_uri": "s3://brokoli-runs/123/files.json"
}
```

## 9.5 Threshold-based automatic spill

Small outputs may remain inline.

Large outputs must automatically spill to configured storage.

Example policy:

```yaml
inline_max_bytes: 1048576
inline_max_rows: 1000
dataset_default_format: parquet
artifact_store: s3
```

The exact thresholds should be configurable per deployment.

---

## 10. Logical nodes and physical instances

The UI and backend must distinguish two concepts.

### Logical node

The node authored in Python:

```text
Fetch GBIF Occurrences
```

### Physical instances

Runtime work created by the planner:

```text
Fetch GBIF Occurrences [offset=0]
Fetch GBIF Occurrences [offset=300]
Fetch GBIF Occurrences [offset=600]
...
```

The logical graph remains small and understandable.

---

## 11. Dynamic node instances

## 11.1 User API

```python
files = list_files("s3://incoming/")

@task
def parse(file):
    ...

parsed = parse.expand(file=files)
```

## 11.2 Runtime model

The Go backend:

1. resolves the upstream collection;
2. creates stable instance keys;
3. enforces an expansion limit;
4. schedules instances;
5. tracks each instance separately;
6. retries only failed instances;
7. produces a collection manifest.

## 11.3 Stable identity

Dynamic instances require deterministic keys:

```python
parse.expand(
    file=files,
    key=lambda file: file.checksum,
)
```

If no key is provided, the planner may use collection index plus input digest.

## 11.4 Collection operations

```python
parsed.wait()
parsed.collect()
parsed.union()
parsed.reduce(merge)
```

These operations should preferably manipulate manifests, not load all values into the scheduler.

---

## 12. Distributed partition execution

Dynamic task mapping and distributed data processing are related but not identical.

### Dynamic task mapping

One runtime task per logical item:

```text
one file → one task instance
```

### Partition execution

A dataset is split into physical partitions:

```text
many records → partition blocks → worker tasks
```

Brokoli should support both.

## 12.1 Dataset API

```python
clean = (
    records
    .filter(valid_coordinates)
    .map(normalize_occurrence)
)
```

## 12.2 Partition policy

```python
clean = records.map_partitions(
    normalize_batch,
    execution=partitioned(
        target_size="128MiB",
        max_workers=32,
        retry_scope="partition",
    ),
)
```

## 12.3 Physical planning

The Go planner can:

- split source artifacts;
- use connector-provided partitions;
- preserve existing Parquet partitions;
- create API page work units;
- assign partitions to worker pools;
- fuse compatible transformations;
- materialize shuffle boundaries;
- retry failed partitions.

---

## 13. Brokoli should orchestrate compute, not recreate every compute engine

Brokoli needs a native partition executor for common jobs, but it should also delegate specialized workloads.

## 13.1 Native Brokoli executor

Appropriate for:

- API pagination;
- file-level map;
- moderate batch transformation;
- connector work;
- simple map/filter;
- data movement;
- validation;
- notifications.

## 13.2 External execution adapters

Appropriate integrations:

- Spark for heavy SQL, shuffle, and very large distributed transformations;
- Ray for distributed Python and ML workloads;
- Kubernetes Jobs for isolated container execution;
- dbt and SQL warehouses for pushdown transformations;
- serverless batch systems;
- custom enterprise worker pools.

## 13.3 SDK example

```python
features = raw.map_partitions(
    compute_features,
    compute="ray-gpu",
)
```

```python
summary = raw.sql(
    "SELECT country, COUNT(*) FROM data GROUP BY country",
    compute="spark-prod",
)
```

The Go backend remains the orchestrator and state owner.

---

## 14. Native ingestion primitives

## 14.1 Response extraction

```python
source_api(
    "Events",
    url="...",
    records="data.results",
)
```

## 14.2 Query parameters

```python
source_api(
    "Events",
    url="...",
    params={
        "from": "{{ ds }}",
        "limit": 1000,
    },
)
```

This is clearer than requiring users to concatenate URLs.

## 14.3 Pagination strategies

```python
offset_pages(
    offset_param="offset",
    limit_param="limit",
    page_size=300,
    max_records=30_000,
)
```

```python
cursor_pages(
    cursor_param="cursor",
    cursor_path="meta.next_cursor",
)
```

```python
numbered_pages(
    page_param="page",
    start=1,
    total_pages_path="meta.pages",
)
```

```python
next_link_pages(
    next_path="links.next",
)
```

```python
link_header_pages(rel="next")
```

## 14.4 Pagination execution policy

```python
pagination=offset_pages(...).with_execution(
    max_concurrency=8,
    requests_per_second=2,
    retry_scope="page",
    checkpoint_every=10,
    deduplicate_by="key",
)
```

## 14.5 Asynchronous external jobs

```python
export = external_job(
    "Request Export",
    submit=http_post(...),
    status=http_poll(
        url=".../{job_id}",
        state_path="status",
        success=["SUCCEEDED"],
        failure=["FAILED", "CANCELLED"],
        interval=30,
    ),
    result=artifact_from(path="downloadLink"),
)
```

This is essential for GBIF bulk downloads, cloud exports, ML training services, and warehouse unload jobs.

---

## 15. Deferrable waiting

A sensor should not occupy a worker process while sleeping.

```python
@sensor(
    mode="deferred",
    interval=30,
    timeout="4h",
)
def export_ready(status):
    return status == "SUCCEEDED"
```

The Go scheduler should persist:

- next evaluation time;
- external job ID;
- timeout deadline;
- checkpoint state.

---

## 16. Checkpointing

## 16.1 Connector checkpoint

An HTTP pagination connector may checkpoint:

```json
{
  "next_offset": 15000,
  "completed_pages": 50,
  "dataset_manifest": "s3://.../partial-manifest.json"
}
```

## 16.2 Task checkpoint

A task runtime may expose:

```python
def process(ctx, partitions):
    for partition in partitions:
        ...
        ctx.checkpoint({
            "last_partition": partition.key,
        })
```

## 16.3 Resume behavior

On retry:

- completed partitions remain complete;
- the connector resumes from its checkpoint;
- the run history shows resumed work;
- duplicated external requests are avoided.

---

## 17. Structured progress protocol

A polyglot progress event should look like:

```json
{
  "type": "progress",
  "run_id": "run-123",
  "logical_node_id": "gbif",
  "instance_id": "gbif[offset=6000]",
  "current": 21,
  "total": 100,
  "unit": "pages",
  "rows_in": 0,
  "rows_out": 6300,
  "bytes_in": 50200000,
  "bytes_out": 31800000,
  "rate": 1.7,
  "message": "Fetched offset 6000",
  "timestamp": "2026-08-02T15:00:00Z"
}
```

## 17.1 Automatic runtime metrics

Workers and connectors should emit:

- queued duration;
- execution duration;
- page count;
- row count;
- byte count;
- rows per second;
- bytes per second;
- CPU;
- memory;
- retries;
- rate-limit waits;
- checkpoint count;
- spill bytes;
- output partitions.

## 17.2 Heartbeats

Heartbeats must be separate from progress.

A node can be alive without advancing, such as while waiting on an external system.

The UI should distinguish:

```text
Running and progressing
Running but rate limited
Running and externally waiting
Running without recent progress
Worker heartbeat lost
```

---

## 18. Go backend changes

The backend remains the core of Brokoli Next.

## 18.1 Logical plan validator

Responsibilities:

- protocol compatibility;
- graph integrity;
- connector availability;
- capability validation;
- data-kind compatibility;
- schema compatibility;
- policy validation;
- expansion safety;
- resource-policy validation.

## 18.2 Physical planner

Responsibilities:

- pagination expansion;
- dataset partition discovery;
- dynamic instance expansion;
- stage creation;
- task fusion;
- executor selection;
- materialization boundaries;
- retry scopes;
- concurrency and rate-limit plans.

## 18.3 Scheduler

Responsibilities:

- instance readiness;
- worker leases;
- heartbeats;
- checkpoint state;
- partial retry;
- cancellation;
- concurrency limits;
- rate limiting;
- backpressure;
- fairness;
- tenancy quotas.

## 18.4 Runtime instance state

Suggested state model:

```text
PENDING
PLANNED
QUEUED
LEASED
RUNNING
DEFERRED
CHECKPOINTED
RETRY_WAIT
SUCCEEDED
FAILED
CANCELLED
LOST
SKIPPED
```

## 18.5 Worker lease model

A worker receives a time-bound lease.

The lease contains:

- node instance ID;
- connector/runtime;
- input references;
- config;
- checkpoint;
- resource policy;
- progress endpoint;
- cancellation token.

Workers renew leases through heartbeats.

## 18.6 Event log

Persist append-only events for:

- state transitions;
- progress;
- metrics;
- checkpoints;
- retries;
- connector messages;
- artifacts;
- quality results.

Current run state can be projected from events or maintained as a materialized view.

---

## 19. Python SDK redesign

The SDK should become simpler while producing richer IR.

## 19.1 Typed references

```python
ScalarRef[T]
ArtifactRef
DatasetRef[T]
CollectionRef[T]
```

These can remain subclasses or wrappers around a common logical reference.

## 19.2 Predictable invocation

```python
@task
def clean(rows):
    ...

cleaned = clean(raw)
```

## 19.3 Predictable chaining

```python
cleaned = raw >> clean
```

Both produce the same reference.

## 19.4 Data operations

```python
clean = raw.map(normalize)
valid = clean.filter(is_valid)
summary = valid.group_by("country").aggregate(...)
```

The SDK may compile supported expressions into connector-native operations or task stages.

## 19.5 Explicit escape hatch

```python
@task
def custom(rows):
    ...
```

Custom Python remains available, but common operations should not require it.

## 19.6 Strong defaults

A short pipeline should not require:

- node IDs;
- manual layout;
- repeated timeout settings;
- explicit serialization;
- page loops;
- data-shape extraction;
- worker configuration;
- connector plumbing.

## 19.7 Avoid excessive API styles

Choose one canonical documented style.

Recommended:

```python
source = source_api(...)
clean = transform(source)
clean >> sink_file(...)
```

Fluent dataset methods may be offered for data operations, but should compile into the same IR.

---

## 20. Connector developer experience

A connector developer should receive:

- generated protocol types;
- a local test harness;
- conformance tests;
- schema validation;
- mock control-plane server;
- progress and checkpoint helpers;
- packaging tools;
- compatibility testing;
- connector manifest generation.

Example conceptual Python connector:

```python
class HttpConnector(Connector):
    def describe(self) -> ConnectorDescription:
        ...

    def validate(self, request: ValidateRequest) -> ValidateResponse:
        ...

    def plan(self, request: PlanRequest) -> PlanResponse:
        ...

    def run(self, request: RunRequest, ctx: ConnectorContext):
        for page in pages:
            ...
            ctx.progress(...)
            ctx.checkpoint(...)
            yield partition
```

Equivalent interfaces should be available in other languages.

---

## 21. UI redesign

## 21.1 Logical graph by default

The default graph must remain the graph the engineer wrote.

```text
GBIF Source → Normalize → Validate → Aggregate → Publish
```

## 21.2 Physical plan on demand

Expand the GBIF node:

```text
100 page instances
8 concurrent
98 complete
1 retrying
1 queued
```

## 21.3 Node card information

```text
GBIF Occurrences
29,400 / 30,000 records
98 / 100 pages
2.1 pages/s
8 active workers
1 retry
ETA 00:00:14
```

## 21.4 Artifact and dataset view

Display:

- format;
- schema;
- rows;
- bytes;
- partitions;
- storage URI;
- checksum;
- producer;
- consumers;
- preview;
- quality status;
- retention.

## 21.5 Instance table

Columns:

- instance key;
- status;
- worker;
- attempt;
- duration;
- rows;
- bytes;
- checkpoint;
- error.

## 21.6 Retry controls

Allow:

- retry failed instances;
- retry one partition;
- resume from checkpoint;
- rerun logical node;
- rerun downstream;
- cancel outstanding instances.

## 21.7 Connector detail

Display:

- connector name and version;
- capabilities;
- runtime language;
- configuration schema;
- resource policy;
- progress dimensions.

## 21.8 Planner explanation

Before execution:

```text
Logical nodes: 6
Physical stages: 9
Expected page instances: 100
Maximum concurrent requests: 8
Estimated output: 30,000 rows
Artifact format: Parquet
Executor: native HTTP connector
```

---

## 22. Competitive analysis

Brokoli should learn from existing systems without copying their complexity.

---

## 22.1 Apache Airflow

### Relevant strengths

Airflow supports dynamic task mapping, where the scheduler creates task instances at runtime based on upstream data. It also supports map/reduce patterns and multiple execution backends.

### Relevant limitations

Airflow remains primarily a task orchestrator. Large datasets are generally moved through external stores rather than normal task metadata. Authoring integrations and infrastructure can become verbose.

### Brokoli opportunity

Brokoli can combine dynamic mapping with native dataset and artifact references, making the external-storage pattern an explicit first-class model rather than a user convention.

---

## 22.2 Prefect

### Relevant strengths

Prefect provides approachable Python task functions, `.submit()`, `.map()`, task runners, and work pools that bridge orchestration and runtime infrastructure.

### Relevant limitations

Distributed execution often requires selecting and configuring task runners or external infrastructure. Data partitions and artifact lineage are not the sole center of its programming model.

### Brokoli opportunity

Provide comparable Python ergonomics while making partitioning, data references, connector capabilities, and visual data lineage native.

---

## 22.3 Dagster

### Relevant strengths

Dagster emphasizes software-defined assets, lineage, observability, declarative modeling, and testability.

### Relevant limitations

Its asset and partition abstractions can introduce a substantial framework vocabulary for new users.

### Brokoli opportunity

Keep asset-quality observability while allowing simple pipelines to begin as direct data flow.

---

## 22.4 Kestra

### Relevant strengths

Kestra has a distributed executor/worker split, internal storage for files passed between tasks, a plugin ecosystem, and support for scripts in multiple languages.

### Relevant limitations

Workflow authoring is primarily declarative configuration, while plugins are centered on its own ecosystem and implementation model.

### Brokoli opportunity

Use the same strong separation between orchestration, workers, plugins, and internal storage, while retaining a superior Python-first authoring experience and a language-neutral connector protocol.

---

## 22.5 Apache Spark

### Relevant strengths

Spark models data as partitions and schedules work per partition. It supports transformations, shuffle boundaries, caching, repartitioning, and recomputation.

### Relevant limitations

Spark is a distributed compute engine, not a complete low-ceremony orchestration product for APIs, notifications, sensors, and heterogeneous integrations.

### Brokoli opportunity

Adopt partition semantics and physical planning, but delegate heavy shuffle workloads to Spark rather than rebuilding Spark immediately.

---

## 22.6 Ray

### Relevant strengths

Ray Data provides distributed datasets, batch transformations, task and actor pools, CPU/GPU resource controls, and streaming execution.

### Relevant limitations

Ray is focused on distributed Python and AI workloads, not language-neutral connector orchestration and broad integration workflows.

### Brokoli opportunity

Use Ray as one optional execution target while Brokoli remains the polyglot orchestrator and UI.

---

## 22.7 Temporal

### Relevant strengths

Temporal provides durable execution and recovery across crashes and infrastructure outages.

### Relevant limitations

Temporal is a general durable application workflow system rather than a data-native visual pipeline platform.

### Brokoli opportunity

Bring durable state, external job IDs, checkpoints, heartbeats, and resumability into a data-specific model with datasets, artifacts, and connectors.

---

## 23. Competitive positioning

Brokoli should not market itself as "Spark implemented in Go" or "Airflow with a nicer UI."

A stronger positioning is:

> Brokoli is a Python-first, language-neutral data orchestrator that automatically turns readable logical pipelines into observable distributed execution.

### Differentiators

1. Python-first authoring with minimal ceremony.
2. Go control plane for efficient orchestration.
3. Polyglot connector protocol.
4. Logical-versus-physical graph visibility.
5. Native datasets and artifacts.
6. Runtime node expansion.
7. Partition-level retry and checkpoints.
8. Native pagination and external-job orchestration.
9. Pluggable execution targets.
10. One shared protocol across SDK, backend, connectors, and UI.

---

## 24. GBIF pipeline before and after

## 24.1 Current workaround

The user must manually implement:

- 100 requests;
- page ranges;
- retries;
- sleeps;
- timeouts;
- deduplication;
- progress;
- ten chunk nodes;
- cumulative outputs.

## 24.2 Proposed SDK

```python
from brokoli import Pipeline, source_api

with Pipeline("gbif-monitor", schedule="0 2 * * *"):
    occurrences = source_api(
        "GBIF Occurrences",
        url="https://api.gbif.org/v1/occurrence/search",
        params={
            "hasCoordinate": True,
            "occurrenceStatus": "PRESENT",
        },
        records="results",
        pagination=offset_pages(
            page_size=300,
            max_records=30_000,
            end_flag="endOfRecords",
        ),
        execution=distributed(
            max_workers=8,
            retry_scope="page",
            rate_limit="2/second",
        ),
    )

    normalized = occurrences.map(normalize_occurrence)
    valid = normalized.filter(valid_coordinates)

    valid.write.parquet(
        "/data/gbif/{{ run.id }}/",
        partition_by="country_code",
    )

    (
        valid
        .group_by("country_code")
        .aggregate(
            occurrences=count(),
            species=count_distinct("species"),
        )
        .write.csv("/reports/gbif_country_summary.csv")
    )
```

## 24.3 Backend physical plan

```text
Stage 1: HTTP pagination
    100 page instances
    max concurrency 8
    retry by page
    output: 100 Parquet partitions

Stage 2: Normalize + filter
    fused partition transformation
    100 partition instances

Stage 3: Materialize
    country-partitioned dataset

Stage 4: Aggregate
    native aggregate, SQL engine, or Spark depending on size

Stage 5: Publish
    report artifact
```

---

## 25. Implementation roadmap

## Phase 0 — Protocol and correctness

### Work

- Version pipeline IR.
- Add capability-based node validation.
- Add SDK/server capability negotiation.
- Fix decorator wrapper versus instance behavior.
- Fix public namespace collisions.
- Replace falsy optional handling with explicit `UNSET`.
- Add data-kind declarations.
- Add `union`.
- Define connector manifest.

### Acceptance criteria

- Every documented decorator executes.
- SDK and backend agree on source/sink capabilities.
- Fan-out works consistently.
- Explicit `False` and `0` config values serialize correctly.
- Deployment rejects unsupported protocol features before pipeline creation.

---

## Phase 1 — Source ergonomics and progress

### Work

- `records` and `value_path`.
- Query `params`.
- Pagination strategies.
- Structured rate limits.
- Structured progress events.
- Heartbeats.
- Page-level retry.
- Page checkpoints.
- UI page progress.

### Acceptance criteria

- The GBIF search example contains no page loop.
- A failed page retries independently.
- The UI shows pages, records, rate, retry, and ETA.
- A worker loss resumes from the last checkpoint.

---

## Phase 2 — Artifact and dataset plane

### Work

- Artifact store interface.
- `ArtifactRef`.
- `DatasetRef`.
- Dataset manifests.
- Parquet and Arrow support.
- Automatic spill.
- Schema, rows, bytes, and checksum tracking.
- Retention policy.
- Dataset and artifact UI.

### Acceptance criteria

- A multi-gigabyte result does not pass through control-plane JSON.
- Downstream retries reuse existing upstream artifacts.
- The UI previews and downloads outputs from artifact storage.
- Data lineage includes partitions and schema.

---

## Phase 3 — Code packages and polyglot runtime protocol

### Work

- Module/package deployment.
- Runtime packages and images.
- Connector protocol IDL.
- Go/Python/Rust/Java connector SDKs.
- Conformance tests.
- Local connector harness.
- Runtime cancellation and checkpoint APIs.

### Acceptance criteria

- Python tasks can use module constants and helpers.
- Connectors in two different languages pass the same conformance suite.
- The backend invokes them through one logical contract.

---

## Phase 4 — Dynamic instances

### Work

- `.expand()`.
- Stable instance keys.
- Dynamic collection manifests.
- Instance-level state.
- Partial retries.
- Concurrency limits.
- Backpressure.
- Logical/physical UI.
- Retry selected instances.

### Acceptance criteria

- One logical node can create thousands of physical instances.
- The scheduler does not load the entire result collection into memory.
- A failed instance can be rerun independently.

---

## Phase 5 — Partition planner and execution adapters

### Work

- Partitioned datasets.
- `map_partitions`.
- `repartition`.
- `coalesce`.
- `group_by`.
- `aggregate`.
- `union`.
- Materialization stages.
- Native worker executor.
- Kubernetes executor.
- Spark adapter.
- Ray adapter.
- SQL pushdown.

### Acceptance criteria

- The same logical pipeline can run on native workers or Spark.
- The UI explains the selected executor.
- Failed partitions retry independently.
- Data transfers occur through manifests and artifacts.

---

## Phase 6 — Optimization

### Work

- Operation fusion.
- Projection pushdown.
- Predicate pushdown.
- Partition pruning.
- Cost estimates.
- Historical runtime estimates.
- Adaptive concurrency.
- Shuffle planning.
- Streaming and incremental execution.

---

## 26. Compatibility and migration

Existing pipelines should remain valid.

### Compatibility layer

The backend can translate current node JSON into the new IR:

```text
source_api → source.http capability set
sink_file  → sink.file capability set
code       → runtime.code capability set
```

### SDK deprecation policy

- Introduce new APIs without immediately removing old signatures.
- Emit warnings for ambiguous behavior.
- Provide automated migration guidance.
- Maintain server support for older protocol versions for a defined period.
- Include SDK and protocol versions in every deployment.

---

## 27. Security and governance

A distributed connector model requires explicit controls.

### Connector trust levels

```text
native-trusted
signed-plugin
isolated-container
remote-service
untrusted-code
```

### Required controls

- signed connector manifests;
- image allowlists;
- secret scoping;
- network policies;
- workspace isolation;
- resource quotas;
- connector permissions;
- audit events;
- output retention;
- artifact encryption;
- sandboxing for custom code;
- per-tenant concurrency and rate limits.

The Go backend should enforce policy. SDK options are requests, not authority.

---

## 28. Success metrics

### SDK productivity

- Lines of code for representative pipelines.
- Time to first deployed pipeline.
- Percentage of pipelines needing custom pagination code.
- Percentage of tasks using duplicated helper code.

### Runtime reliability

- Work reused after retry.
- Page/partition retry success.
- Duplicate external job submissions avoided.
- Worker-loss recovery time.
- Checkpoint resume rate.

### Scale

- Maximum dataset size without control-plane growth.
- Dynamic instances scheduled per second.
- Partition throughput.
- Artifact throughput.
- Worker utilization.

### Observability

- Percentage of long-running nodes with structured progress.
- Time to identify the failing instance.
- Percentage of outputs with row count, bytes, schema, and lineage.
- Accuracy of progress and ETA.

---

## 29. Non-goals

Brokoli should not initially:

- rebuild the complete Spark SQL engine;
- create a proprietary distributed filesystem;
- promise automatic parallelization for arbitrary Python;
- promise exactly-once semantics where connectors cannot provide them;
- require every connector to run in-process in Go;
- hide planner decisions so completely that performance becomes unpredictable;
- replace specialized systems that can be cleanly orchestrated.

---

## 30. Immediate engineering decisions

The team should decide:

1. Is logical-versus-physical execution a core product concept?
2. What is the canonical pipeline IR format and versioning policy?
3. What data size is allowed inline?
4. Which artifact store abstraction will be implemented first?
5. What connector transport is canonical?
6. What capabilities must every connector expose?
7. What is the first dynamic workload: API pages, files, or explicit `.expand()`?
8. How are checkpoints persisted?
9. How will the UI represent thousands of instances?
10. Which first external executor should be supported after native workers?

---

## 31. Recommended first milestone

The first milestone should be narrow enough to ship but powerful enough to prove the architecture.

### "Distributed HTTP Source"

Deliver:

- `records`;
- query `params`;
- offset and cursor pagination;
- runtime page instances;
- page-level retry;
- checkpointing;
- structured progress;
- rate limiting;
- dataset manifest output;
- logical/physical UI.

This milestone directly solves the problems found in the GBIF pipeline and validates:

- richer SDK compilation;
- Go physical planning;
- dynamic node instances;
- polyglot connector progress;
- artifact-backed datasets;
- UI expansion.

It is a better first step than attempting general Spark-like transformations immediately.

---

## 32. Final recommendation

Brokoli should preserve its current strengths:

- Go orchestration core;
- Python-first SDK;
- polyglot connectors;
- visual pipelines;
- simple graph syntax.

The next generation should add a stronger contract between those layers.

The Python SDK should describe **what** the engineer wants.

The Go backend should determine **how** to execute it safely and efficiently.

Connectors should expose capabilities through a language-neutral protocol.

The UI should show both the simple logical pipeline and the real physical work.

Large data should move as datasets and artifacts, not as oversized task return values.

With that architecture, Brokoli can offer something meaningfully stronger than a conventional DAG scheduler:

> A low-code-in-Python, high-visibility, polyglot distributed data orchestrator.

---

## 33. External references

- Apache Airflow, Dynamic Task Mapping  
  https://airflow.apache.org/docs/apache-airflow/stable/authoring-and-scheduling/dynamic-task-mapping.html

- Prefect, Task Runners  
  https://docs.prefect.io/v3/concepts/task-runners

- Prefect, Work Pools  
  https://docs.prefect.io/v3/concepts/work-pools

- Dagster documentation  
  https://docs.dagster.io/

- Kestra architecture  
  https://kestra.io/docs/architecture

- Kestra server components  
  https://kestra.io/docs/architecture/server-components

- Apache Spark RDD API  
  https://spark.apache.org/docs/latest/api/java/org/apache/spark/rdd/RDD.html

- Ray Data  
  https://docs.ray.io/en/latest/data/data.html

- Temporal documentation  
  https://docs.temporal.io/
