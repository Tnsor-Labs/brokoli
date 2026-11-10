# ADR-050: Dynamic pipelines: generated structure and mapped groups on a static IR

**Status:** proposed
**Date:** 2026-11-10

## Context

A pipeline's IR is static: a fixed set of nodes and edges, validated,
canonicalized and digested
([ir-canonicalization.md](../schema/ir-canonicalization.md)), stored as a
version, and run as that version. That is what makes a run auditable and a
recovery deterministic. It is also why a common class of pipeline cannot be
written once:

- **The shape depends on data outside the pipeline.** One extract per table
  in a source schema, one load per customer in a config table, one branch
  per region in a YAML file. When a table, customer or region is added, the
  pipeline has to change.
- **The amount of work is only known at run time.** Every file that landed
  today, every partition of a date range, every page of an API.
- **A whole chain repeats per item, not one step.** Extract, validate and
  load per table, with each table's failure isolated from the others.

Apache Airflow answers the first with **dynamic DAG generation**. A DAG file
is Python, and the DAG processor re-runs its top-level code every
`min_file_process_interval` seconds, so a loop over a config file or an
environment variable produces as many tasks or DAGs as the input has
entries. It answers the second and third with **dynamic task mapping**:
`expand()` creates one task instance per element of an upstream output,
and a `@task_group` can be mapped as a whole.

That model is powerful, and its costs are documented by Airflow itself:

- **Generation runs in the scheduler's parse loop.** Airflow's best practices
  tell authors to keep database access, network calls and heavy computation
  out of top-level code, and warn that reading Variables there can time a
  DAG file out before it finishes parsing. So the most useful inputs to
  generate from (a database, an API) are the ones authors are told not to
  read.
- **Structure changes silently.** Whatever the parser produced last is the
  DAG. A changed input can add or remove tasks with no review. Until Airflow
  3.0 a past run was shown against the current structure, not the one it
  ran with; 3.0 added DAG versions, based on a hash of the serialized DAG.
- **Mapping has fixed limits.** `max_map_length` (default 1024) fails the
  upstream task when exceeded. Mapping nested inside a mapped task group is
  not permitted, and a branch cannot be taken on a mapped task's output.

Brokoli already has part of the runtime half:

- **`.expand()` in the Python SDK** compiles onto a `code` node's
  `expansion` block. The engine runs one instance per upstream row, each
  with its own status ([`engine/expansion.go`](../../engine/expansion.go)).
  Locally the instances run sequentially; with remote dispatch (ADR-017) a
  bounded number run at once. The fan-out is capped at 1000 by default,
  overridable per node with `expansion.max_instances`. Only `code` nodes can
  expand, and only one node, not a chain.
- **ADR-015** (accepted) separates the logical plan, immutable per pipeline
  version, from the physical plan created per run. It already says one
  logical node may become many independently scheduled, retried and
  observed physical instances with deterministic keys, and that pagination,
  `.expand()` and connector partitions converge on that one work-unit
  model.
- **IR 2.1 conditional edges** (ADR-014 updates) route a run down one branch
  and durably skip the other.
- **Runs record their pipeline version**, change requests (ADR-046) let a new
  version be proposed and applied by a person, and pipelines can depend on
  other pipelines (`depends_on`).

Brokoli does not have:

- any way for a pipeline's **structure** to follow its inputs without a
  person editing or redeploying it. The SDKs compile code to IR when it is
  deployed, so a loop in that code produces repeated nodes, but only at
  deploy time;
- **mapping over a group of nodes**, or mapping any node type other than
  `code`;
- a way for one pipeline to **call another** as a step, with parameters, and
  wait for its result.

## Decision

The IR stays static. A pipeline version is a fixed, digested graph, and every
run executes exactly one version. Dynamism comes from two explicit
mechanisms, each recorded where it happens, instead of from code that
rewrites the graph:

1. **Generators** produce pipeline versions from inputs, ahead of runs. This
   is structural dynamism, Airflow's DAG generation.
2. **Mapped groups and calls** fan work out inside one run, recorded in that
   run's physical plan (ADR-015). This is runtime dynamism, Airflow's task
   mapping, made more general.

### 1. Generators: structure that follows its inputs

A **generator** is code, written with the SDKs, that takes declared inputs
and returns one or more pipelines. A sketch of the Python form (names are
illustrative; the SDK phase settles them):

```python
from brokoli import generator, Pipeline

@generator(inputs={"tables": query("warehouse", "SELECT table_name FROM catalog.tables WHERE active")})
def per_table(tables):
    for t in tables:
        p = Pipeline(key=t["table_name"], name=f"Load {t['table_name']}")
        ...  # build the pipeline as usual
        yield p
```

