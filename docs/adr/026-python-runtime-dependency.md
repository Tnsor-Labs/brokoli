# ADR-026: Check for a Python runtime, do not ship one

**Status:** accepted — the rule is ADR-016's, applied to dbt; implementation
is #353's Phase 0
**Date:** 2026-08-25

## Context

ADR-025 accepted running real dbt projects, and named this as the thing to
settle **before** implementation rather than during it: dbt-core is Python,
and running dbt projects means a Python environment exists wherever pipelines
execute.

That is awkward because of a property the project has defended twice. The
release is `CGO_ENABLED=0`, cross-compiled to six targets, and installed by
a one-liner that downloads a single binary (ADR-007). ADR-024 records that
same constraint as the reason Oracle stays deferred — `godror` needs CGO, so
only a pure-Go driver could ever be used. A Python dependency is the first
thing that breaks the single-binary property outright, and "we already
shell out to `dbt`" is not a defence: the node that does so was shipped
unable to run any dbt command at all (#348), precisely because nobody had
thought about what it depended on.

### There is already a decision here, and it is not new

The instinct is to treat this as an unprecedented question. It is not.
**ADR-016 already defined a `python` runtime class for plugins**, with the
policy stated in one sentence:

> We check and resolve; we do not provision. Installing a JRE is the
> operator's job, and the feasibility error is how they learn it, before
> anything is downloaded rather than when a pipeline fails.

It resolves an interpreter against a declared version range (`requires.python`),
records the resolved absolute path at install time, and refuses the install
naming exactly what is missing and on which host. It also names, in its own
Consequences, the two things it deliberately did not do: bundling runtimes
("the cost/benefit of shipping runtimes dwarfs this ADR"), and dependency
isolation ("a `python` payload that needs third-party packages still needs
them importable by the resolved interpreter"; per-plugin venv creation is
listed under Deferred).

dbt is that same shape with one difference that matters: a plugin's Python
payload is code Brokoli ships, while dbt-core is a third-party distribution
with its own version cadence, its own transitive dependency tree, and an
artifact format (`manifest.json`, `run_results.json`) that changes across
minor releases. So dbt needs everything the plugin runtime class needs, plus
an answer to the dependency-isolation question ADR-016 deferred.

### What the options actually cost

- **Documented prerequisite.** The operator installs dbt-core; Brokoli finds
  it on `PATH`. Cheapest, and what the current node already assumes. Pushes
  version compatibility onto the operator — and dbt's CLI and artifact schema
  both drift, which is how #348's class of defect arrives silently.
- **A container image bundling dbt.** Predictable versions; natural for the
  Kubernetes executor (ADR-017). Costs the download-one-binary install story
  for everyone, and makes Brokoli responsible for shipping and patching a
  Python distribution — a supply-chain surface it does not have today.
- **A separate worker class with dbt installed.** Keeps the core binary
  clean and fits physical-instance dispatch (ADR-017), where "installed on
  the worker" already varies per worker class. Most infrastructure, and the
  deployment story gains a moving part.
- **Bundle a Python runtime in the release artifact.** Removes the
  prerequisite, and turns six cross-compiled targets into six
  platform-specific Python builds with their own CVE stream. Worst of both.

## Decision

**Follow ADR-016's rule: check and resolve, do not provision.** Brokoli
requires a working dbt-core on the execution host and refuses, with a named
error, when it is absent or outside the supported range. It does not ship,
download, or install Python or dbt.

Concretely:

1. **The single-binary install story is unchanged for everyone who does not
   use dbt.** Someone running Postgres-to-Postgres pipelines acquires no
   Python dependency, and nothing about the download or the one-liner
   changes. This is a hard requirement, not an aspiration: a packaging
   answer that taxes every user for a feature some use is the wrong answer.

2. **The supported dbt range is declared in code and enforced at
   resolution**, in the same shape ADR-016 uses for `requires.python`.
   Brokoli probes `dbt --version`, compares against the declared range, and
   records what it resolved. An unsupported version is refused by name and
   version, not discovered when a manifest field has moved.

3. **Failure happens at validation, not mid-run.** A pipeline containing a
   dbt node is refused at validation time when no satisfying dbt is present,
   the same way ADR-016 refuses a plugin install rather than letting a run
   fail later. A dbt node that fails in the middle of a DAG has already cost
   whatever ran before it.

4. **Dependency isolation stays the operator's**, and is documented rather
   than pretended away. dbt adapters (`dbt-postgres`, `dbt-mysql`) are
   separate distributions with their own versions; Brokoli names which
   adapter a connection type requires in the error when it is missing. A
   virtualenv per project is a reasonable operator practice and Brokoli
   supports it by honouring a configured interpreter path rather than
   assuming `PATH`.

5. **Deployment images are where bundling belongs, if anywhere.** An
   operator who wants dbt present without managing it should get it from a
   deployment artifact that already exists for their environment, not from
   the core release. That keeps the choice with the people who know their
   environment, and keeps the core binary honest about what it is.

### Why not the container image

It is the tempting answer, because it makes the problem disappear for the
Kubernetes case. It is rejected for the same reason ADR-016 rejected bundled
runtimes: the cost is paid by every user and the benefit accrues to some.
It also moves Brokoli into shipping a Python distribution, which means
tracking its CVEs on Brokoli's release cadence rather than the distribution's
— a commitment this project has not made and should make deliberately if
ever.

## Consequences

### Positive

- Nothing changes for users who do not use dbt, which is most of them today.
- The rule is one the codebase already follows, so there is one policy for
  interpreted runtimes rather than two that will drift.
- Version drift becomes a named refusal instead of a mystery. #348 shipped
  because nothing checked what it depended on; this makes that class of
  defect loud.
- Refusing at validation rather than mid-run means a dbt-node pipeline fails
  before doing partial work.

### Negative

- **The operator has real work to do**, and on a fleet that work is per-host.
  This is the honest cost of not provisioning, and ADR-016 already accepted
  it for plugins; accepting it again here is consistency, not a free choice.
- **A supported-version range is a maintenance commitment.** Widening it
  means testing against more dbt versions; not widening it means telling
  users their dbt is too new. ADR-025 already requires manifest fixtures from
  more than one dbt version, so this cost was incurred there.
- **"Works on my machine" gets easier to hit**, because the execution host
  is now part of the pipeline's contract in a way it was not before.
- It does not make the Kubernetes case pleasant on its own. An operator
  running workers in a cluster still needs an image with dbt in it; this ADR
  says that image is theirs to choose, which is a decision, not a solution.

### Neutral

- The existing `dbt` node (#348, v0.10.72) already assumes dbt on `PATH`, so
  its behaviour is unchanged. What changes is that the assumption becomes
  checked and stated rather than implicit.

## Alternatives considered

- **Bundle Python in the release artifact.** Rejected: six targets become
  six platform-specific Python builds with their own CVE stream, and every
  user pays for a feature some use.
- **Require a container deployment for dbt.** Rejected as a *requirement*,
  accepted as an operator's option — see Decision 5. Requiring it would make
  the download-one-binary path a second-class citizen for a whole feature.
- **Vendor dbt-core as a Python payload the way ADR-016 vendors plugin
  dependencies.** Rejected: dbt-core's dependency tree is large and includes
  compiled extensions, so vendoring it is closer to shipping a distribution
  than to bundling a script.
- **Wrap dbt Cloud instead**, avoiding the runtime entirely. Already rejected
  in ADR-025 for the first release: it requires a commercial account the
  buyer may not have, and inverts the control relationship ADR-025 proposes
  to own.

## Follow-ups

- Implementation of the resolution and validation-time refusal is Phase 0
  work in #353, which this ADR unblocks.
- ADR-016's deferred per-plugin venv provisioning is the same question one
  layer down. If it is ever answered, the answer should cover both rather
  than being invented twice.
- The supported dbt range needs a stated policy for how it widens, since
  ADR-025's multi-version manifest fixtures are what make widening testable.
