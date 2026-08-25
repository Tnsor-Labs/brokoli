# ADR-025: Run dbt projects as they are, and own the orchestration around them

**Status:** proposed
**Date:** 2026-08-25

## Context

Brokoli ships a `dbt` node type today. It is in `models.NodeType`, it has a
config panel in the UI with command cards for run/test/build/seed, it is in
the capability matrix, and the docs say it "runs dbt models to transform data
in your warehouse".

**It cannot execute a single `dbt run`.** `engine/node_dbt.go` appends
`--output json` to every invocation, and that flag exists only on `dbt ls`.
Verified against dbt-core 1.8.9:

```
$ dbt run --project-dir /tmp --output json
Usage: dbt run [OPTIONS]
Error: No such option '--output'.
```

Every command the UI offers — run, test, build, seed — fails at dbt's option
parser before touching the project. The node has **no test of its own**; the
only test that references it substitutes a fake executor that intercepts the
node type entirely, with a comment explaining that this avoids "needing a
real dbt project on disk". So the feature has never been executed, by CI or
by anyone, against the tool it wraps.

Three further defects are visible in the same 131 lines, and are worth
recording because they shape the decision rather than just needing fixes:

- `DBT_PROFILES_DIR` is set unconditionally, so an unset `profiles_dir`
  exports the variable as empty and overrides dbt's own default of `~/.dbt`.
- The `run_results.json` fallback reads `target/run_results.json` relative to
  the *engine's* working directory, not the project's, so it resolves to the
  wrong path whenever the project is anywhere but the process cwd.
- `exec.Command` is used without the attempt's context, so a hung dbt
  invocation ignores both the node timeout and a cancelled run. Every other
  external call in the engine threads `r.ctx`.

The last one matters most for what follows: it is not a typo, it is the
signature of a feature bolted to the side of the engine rather than built
inside it.

### What this is really evidence of

The node is a wrapper someone wrote from the shape of dbt's CLI rather than
from its behaviour. That is a fair description of most "dbt integration"
features in orchestrators: shell out, stream stdout to a log pane, exit code
becomes node status. It is cheap to build and it is what the market already
has.

It also throws away everything that makes dbt worth integrating. dbt's value
is not that it runs SQL; it is that it *knows the shape of the SQL* — a
manifest with every model, its dependencies, its tests, its columns, and its
freshness contracts. Shelling out reduces that to an exit code.

Meanwhile Brokoli spent this month building the thing that could consume it.
ADR-024 gave the engine `pkg/dbdialect`: a backend described in one place,
with a rule that a capability may only be claimed when a differential test
proves it. ADR-023 gave it `TableRef` — the ability to hand a consumer "these
rows are what this query returns on this server" instead of the rows. #346
made a same-server segment run as one statement on two backends. A dbt model
is exactly a named SQL query against a connection, which is exactly what
those two mechanisms are built to carry.

### The three shapes this could take

**A. Run dbt-core, own the orchestration.** Keep dbt as the compiler and
runtime. Parse its manifest to get the model DAG, schedule models through
Brokoli's own scheduler, surface each model as a first-class node with its
own run history, lineage, and retry. Sells as: your dbt project, observable
and orchestrated properly.

**B. A native model layer.** Build Brokoli's own models/refs/tests on top of
`pkg/dbdialect`, borrowing dbt's ideas without dbt. Sells as: dbt without
Python, one binary. Competes with dbt rather than adopting it, and asks every
prospect to rewrite a working project.

**C. Shell out, better.** Fix the flags, add a timeout, capture
`run_results.json`. Sells as: a checkbox on a comparison table.

Option C is where the current node was aiming and is not worth a release
note. Option B is architecturally the most attractive — the abstraction is
already there — and commercially the worst: a team with a dbt project has
years of accumulated tests, macros, and packages, and "rewrite it in ours" is
not a migration, it is a rebuild. Nobody buys that.

Option A is the one that matches both what exists and what a buyer wants.
It also has a property the other two lack: it is *additive to* the customer's
existing investment. They keep their models, their tests, their `dbt build`
in CI, and gain scheduling, lineage, and failure handling they currently get
from cron and a Slack channel.

## Decision

**Run real dbt projects, unmodified, and own the orchestration and
observability around them. Do not reimplement dbt, and do not ship a shell
wrapper.**

Concretely, the integration's unit of work is a **model**, not a `dbt`
command:

1. **The manifest is the contract.** Brokoli invokes `dbt parse` (or reads an
   existing `target/manifest.json`) and reads the model DAG from it: models,
   their `depends_on`, tests, materializations, and the columns dbt already
   knows about. This is a documented dbt artifact with a versioned schema —
   the same input dbt's own metadata tooling consumes.
2. **A dbt project becomes a pipeline subgraph.** Each model is a node in
   Brokoli's DAG with its own status, duration, log, and retry — not a line
   in one node's stdout. A failed model fails that node; the models that do
   not depend on it still run, which is what `dbt build` does internally and
   what an orchestrator currently cannot see.
3. **Execution goes through dbt-core**, invoked per selected subset, with
   `--log-format json` parsed structurally and `run_results.json` read from
   the project's own target path. dbt remains the compiler and the executor;
   Brokoli never generates the model SQL.
4. **Credentials come from the connection store.** Brokoli generates the
   `profiles.yml` for the run from an existing Brokoli connection, so a dbt
   project does not need its own copy of the warehouse password, and the
   secret handling that already exists applies to it.