- **It runs as a task, never in the control plane.** A generator is
  packaged and executed like any task bundle (ADR-031, ADR-033), as its
  tenant's own user, with the same time and memory limits. Its inputs are
  read through Brokoli connections (a query, a file, an API call, a dataset,
  run parameters), declared up front so Brokoli fetches them, not the
  generator's code. Expensive or networked inputs are therefore the normal
  case, not the thing to avoid: the scheduler never executes author code to
  learn what to schedule.
- **It runs when something says so, not on a loop.** Triggers are explicit:
  a schedule, a change to an input dataset (through lineage, ADR-039), a
  push to the pipelines' repository, an agent or a person. A generator that
  is never triggered costs nothing.
- **Output goes through the same gate as any other version.** Each pipeline
  a generator returns is validated, canonicalized and digested. If its
  digest equals the current version's, nothing happens, so a generator that
  runs every hour but whose inputs did not change creates no versions.
  Otherwise the result is a new version, carrying provenance: the
  generator's bundle digest, a fingerprint of its inputs, and a structural
  diff against the previous version (nodes and edges added, removed,
  changed).
- **Structural change can require review.** A generator declares
  `apply: auto` or `apply: review`. With `review`, a changed version becomes a
  change request (ADR-046) that a person applies, showing the structural
  diff. `auto` is for generators whose changes are routine, such as one more
  table of a known shape. Either way the change is recorded, and a past run
  always renders the version it ran.
- **One generator, many pipelines, stable identities.** Each pipeline a
  generator returns carries a `key` (a table name, a customer ID). The
  pipeline's identity is the generator plus the key, so history, schedules
  and lineage continue across regenerations. A key that disappears retires
  its pipeline: paused and marked retired, with its history kept, never
  deleted by a generator.
- **Bounded and scoped.** A generator declares limits (pipelines, nodes per
  pipeline) and fails loudly past them. The pipelines it produces live in
  the generator's workspace and may only use connections and resources that
  the generator's owner may use there (ADR-049). A generator cannot widen
  what its author could have written by hand.
- **Determinism is checked, not assumed.** Brokoli records the input
  fingerprint with each version. When the same inputs produce a different
  digest, the run that produced it is marked non-deterministic and, under
  `apply: auto`, the version is held for review instead of applied.

### 2. Mapped groups and calls: work that follows the data within a run

IR 2.3 adds two constructs, both additive. IR 2.0 to 2.2 documents remain
valid, and a server that does not advertise 2.3 refuses a 2.3 document
rather than running it differently.

- **`group` with `map`.** A group is a named set of nodes with one input
  port and one output port. A group with a `map` block runs its nodes once
  per item of an upstream collection (a row, a file, a partition):

  ```json
  {"id": "per_table", "type": "group",
   "map": {"over": "list_tables", "key": "table_name", "max_items": 5000, "max_parallel": 16},
   "nodes": [...], "edges": [...],
   "collect": "union"}
  ```

  Each item's run of the group is a set of physical instances keyed by the
  logical node and the item key (ADR-015 rule 2), scheduled, leased, retried
  and recovered individually through instance dispatch (ADR-017). One item's
  failure does not stop or rerun the others; `on_item_failure: fail | skip |
  continue` decides what the run does with it. `collect` names how the
  per-item outputs become the group's output: `union` (one dataset), `list`
  (a collection, so a later group can map over it again) or `none`.
- **Any node can map, not only `code`.** A single node with a `map` block is
  a group of one, so `source_db`, `task`, `sink_db` and every other type map
  the same way. The existing `code` `expansion` block becomes a compatibility
  form of the same thing and is migrated, not run by a second code path.
- **Conditions inside a mapped group apply per item.** A `condition` node
  inside the group routes each item down its own branch, and the skipped
  branch is skipped for that item only, with IR 2.1's skip semantics.
  Airflow cannot branch on a mapped task's output; per-item conditions
  cover that case.
- **Nesting is allowed and budgeted.** A mapped group may contain another
  mapped group (per table, then per partition). Instead of forbidding
  nesting, the run has one instance budget across all levels, declared on
  the pipeline (`max_instances`), checked as items are discovered, and
  failing the run loudly with the count reached when exceeded.
- **`call` runs another pipeline as a step.** A `call` node names a pipeline
  (by ID, and either a pinned version or the current one) and the parameters
  to pass, starts it as a child run, waits for it, and outputs its result.
  Combined with `map`, one pipeline runs another once per item. Child runs
  are ordinary runs linked to their parent, so they keep their own history,
  permissions and retries, and cancelling the parent cancels them.

Limits are declared, not fixed: `max_items`, `max_parallel` and the run's
`max_instances` have server-wide defaults and ceilings an operator sets, and
every one of them fails with a message naming the limit and the count,
never by truncating the collection.

