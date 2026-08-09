# ADR-015: Persist logical and physical execution plans separately

**Status:** proposed
**Date:** 2026-08-09

## Context

The Python SDK authors a compact logical graph. The current Go runner executes that graph directly with static Kahn-wave scheduling. Pagination and dynamic expansion create concurrency inside node handlers rather than scheduler-visible work. Expansion instances are sequential, page retries are internal to the HTTP fetcher, distributed workers claim whole pipelines, and physical attempts are not consistently connected to the lease/fencing interfaces already present in storage.

This model cannot provide the core Brokoli Next promise: one readable logical node becoming many independently scheduled, observable, checkpointed, and retryable physical instances.

Conflating the two graphs also creates a UI problem. Expanding a 100-page source into 100 authored nodes makes the pipeline unreadable, while hiding all page work inside one node makes progress, retries, placement, and failures invisible.

## Decision

Brokoli stores the authored logical pipeline and the per-run physical execution plan as separate, linked models.

The logical plan is immutable for a deployed pipeline version. It contains user intent: logical nodes and edges, data contracts, connector/runtime requirements, policies, and code/package references.

The physical plan is created by the Go planner for each run. It contains stages, work units, dependencies, executor placement, materialization boundaries, retry scope, concurrency/rate-limit groups, and stable physical instance keys.

The first implementation follows these rules:

1. Planning occurs at run creation for statically knowable work and incrementally when an upstream collection, connector plan, or dataset manifest reveals dynamic work.
2. Every physical instance links to one logical node and has a deterministic key. The default key is derived from logical node ID, input digest, and collection index; connectors or SDK policy may provide a stable domain key.
3. `NodeRun` remains the logical-node summary shown in the default graph. Physical instances and their attempts are separate records. Existing `ExpansionInstance` data migrates into or becomes a compatibility projection of the physical-instance model.
4. `ExecutionAttempt` represents one leased attempt of a physical instance. Claims, heartbeats, fencing generations, cancellation, retry timing, and lost-worker recovery attach to that attempt.
5. Collections and partition lists are represented by manifests. The planner and scheduler read and materialize claims in bounded batches rather than loading an entire dynamic collection into memory.
6. Retry is scoped to the smallest declared durable work unit. Successful sibling instances remain complete and their immutable outputs are reused.
7. Pagination, `.expand()`, connector `plan`, and dataset partitions converge on the same work-unit abstraction. They do not create separate schedulers.
8. The UI shows the logical graph by default and physical stages/instances on demand. Planner explanations are available before execution.
9. The initial planner does not perform cost-based optimization, task fusion, shuffle optimization, or automatic parallelization of arbitrary Python. Those require later executor and data-contract decisions.

Physical plans are persisted so recovery, audit, retry, and UI explanations do not depend on reconstructing planner choices from mutable code after a crash.

## Consequences

### Positive

- Logical pipelines remain readable regardless of runtime scale.
- Pages, files, partitions, and mapped items gain one scheduling and retry model.
- A failed instance can be retried without rerunning successful siblings.
- Worker leases and fencing can protect the actual unit being executed rather than only a whole pipeline.
- The UI can explain placement, concurrency, progress, retries, and materialization while preserving the authored graph.
- Persisted planner decisions make recovery and historical debugging deterministic.

### Negative

- Run creation and recovery gain a new persisted model and migration burden.
- Incremental planning requires careful transactional boundaries between upstream completion, manifest publication, instance creation, and queue publication.
- Large expansions can create many database records; bounded materialization, retention, and indexing are required.
- Existing `NodeRun`, `ExecutionAttempt`, and `ExpansionInstance` responsibilities overlap and must be clarified without breaking run history.
- Planner behavior becomes a compatibility surface that requires versioning and explanation.

### Deferred

- Cost-based executor selection and adaptive concurrency.
- Operation fusion, predicate/projection pushdown, partition pruning, and shuffle planning.
- Cross-run caching and global content deduplication.
- Streaming plans with unbounded work-unit discovery.
- Exact UI design for very large instance sets.
- The durable scheduler's fairness, tenant quota, and queue-priority algorithm; this needs a dedicated ADR.

## Alternatives considered

- **Expand logical nodes before persistence.** Rejected because it destroys the authored graph, makes runtime-dependent node counts impossible, and creates noisy pipeline versions.
- **Keep expansion inside each node handler.** Rejected because the scheduler cannot place, lease, retry, cancel, or observe individual work units.
- **Use a separate model for pagination, mapping, and connector partitions.** Rejected because these are the same scheduling problem with different discovery mechanisms.
- **Generate physical plans on demand without persistence.** Rejected because recovery and audit could produce different decisions after code, connector, policy, or capacity changes.
- **Build cost-based optimization in the first planner.** Rejected because durable identity, retry, and recovery are prerequisites; optimization can evolve after correctness is proven.

## Follow-ups

- Tracked in [brokoli#90](https://github.com/Tnsor-Labs/brokoli/issues/90), milestones M2 and M3.
- Add a dedicated ADR for durable node scheduling, leases, fairness, and backpressure before wiring distributed dispatch.
- Define migrations/projections for `NodeRun`, `ExecutionAttempt`, and `ExpansionInstance`.
- Update ADR-013 when connector `plan` work units use this model.
- Add API and UI contracts for planner explanations and instance pagination.