5. **The boundary with pushdown is explicit and closed.** A dbt model is a
   `TableRef` in ADR-023's sense once it has been built — a named relation on
   a known server — so a downstream Brokoli `source_db` reading that model on
   the same connection composes with the existing pushdown compiler with no
   new mechanism. That is the payoff of doing this after ADR-024 rather than
   before it.

### What "fully tested" has to mean here

The house rule from ADR-024 applies unchanged: a capability is claimed only
when a test proves it against the real thing. For this integration that
means the test suite runs **real dbt-core against a real warehouse**, in CI,
the way #339 made the MySQL suites run. A dbt integration tested against a
mock is exactly how the current node reached production unable to run.

Minimum bar for the first release:

- A fixture dbt project in the repository — models with refs, a seed, a
  schema test, one incremental model — built by real dbt in CI against the
  Postgres and MySQL service containers that already exist.
- The manifest parser tested against manifests from **more than one dbt
  version**, pinned as fixtures. `manifest.json` carries a schema version and
  it changes between minor releases; a parser written against one version and
  never checked against another is the same defect class as the `--output`
  flag.
- Equivalence: a `dbt build` run by Brokoli and the same project run by
  `dbt build` on the command line produce the same relations, compared by
  content, and the same pass/fail per test.
- Failure semantics pinned: a failing model, a failing test, a compile error,
  and a killed process each produce a distinct, named node status — and a
  cancelled run actually stops dbt, which today it does not.

### Scope of the first release

In: `dbt run`, `dbt test`, `dbt build`, `dbt seed`; model-level DAG
expansion; profiles generated from a Brokoli connection; structured results;
Postgres and MySQL (the two backends with proven dialect support).

Out, deliberately, and each needs its own decision: snapshots (they mutate
history and their failure semantics deserve their own analysis); Python
models (a different runtime entirely); dbt Cloud as a remote executor;
`dbt deps` fetching from the network at run time (a supply-chain question,
not a feature question); and any warehouse Brokoli's own dialect layer does
not support, because a green dbt run against a backend Brokoli cannot read
back is a half-integration.

## Consequences

### Positive

- A team keeps its dbt project exactly as it is and gains per-model
  scheduling, lineage, retry, and history. The migration cost is pointing
  Brokoli at a repository.
- Model-level granularity is the visible product difference. Most
  orchestrators show one green or red box per `dbt build`; this shows the
  DAG, which is what someone debugging a 300-model project actually needs.
- It composes with what was just built rather than duplicating it: a built
  model is a `TableRef`, so pushdown, streaming sinks, and the dialect layer
  all apply with no new mechanism.
- Credentials stop being duplicated into `profiles.yml` by hand.

### Negative

- **dbt-core becomes a runtime dependency**, with a Python environment to
  provision, version-pin, and support. Brokoli ships as a single static
  binary today; this is the first thing that breaks that property, and the
  packaging answer (container image, sidecar, or documented prerequisite) is
  a real decision this ADR does not make.
- **The manifest is a moving target.** It is documented and versioned, but it
  is dbt's internal artifact and it changes. The multi-version fixture
  requirement above is the mitigation, and it is a standing tax.
- **Model-level dispatch is not free.** Invoking dbt per model pays its
  startup cost repeatedly; invoking it per subset means reconciling dbt's own
  intra-run scheduling with Brokoli's. Which of those wins is a measurement,
  not an opinion, and the first implementation should measure it on a project
  of real size rather than a five-model fixture.
- **It is a large surface.** dbt's semantics include incremental strategies,
  macros, packages, and hooks; each is a place where "we run your project
  unmodified" can turn out to be false in a way that is embarrassing rather
  than merely incomplete.

### Neutral

- The existing `dbt` node type stays, and its config grows rather than being
  replaced, so pipelines referencing it do not break. Its current behaviour
  cannot be preserved because it does not have any.

## Alternatives considered

- **Fix the wrapper (Option C).** Rejected as a product, accepted as a
  stopgap only if the full work is not scheduled: see Follow-ups. It is
  perhaps two hours of work and it would stop the node lying about what it
  does.
- **Build a native model layer (Option B).** Rejected on migration cost, not
  on architecture — `pkg/dbdialect` plus the pushdown compiler is genuinely
  most of a model compiler, and this may be worth revisiting as an *addition*
  once dbt projects run, for teams with no existing dbt investment.
- **Wrap dbt Cloud's API instead of dbt-core.** Rejected for the first
  release: it makes the integration depend on a commercial account the buyer
  may not have, and it inverts the control relationship — dbt Cloud would own
  the scheduling this ADR is proposing to own.
- **Generate dbt projects from Brokoli pipelines.** Rejected: it produces
  code a human then has to maintain in two places, and the direction of
  travel is wrong.

## Follow-ups

- **Immediately, regardless of whether this ADR is accepted:** the shipped
  `dbt` node advertises a capability it does not have. Either fix the flag
  and add the missing context/timeout handling, or mark the node
  unavailable in the UI. Shipping a node that fails at dbt's option parser
  for every command it offers is worse than not shipping it. Filed as its own
  issue (#348).
- The packaging decision for a Python runtime dependency needs its own ADR
  before implementation starts, not during it.
- Revisit Option B as an addition once dbt projects run, for teams starting
  from nothing.