Every expansion decision is part of the run's persisted physical plan, so a
recovered run continues with the items it already discovered, an item can be
retried on its own, and the UI shows the authored graph by default with the
per-item instances on demand (ADR-015 rule 8).

### What Brokoli does not do

- **No code that mutates the graph during a run.** A task cannot add nodes to
  the run it is part of. Every node a run can execute is in its version or in
  a version it calls; the only thing decided at run time is how many times
  and down which branch.
- **No continuous parsing.** Nothing re-executes pipeline code on a timer to
  discover what exists. Generators run on explicit triggers.

## Consequences

### Positive

- **Structure can follow data without giving up auditability.** A new table
  produces a new pipeline version with a diff, provenance and, when wanted, a
  review, instead of a silent change on the next parse.
- **Generators can read what they need.** Databases, APIs and catalogs are
  ordinary generator inputs, because generation runs as an isolated task
  with declared inputs, not in the scheduler.
- **No version churn.** An unchanged generator output produces no version,
  because versions are compared by digest.
- **One fan-out model for everything.** Pages, partitions, files, mapped
  nodes, mapped groups and calls are all physical instances under ADR-015
  and ADR-017, with one retry, lease, recovery and observability model.
- **More than Airflow offers.** Brokoli adds four things Airflow's task
  mapping does not have:
  - nested mapping under one budget;
  - per-item branching;
  - any node type can be mapped;
  - limits that are declared rather than a fixed 1024.

  Generated structure is also versioned and reviewable from the first
  version, not added afterwards.
- **Reuse across pipelines.** `call` lets a pipeline be a building block, run
  per item when combined with `map`, with each child run keeping its own
  history.

### Negative

- **Physical plans grow by orders of magnitude.** ADR-015's retention and
  bounded materialization become prerequisites for mapped groups, not
  follow-ups.
- **Generators are a new kind of thing** to package, trigger, permission,
  monitor and explain in the UI, and a failing generator leaves its
  pipelines on their last good version. That has to be visible, or it
  becomes a silent staleness.
- **Two places can change a generated pipeline.** Editing a generated
  pipeline by hand would be overwritten on the next generation. Generated
  pipelines are therefore read-only in the editor, and changes go to the
  generator.
- **IR 2.3 widens the contract** every SDK, the UI and the validator must
  implement, with the conformance fixtures each IR version already carries.
- **Determinism checks cost a regeneration** when a generator's output
  changes while its input fingerprint did not.

### Deferred

- **Streaming discovery.** Mapping over an unbounded collection, where items
  arrive while the group runs (ADR-015 already defers streaming plans).
- **Cross-run reuse of mapped items** whose inputs did not change
  (incremental mapping by content digest).
- **Cost-aware `max_parallel`** that adapts to the fleet.
- **Generators that edit an existing pipeline** rather than produce whole
  pipelines.
- **The exact UI** for very large instance sets and for generator diffs.

## Alternatives considered

- **Airflow's model: re-run pipeline code continuously and take what it
  produces.** It gives the most flexibility for the least design. It was
  rejected because it executes author code in the control plane, discourages
  exactly the inputs generation needs, and changes structure without a
  record.
- **Dynamic IR: let a running task add nodes to its own run.** It removes the
  need for generators. It was rejected because the graph a run executes
  could no longer be known from its version, recovery would depend on
  replaying author code, and review and lineage would see a graph that never
  existed as a version.
- **Templated IR with loops evaluated by the server.** It would avoid running
  author code. It was rejected because it means building and securing a
  second language inside the IR. The SDKs already provide a real language,
  and generators reuse them.
- **Keep `.expand()` as it is and add more node-specific expansion blocks.**
  It is the smallest change. It was rejected because ADR-015 already decided
  one work-unit model, and a block per node type is the divergence it warned
  against.
- **Forbid nested mapping, as Airflow does.** It is simpler. It was rejected
  because per-table then per-partition is a common shape, and a single run
  budget bounds it just as well.

## Follow-ups

Ordered so each phase is usable on its own:

1. **Mapping any node, in parallel.** Generalize `.expand()` to the `map`
   block on every node type, run items as physical instances through
   instance dispatch both locally and remotely, with declared limits.
   Migrate the `code` `expansion` block.
2. **IR 2.3: `group`, `collect`, per-item conditions and nested mapping**
   under a run instance budget, with validator, schema, fixtures and SDK
   support in Python and TypeScript.
3. **`call`: child runs** with parameters, cancellation and history links.
4. **Generators:** the SDK API, packaging as a task bundle, declared inputs,
   triggers, digest comparison, provenance, structural diff, `apply: review`
   through change requests, retirement by key, and determinism checks.
5. **Data-aware generator triggers** from lineage (ADR-039).

Each phase gets its own issue linking back here, per this directory's README.
