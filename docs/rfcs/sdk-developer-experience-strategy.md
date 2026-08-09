# RFC: Brokoli SDK developer experience strategy

**Status:** Draft for engineering review
**Date:** 2026-08-09
**Scope:** Python SDK, Go control plane, connector runtime, CLI, and UI contracts
**Tracking:** [brokoli#90](https://github.com/Tnsor-Labs/brokoli/issues/90), [brokoli-sdk#15](https://github.com/Tnsor-Labs/brokoli-sdk/issues/15)

## 1. Purpose

This document turns the Brokoli Next architecture RFC into a developer-experience roadmap grounded in the code that exists now.

The product goal is not to win by accumulating more concepts than Apache Airflow, Dagster, or Prefect. It is to make common data work shorter to author, safer to deploy, easier to test, and more transparent at runtime while preserving Brokoli's architectural advantage:

> Python describes the logical data flow. The Go control plane validates, plans, schedules, and observes physical execution.

That boundary is non-negotiable. The SDK must not become a second scheduler, and the backend must not require users to author physical work units by hand.

## 2. Current product truth

The repository has moved beyond the initial Phase 0 described in the original RFC. The following capabilities are real today.

### 2.1 Shipped end to end

- Pipeline IR `2.0` and node capability annotations.
- `GET /api/capabilities` on the Go server.
- Static DAG execution with run- and node-level retries and timeouts.
- Built-in database, file, HTTP, transform, join, quality, notification, dbt, and migration nodes.
- Decorator-based Python code nodes with module-context packaging.
- HTTP response contracts, record extraction, query parameters, pagination strategies, rate limits, page retries, and pagination checkpoints.
- Run snapshots, durable run events, execution-attempt storage, recovery, cancellation, and scheduler leadership.
- Artifact references, local content-addressed storage, real HTTP artifact responses, and automatic spill for large intermediate results.
- A language-neutral subprocess plugin protocol with manifests and structured progress messages.

### 2.2 Partial implementations

- Dynamic expansion records instances, but executes them sequentially inside one logical node and retries the whole expansion.
- `union()` and `CollectionRef.collect(mode="union")` execute by combining ordinary in-memory datasets, not dataset manifests.
- Typed SDK references classify authoring-time outputs, but they are not runtime data-reference envelopes.
- Large intermediate values spill to local storage, but node handlers still exchange `*common.DataSet` and distributed workers need shared or object storage.
- Plugin progress is structured, but currently reaches the engine in a batch after process completion rather than as live durable progress.
- Durable node-attempt claim and lease interfaces exist, but normal node and plugin dispatch do not use them.
- The capabilities endpoint exists, but the SDK does not yet enforce it before deployment and it does not describe installed plugin schemas or all execution features.

### 2.3 Compile-only SDK surface

- `DatasetRef.map()` and `.filter()` emit partition-oriented nodes, but the SDK serializes only function name/documentation references. Normal SDK output therefore contains no runnable script for the backend's whole-dataset fallback.
- Stable expansion key callables are represented by name and documentation, not executable key logic.

Compile-only APIs are useful for contract design, but they must not be presented as generally available runtime features.

### 2.4 Known correctness and developer-experience gaps

- Node IDs contain randomness, so unchanged pipeline source produces noisy IR diffs.
- `Pipeline._current` is a process-global context and is unsafe for nested, threaded, or asynchronous authoring.
- CLI discovery imports and executes pipeline modules, including arbitrary top-level side effects.
- Some accepted pipeline settings are stored by the SDK but not serialized into IR.
- Condition branch metadata is collected in memory but not represented clearly in the serialized graph.
- Pipeline create/update use less validation than execution, and import can bypass important validation.
- Unknown backend node types can pass input through instead of failing closed.
- The SDK CLI deploys and validates, but cannot run, inspect, watch, retry, cancel, backfill, diff, or pull.
- SDK validation and backend validation are still handwritten in separate repositories.
- The SDK has no CI matrix, formatter/linter policy, static type check, package check, or `py.typed` marker.
- SDK examples and README claims have drifted from files and behavior that actually exist.

## 3. Lessons from other orchestrators

The comparison is about proven user expectations, not feature-count marketing.

### 3.1 Apache Airflow

Airflow 3.3 demonstrates several mature contracts Brokoli should match:

- TaskFlow makes decorated functions callable and wires return values without manual XCom handling.
- Dynamic Task Mapping creates scheduler-owned runtime task instances, supports repeated mapping, reduce, named instances, limits, zero-length maps, zip, and concat.
- Runtime environments include virtualenv, external Python, Docker, and Kubernetes options.
- Backfill, rerun, CLI debugging, object-storage XCom backends, and deferrable work are first-class operating concepts.

Brokoli should learn from the safety limits and runtime visibility, not copy Airflow's operator/provider ceremony or use metadata values as an accidental large-data plane.

### 3.2 Dagster

Dagster's strongest lessons are data identity and testability:

- Assets give durable data products identity, lineage, metadata, quality checks, partitions, and automation rules.
- IO managers separate computation from storage and retrieval.
- Structured resources and config make external dependencies explicit and testable.
- Partitions and backfills operate on logical slices of data rather than only whole task runs.

Brokoli should adopt explicit data contracts, materialization metadata, partition identity, and focused local tests. It should not require every new user to learn an asset-centric vocabulary before writing a simple source-to-sink flow.

### 3.3 Prefect

Prefect's strongest lessons are Python ergonomics and operational flexibility:

- Flows and tasks behave like normal synchronous or asynchronous functions.
- Function type hints become validated parameter schemas.
- Deployments separate flow identity from schedules, parameters, versions, and infrastructure.
- Work pools expose governed infrastructure choices without hard-coding one execution environment into flow source.
- Local invocation, remote deployment, retries, timeouts, nested flows, and operational APIs form one coherent path.

Brokoli should match the short path from function to observable run and the quality of parameter/deployment tooling. It should retain a language-neutral IR and Go-owned scheduler rather than requiring the Python process to remain the workflow runtime.

## 4. Product principles

### 4.1 One canonical authoring style

The documented default remains direct data flow:

```python
with Pipeline("daily-orders", schedule="0 6 * * *"):
    orders = source_api("Orders", url="...", records="data")
    cleaned = clean(orders)
    cleaned >> sink_file("Archive", path="...")
```

Operator chaining is convenience syntax for the same invocation model, not a second semantic model.

### 4.2 Stable logical identity

An unchanged logical pipeline must compile to an unchanged semantic representation. Stable identity enables meaningful diffs, deployment previews, cache keys, UI/code reconciliation, and run comparisons.

Random node IDs should be replaced by explicit keys when supplied and deterministic compiler-generated keys otherwise. Layout changes must not count as execution-semantic changes.

### 4.3 Runtime honesty

Every public feature must be classified as one of:

- **Available:** executable and supported end to end.
- **Partial:** useful behavior exists with documented limits.
- **Experimental:** IR and API may change; deployment requires an explicit opt-in.
- **Proposed:** documentation/design only; not exposed as usable API.

The SDK must compare required execution capabilities with the target server before persistence. Unsupported behavior must fail with an actionable message, never become a runtime no-op.

### 4.4 Local confidence without a Python scheduler

Developers need three local test levels:

1. Pure tests of task functions and helper code.
2. Compiler tests of graph shape, contracts, and normalized IR snapshots.
3. A local execution harness for the subset of semantics supported by an embedded or subprocess backend.

Level three must invoke the same backend contracts used in production. A separate Python scheduler would make local tests convenient but untrustworthy.

### 4.5 Data moves by contract

The authoring SDK's `DatasetRef` and `ArtifactRef` identify the logical output of a node. Runtime references identify immutable stored values. They are related concepts, not the same serialized object.

The IR must declare input/output kinds and schemas. The physical plan decides whether a value stays inline, spills locally, uses object storage, preserves partitions, or materializes at an executor boundary.

### 4.6 Infrastructure is policy-governed

Users may request resources or a compute class. The server chooses among allowed executors based on capabilities, policy, locality, and availability. SDK options are requests, not authority.

## 5. Target developer workflow

### 5.1 Create and test

```bash
pytest
brokoli compile pipeline.py --check
brokoli diff pipeline.py --server "$BROKOLI_URL"
```

`compile --check` validates deterministic IR and local contracts. `diff` shows semantic changes, required capabilities, and an execution-plan preview without deployment.

### 5.2 Deploy safely

```bash
brokoli deploy pipeline.py --environment staging
```

Deployment should:

1. discover the pipeline without executing unrelated top-level code where possible;
2. compile deterministic IR;
3. fetch server capabilities;
4. reject incompatible IR or execution requirements;
5. run full server-side executable validation;
6. display a semantic diff and planner explanation;
7. persist an immutable pipeline version and code digest.

### 5.3 Operate without changing tools

```bash
brokoli run daily-orders --param ds=2026-08-09
brokoli logs --follow RUN_ID
brokoli retry RUN_ID --failed
brokoli cancel RUN_ID
brokoli backfill daily-orders --from 2026-08-01 --to 2026-08-08
```

The CLI should call backend APIs; it should not execute distributed work locally.

### 5.4 Understand physical work

The default UI remains the authored logical graph. Expanding a node reveals pages, partitions, mapped items, attempts, workers, checkpoints, progress, and output references. A user should be able to answer:

- What did I author?
- What did Brokoli plan?
- What is running now?
- What failed, and at what retry scope?
- What data was produced, and where is it stored?
- What can be retried without repeating successful work?

## 6. Architecture decisions

Implementation should proceed through the repository's ADR process.

### 6.1 Decisions already represented

- [ADR-012](../adr/012-artifact-and-dataset-plane.md): artifact and dataset storage and spill.
- [ADR-013](../adr/013-connector-protocol-partition-progress-cancellation.md): connector work units, progress, and cancellation.

### 6.2 New foundational decisions

- [ADR-014](../adr/014-pipeline-ir-ownership-and-compatibility.md): canonical pipeline IR ownership, evolution, capability negotiation, and deployment validation.
- [ADR-015](../adr/015-logical-and-physical-execution-plans.md): durable logical-to-physical planning and instance identity.

### 6.3 Decisions still required later

- Durable node scheduling, lease, retry, fairness, and backpressure semantics.
- Worker protocol v2 and the relationship between queue workers, enterprise worker pools, EventBus, and SODP.
- Code-package, dependency, image, trust, and sandbox policy.
- Runtime value envelopes and schema evolution.
- Deferred sensors, general checkpoints, and external-job resume semantics.
- Executor selection and materialization policy.

These should become separate ADRs when their milestone reaches implementation scope. They should not be hidden inside a broad implementation PR.

## 7. Delivery sequence

### Stage 0: Make current behavior honest

- Fix documentation and examples that do not execute.
- Add the SDK capability preflight already tracked in `brokoli-sdk#9`.
- Fail closed on unknown node kinds and unsupported config.
- Apply full executable validation consistently to create, update, and import.
- Add cross-repository fixtures proving SDK IR is accepted by the backend.
- Add SDK CI, lint, typing, and package checks.

**Exit condition:** no generally documented pipeline can validate locally and then fail solely because the target server never supported its declared feature.

### Stage 1: Deterministic compiler and local test harness

- Stable logical node identity and normalized semantic IR.
- Nested/async-safe pipeline context.
- Graph snapshots and semantic diff.
- Safe discovery boundaries and explicit entrypoints.
- Local backend-backed test harness for supported nodes.

**Exit condition:** unchanged source produces no semantic diff, and task plus graph tests run without a remote deployment.

### Stage 2: Canonical IR and deployment contract

- Land ADR-014's schema and conformance fixtures.
- Separate semantic node roles from required execution features.
- Add input/output data kinds, schemas, resources, code digests, and policies incrementally.
- Store immutable deployment versions and validate before persistence.

**Exit condition:** SDK, backend, and UI consume one normative contract with an explicit compatibility policy.

### Stage 3: Physical planning and durable work units

- Land ADR-015's persisted plan model.
- Convert pagination and `.expand()` into planner-visible instances.
- Wire node-level claims, leases, heartbeats, fencing, cancellation, and partial retries.
- Expose physical instances and plan explanations through API and UI.

**Exit condition:** one logical node can create many independently retryable physical instances across workers.

### Stage 4: Complete the data and connector planes

- Shared/object artifact storage and real dataset manifests.
- Partition-aware reads and writes without scheduler materialization.
- Live durable progress, connector work-unit planning, and persisted connector state.
- Artifact/dataset lineage and UI inspection.

**Exit condition:** large and partitioned workloads move through references, and connector work has the same visibility and retry semantics as native work.

### Stage 5: Runtime placement and advanced data operations

- Runtime packages and isolated images.
- Native partition executor, SQL pushdown, and Kubernetes jobs first.
- Spark/Ray adapters only after the placement contract is proven.
- `map_partitions`, repartition/coalesce, group/aggregate, and materialization boundaries.

**Exit condition:** the same logical operation can be placed on an allowed executor without changing business-flow code.

## 8. Issue strategy across repositories

Use one core issue for each accepted backend capability and one SDK issue for the corresponding authoring/compiler surface.

- The core issue links its motivating ADR near the top and divides implementation into independently landable milestones.
- The SDK issue names the exact backend capability it requires and does not claim runtime support before that dependency ships.
- Cross-repository tests land with the first executable slice, not after both sides drift.
- UI work gets a separate issue when it has independent acceptance criteria.
- An ADR update ships in the same PR that changes the recorded decision.

The umbrella issues for this strategy are [brokoli#90](https://github.com/Tnsor-Labs/brokoli/issues/90) and [brokoli-sdk#15](https://github.com/Tnsor-Labs/brokoli-sdk/issues/15). Existing focused issues remain authoritative for already-scoped bugs and features.

## 9. Success measures

### Authoring

- Median time from installation to first successful run.
- Lines of user code for HTTP ingestion, file fan-out, quality gating, and backfill examples.
- Percentage of pipelines requiring custom pagination, serialization, or retry loops.
- Percentage of compile failures with an actionable local error.

### Compatibility

- Deployments rejected before persistence due to unsupported capabilities.
- SDK/backend compatibility regressions caught by contract tests.
- Percentage of public APIs classified and tested as available, partial, experimental, or proposed.

### Reliability and scale

- Successful work reused after retry.
- Page/partition/instance retries versus whole-node retries.
- Worker-loss recovery time.
- Peak control-plane memory relative to dataset size.
- Outputs carrying schema, rows, bytes, checksum, and lineage.

### Operations

- Time from failed run to identified physical instance.
- Percentage of routine operations available through both API and CLI.
- Long-running nodes with live structured progress and heartbeats.

## 10. Non-goals

- Reimplement Spark, Ray, or a distributed filesystem.
- Hide all physical decisions from operators.
- Turn arbitrary Python into automatically partitioned work.
- Guarantee exactly-once side effects without connector support.
- Make assets mandatory for simple pipelines.
- Preserve ambiguous or silently ignored behavior for compatibility.

## 11. References

- [Brokoli Next architecture RFC](./brokoli-next-architecture-v2.md)
- [Apache Airflow core concepts](https://airflow.apache.org/docs/apache-airflow/stable/core-concepts/index.html)
- [Apache Airflow Dynamic Task Mapping](https://airflow.apache.org/docs/apache-airflow/stable/authoring-and-scheduling/dynamic-task-mapping.html)
- [Apache Airflow TaskFlow tutorial](https://airflow.apache.org/docs/apache-airflow/stable/tutorial/taskflow.html)
- [Dagster concepts](https://docs.dagster.io/getting-started/concepts)
- [Dagster testing guide](https://docs.dagster.io/guides/test)
- [Prefect flows](https://docs.prefect.io/v3/concepts/flows)
- [Prefect deployments](https://docs.prefect.io/v3/concepts/deployments)
- [Prefect work pools](https://docs.prefect.io/v3/concepts/work-pools)
