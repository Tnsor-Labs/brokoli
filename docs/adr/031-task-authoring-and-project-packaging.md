# ADR-031: Task authoring and project packaging for multi-language pipelines

**Status:** accepted
**Date:** 2026-08-30

## Context

The current code-node model is useful as an execution contract but not as the
primary authoring experience. An author writes a script string, while the
things that make a real project maintainable — IDE support, imports, tests,
dependency resolution, lockfiles, and local debugging — live outside that
string.

The SDKs already expose task-shaped APIs. Python has decorator-based tasks and
TypeScript has `Pipeline.task(...)`, but the deployed representation is still
a self-contained script. TypeScript task serialization deliberately does not
package relative imports, closures, or project dependencies. Python packaging
can include some same-module helpers, but it does not define a complete
project artifact either.

This creates three incompatible experiences for the same pipeline:

1. **The developer** wants to write normal Python or TypeScript in a project,
   with ordinary imports and IDE tooling.
2. **The pipeline editor** needs to display, configure, inspect, and observe a
   task without becoming a multi-file source-code editor.
3. **The deploy command** needs to turn source code and dependencies into an
   immutable, reproducible runtime artifact.

The design must also leave room for JVM languages such as Java, Kotlin, and
Scala without making every SDK invent a different deployment protocol.

Constraints from existing ADRs:

- The pipeline IR is core-owned (ADR-014); SDKs must not create a competing
  execution representation.
- Code execution is already language-dispatched and capability-gated
  (ADR-030).
- Runtime dependencies are resolved on the host rather than shipped as part
  of the core server (ADR-026).
- Reference kinds and data movement are engine decisions, not authoring
  concepts (ADR-023).
- The existing code-node worker protocol must remain usable as the execution
  boundary (ADR-029 and ADR-030).
- Tenant isolation and outbound-request safety are enforced at the
  data/network layer, not per-feature (ADR-022) — a bundle upload/fetch path
  is exactly the kind of surface that ADR covers and must not bypass.
- **The multi-language handoff's non-negotiable applies here without
  exception**: no per-task signature declared on the authoring side and
  bound by name on the execution side, hand-synced instead of verified. This
  is the single largest risk in this ADR — see Decision 1 below — because a
  "declared schema" field is precisely that failure shape if it is trusted
  rather than checked.
- A largely equivalent packaging problem is already solved for plugins:
  ADR-016 defines a content-addressed archive (`manifest.json` + payloads,
  each with a mandatory `sha256`, a closed `runtime` vocabulary with
  `requires` version constraints, install-time resolve-and-refuse — never
  provision — and workers that fetch-by-digest into a local content-addressed
  cache with their own feasibility check). This ADR reuses that format rather
  than inventing a second one; see Decision 2.

## Decision

This ADR proposes that **Task becomes the primary authoring abstraction**,
while `code` remains the low-level execution node and escape hatch. A task is
compiled by its SDK into a versioned, immutable task bundle referenced by the
pipeline IR.

### 1. Authoring model

The SDKs expose normal-language task functions as the preferred API.

TypeScript supports both a named function and an inline function in the
pipeline file:

```ts
function normalizeOrders(rows: Order[]) {
  return rows.map(normalizeOrder);
}

const clean = pipeline.task("Normalize orders", source, normalizeOrders);
```

```ts
const clean = pipeline.task("Normalize orders", source, (rows) => {
  return rows.map((row) => ({ ...row, normalized: true }));
});
```

Python continues to support decorator and callable forms. The authoring
function remains ordinary source code; users do not write a script string for
normal tasks.

The initial task contract must support:

- A task defined in the pipeline entry file.
- A task imported from a relative project file.
- Same-language helper functions and constants.
- Input and output schema recorded in the manifest for the UI and for
  cross-language tooling (see below).
- Local invocation or test execution using the same function.
- Resource settings and runtime selection already represented by code-node
  configuration.

**Schema must be derived, never hand-declared.** An author-supplied schema
string that the packager trusts without checking it against the function is
exactly the Airflow AIP-108 failure this project has already diagnosed and
built policy against (the multi-language engineering handoff's non-negotiable,
restated in Context above): two representations of the same fact — the
function's real signature and a schema someone typed — kept in sync by
convention, with nothing to catch drift when one changes and the other
doesn't. Concretely:

- The packager derives the schema from the language's own type information —
  TypeScript's type checker over the task function's parameter and return
  types, Python's type hints via `typing`/`inspect` — at bundle-build time.
  This is why it is a packaging step and not a runtime concept: derivation
  needs the compiler/type-checker, which is not available inside the worker.
- A task function whose types cannot be resolved to a concrete row/column
  shape (e.g. `Row[] => Row[]`, `any`, an untyped Python function) records no
  schema. Absence is honest; a guess is not.
- If an author supplies additional schema metadata (for a shape the type
  system can't express, such as which specific columns exist inside a `Row`),
  packaging **verifies it against whatever the type system can independently
  confirm and fails the build on contradiction** — it is never accepted
  verbatim. A schema field that cannot be checked at all against anything is
  not part of the v1 contract; propose the checking mechanism before adding
  the field.
- The manifest schema is therefore always a fact about the deployed code,
  reconstructible from the bundle's own source, never a second, independently
  editable declaration. The UI displays it; nothing (including the pipeline
  editor) may hand-edit it separately from the source.

### 2. Task bundle contract — a profile of the ADR-016 package format

TaskBundle is **not a new archive format**. ADR-016 already defines exactly
the shape this problem needs — content-addressed, `manifest.json` + payload
directories, a closed `runtime` vocabulary with version `requires`, mandatory
per-payload `sha256`, install-time resolve-and-refuse, and workers that
fetch-by-digest into a local content-addressed cache and run their own
feasibility check. TaskBundle is a new payload *kind* inside that same
archive and distribution machinery (`packaging_version`, the `.bkg` envelope,
`GET /.../archive`, the worker fetch-by-digest path), not a second format
built beside it. Concretely:

```text
<task>-<digest-prefix>.bkg          # same envelope ADR-016 defines
|-- manifest.json                  # packaging_version, payloads[] as ADR-016
|-- src/ or dist/                  # source or compiled payload
|-- deps/                          # v1: none — see scope note below
`-- <sha256-named files>
```

The manifest adds, per payload, alongside ADR-016's existing
`runtime`/`os`/`arch`/`entrypoint`/`sha256`/`requires` fields:

- `task_name` and the file/symbol entrypoint within the payload.
- `source_digest` (the project files that produced this payload) and
  `build_digest` (build tool + options), so identical source rebuilt
  differently is distinguishable from bit-identical output.
- The derived input/output schema (see Decision 1) — or its explicit absence.
- Files required by the task, exactly as ADR-016 lists files per payload.

**Reproducibility, made checkable, not asserted:** the digest is only
meaningful if the same project produces the same digest on every machine.
The manifest and archive assembly must be deterministic — sorted manifest
keys, sorted archive entries, zeroed timestamps — and this ADR requires a
cross-SDK fixture suite (one fixture project per language, asserted to
produce one pinned digest) in the same spirit as the IR canonicalization
oracle. Without it, "content-addressed" is a claim no test protects.

**Wording correction on rebuilds:** the server never builds anything (see
Decision 5) — it is the SDK/CLI that builds, once, locally. What the server
must do is *refuse to accept an uploaded archive whose contents don't hash
to the digest it claims*, and refuse to overwrite an already-stored digest
with different bytes. "Must not silently rebuild an existing digest" in the
original text was describing the wrong actor.

**The IR reference is a new, versioned, gated field — not implicit.** A
`code` node's config gains an optional `task_bundle` object:
`{"digest": "sha256:...", "format": "task-bundle/1"}`. Its presence is what
tells the engine to resolve and mount a bundle before dispatch, instead of
executing `script` directly (script and task_bundle are mutually exclusive
on one node). This requires, before implementation:

- A `pipeline-ir-2.1.json` schema update for the new field, and a
  corresponding entry in `docs/schema/ir-canonicalization.md` stating exactly
  how `task_bundle` normalizes and digests as part of the pipeline's own
  canonical form (the field is preserved verbatim, never stripped —
  swapping a bundle digest is a real semantic change and must change the
  pipeline's digest and trigger a new pipeline version, consistent with how
  every other config change is already handled).
- A new `SupportedExecutionFeatures` entry (`"task-bundles"`), advertised
  only when a server can actually resolve and mount bundles, so an older
  server refuses these pipelines by feature name at deploy preflight —
  exactly the discipline `code-streaming-emit` and `code-typescript`
  established in ADR-029/030. A pipeline using `task_bundle` against a
  server that doesn't advertise it must never reach "deployed, then fails at
  run time."
- Differential fixtures (paired per-SDK projects producing one pipeline)
  proving every SDK that emits `task_bundle` produces byte-identical
  canonical IR for it, per the existing cross-SDK contract.

The bundle remains an execution artifact referenced by digest, not a second
pipeline IR: core validates the pipeline graph exactly as today and, when a
`task_bundle` is present, additionally resolves and mounts it before
dispatching to the language/runtime already defined by ADR-030.

### 3. Language-specific packaging

The common manifest is shared, but each SDK owns the native packaging step.

**Scope note — v1 is project files only, no third-party dependencies.**
Resolving relative imports and same-repo helpers solves most of the
authoring pain (IDE support, real imports, shared code) with a small,
reviewable amount of machinery: the packager walks the project's own import
graph from the entry file and bundles exactly those files. Third-party
dependency packaging — lockfile formats, a shared dependency cache keyed by
digest, vulnerability scanning, install-time execution of arbitrary package
scripts — is a materially different, security-heavy problem (a shared
digest-keyed cache is a cross-tenant poisoning surface unless designed
against ADR-022 directly) and is **out of scope for this ADR**. It is a
follow-up ADR once project-file bundling has shipped and been used. This
resolves most of the "decisions required" questions below in favor of "not
yet, out of scope" rather than leaving them open indefinitely.

#### Python

The Python packager resolves the pipeline entry module, relative imports,
and same-repo helper modules. It records the Python version. Third-party
dependencies are not packaged in v1 (see scope note); a task that imports
one fails packaging with a clear, named error rather than silently
succeeding and failing at run time.

#### TypeScript

The TypeScript packager resolves the pipeline entry module, inline or named
task functions, and relative imports. It records the TypeScript/Node version
constraint. The generated artifact must be executable by the Node worker
without requiring the worker to transpile user code at run time unless a
later ADR explicitly adds that capability; v1 packaging performs any needed
transpilation at build time, on the SDK/CLI side, never on the worker.
Third-party npm dependencies are out of scope for v1, per the scope note.

#### Bundle loading is an ADR-030 amendment, stated here explicitly

This is not conditional — Decision 2's `task_bundle` field, by definition,
means a worker loads more than one file, and today's execution contract does
not support that. Verified against the current implementation: the
TypeScript worker's `vm.createContext` is constructed with
`codeGeneration: { strings: false, wasm: false }` and explicitly sandboxes
out `require`/`module`/`process` (`pkg/codeexec/jswrapper/contract.mjs`);
the Python worker's `sys.path` is exactly the materialized wrapper directory
(`pkg/codeexec/pywrapper/worker_main.py`). Neither can load a project's
files today, by design — ADR-030 restricted the namespace to exactly what a
single opaque script needs.

This ADR therefore amends ADR-030: the worker gains a bundle-loading mode,
entered only when the exec frame carries a `task_bundle` reference,
constrained the same way the rest of this contract is constrained —

- The loader resolves imports **only within the mounted bundle's own file
  set**, verified against the manifest's file list; any import escaping the
  bundle (absolute paths, `../` past the bundle root, a bare specifier not
  present in the bundle) is refused before the task runs, not caught as a
  runtime error.
- For TypeScript: `vm.SourceTextModule` (or an equivalent module linker) with
  a custom resolver enforcing the rule above — `codeGeneration` stays
  disabled; the bundle ships already-transpiled/already-resolved code, not
  source requiring further compilation on the worker.
- For Python: a per-execution, per-bundle `sys.path` entry scoped to the
  mounted bundle directory only, never the host's site-packages or any
  other bundle's directory.
- The manifest's declared file list is authoritative: a bundle attempting to
  load a file it did not declare is refused, the same way ADR-030's
  namespace refuses undeclared capabilities.

The follow-up list restates that ADR-029/030 need a formal amendment PR
alongside the bundle implementation, not "if" — the sandbox change is
required by this ADR's own decision, known today, and should be designed
before implementation starts rather than discovered midway.

#### Warm pool interaction — bundles must not cross-contaminate workers

The ADR-029 pool keeps a warm interpreter per (language, interpreter,
limits) and reuses its module cache across executions — that reuse is the
entire performance win, and it is exactly what makes bundles dangerous if
unaddressed: a Python warm worker's `sys.modules` is process-global, so two
tasks bundled from different versions of the same-named helper module,
executed on the same warm worker, would silently see each other's cached
module — the precise cross-task pollution the fresh-namespace-per-exec
design otherwise eliminates.

The sub-pool key (already `(language, interpreter, limits)`) must include
the bundle digest wherever a `task_bundle` is present, so a worker only ever
serves executions of the same bundle. This makes warm reuse per-bundle
rather than per-language: colder for a pipeline with many distinct task
bundles, but never cross-contaminated. Bare `code` (script, no bundle) is
unaffected and keeps today's pooling exactly as is. This must be measured,
not assumed, once implemented — the same discipline the earlier arc used
for the spawn-vs-pool benchmark.

#### JVM languages (deferred, not designed here)

JVM support is explicitly **not** designed by this ADR beyond reserving the
`language` field's future values. ADR-030 deferred Go for the identical
reason and stated it plainly: compiled-artifact distribution, classloader
warm-pool semantics, and a resource model are a different problem that
"deserves its own ADR," designed together with its worker — not fixed
normatively ahead of either existing. A JVM packaging spike happens after a
JVM worker ADR exists, not before.

### 4. Pipeline editor behavior

The UI displays a task as a Task node, not as a large script editor. It shows:

- Task name and language.
- Runtime constraint.
- Repository, file, and entrypoint when source metadata is available.
- Bundle digest and version.
- Input/output schema.
- Retry, timeout, memory, and CPU settings.
- Run logs, warnings, metrics, and failure diagnostics.

The pipeline editor may support a small inline-task mode for quick
experiments. Inline tasks written in the UI compile to a plain `code` node
through the existing ADR-029/030 execution path — never to a synthesized
one-file task bundle. This is a deliberate boundary, not an oversight: "the
server never builds, bundles are SDK/CLI-produced" (Decision 5) has to hold
without a UI-shaped exception, or it is not actually true. Repository-backed
tasks remain source-controlled artifacts; editing the graph must not rewrite
an external project without an explicit source-control integration.

### 5. Deployment behavior

`brokoli deploy pipelines/orders.ts` or its Python equivalent performs these
steps, entirely on the SDK/CLI side — the server never builds:

1. Load the pipeline entry file.
2. Discover task entrypoints.
3. Resolve relative imports and same-repo helper modules (v1 scope; no
   third-party dependency resolution — see Decision 3).
4. Resolve the language-specific runtime.
5. Derive schema, build, and validate the task bundle deterministically
   (Decision 2's reproducibility requirement).
6. Validate the pipeline IR, including the `task-bundles` capability
   preflight against the target server (Decision 2).
7. Upload the content-addressed bundle, or skip upload if the server
   already holds that exact digest (re-verified, per Decision 6, not
   trusted from the client's claim).
8. Deploy the pipeline IR with the bundle digest reference.

Deployment must fail before creating or updating the pipeline when bundling,
schema derivation/verification, runtime compatibility, or capability
preflight fails.

### 6. Security constraints

- **Upload requires an authenticated, permissioned actor.** Bundle upload is
  a new write surface and must sit behind the same RBAC model as pipeline
  deploy, not a separate, looser check.
- **The server re-verifies the digest against the archive's actual bytes at
  upload time, and the worker re-verifies again after fetching from cache**
  (mirroring ADR-016 §4's worker-side digest check) — a tampered or
  corrupted stored artifact must never reach execution unnoticed.
- **Bundles are tenant-isolated**, per ADR-022: a bundle uploaded by one
  organization is never fetchable or reusable by another, even by digest
  collision across tenants — the cache key is scoped by tenant, not global.
- **A hard size cap** on uploaded bundles, enforced server-side before
  storage, not just documented as a client convention.
- **No dependency installation happens on the worker at run time, ever** —
  restated here because it is the one place "resolve dependencies" language
  earlier in this ADR could be misread as deferring the decision rather than
  ruling it out. v1 has no third-party dependencies to install in the first
  place (Decision 3); this constraint still binds any future dependency ADR.

### Decisions required from the team

Before implementation, the team must settle:

- Whether a trusted build service is supported in addition to local
  SDK/CLI bundling (upload path and auth are identical either way).
- Whether inline tasks compiled from the UI ever graduate to a real bundle,
  or remain `code` nodes permanently (Decision 4 defaults to the latter).
- The bundle size cap and its enforcement point.
- The exact TypeScript module-linking mechanism for bundle loading
  (`vm.SourceTextModule` vs. an alternative), decided during the ADR-030
  amendment this ADR requires (Decision 3).

Deliberately *not* left open, because v1 scope (Decision 3) already answers
them: third-party lockfile formats, a shared dependency cache, and JVM
entrypoint shape are out of scope until their own follow-up ADRs.

## Consequences

### Positive

- Tasks become normal, testable Python and TypeScript code rather than strings.
- Relative imports and shared helpers become explicit, verifiable deployment
  inputs — schema is derived from the code, never a second hand-maintained
  declaration (Decision 1), so this ADR adds no instance of the AIP-108
  dual-declaration failure.
- The UI can remain a graph editor while source control remains authoritative
  for task code; inline UI tasks stay on the existing `code` path (Decision 4).
- Immutable, reproducibility-tested bundle digests make deployment and run
  history genuinely reproducible, not just labeled as such (Decision 2).
- TaskBundle extends the ADR-016 package format instead of duplicating its
  distribution, verification, and worker-fetch machinery.
- `code` remains available for low-level scripts, migrations, and experiments.

### Negative

- The pool's warm-reuse granularity becomes per-bundle rather than
  per-language for task-bundle executions — a real, measurable cost that
  must be benchmarked once implemented, not assumed acceptable.
- Uploads, digest re-verification, tenant-scoped storage, and size limits
  become part of deployment operations (Decision 6).
- Runtime compatibility failures move earlier into deployment, where their
  diagnostics must be clear.
- Bundle identity, source identity, and pipeline identity must all be shown in
  support tooling.
- The worker execution contract (ADR-030) requires a real amendment — a
  constrained, bundle-scoped module loader — before any of this can execute;
  this is now committed work, not a maybe.

### Deferred

- Third-party dependency packaging for any language: lockfile formats, a
  shared dependency cache, vulnerability scanning — explicitly out of v1
  scope (Decision 3), not merely unscheduled.
- Arbitrary build execution on the Brokoli server (never in scope at all;
  restated in Decision 6).
- JVM packaging and worker implementation, until a JVM worker ADR exists.
- Cross-language task composition inside one task bundle.
- Automatic source-control commits from the pipeline editor.
- A trusted build service as an alternative to local SDK/CLI bundling.

## Alternatives considered

- **Keep raw `code` strings as the primary API** — rejected: poor IDE support,
  no dependable project imports, and no reproducible dependency boundary.
- **Serialize only the callback/function source** — rejected: works for toy
  tasks but loses relative imports, helpers, closures, and dependency metadata.
- **Build projects on the Brokoli server at run time** — rejected: makes runs
  dependent on network/package registries and creates non-reproducible builds.
- **Use a separate packaging protocol per SDK** — rejected: duplicates server
  integration and prevents a consistent UI and run-history model.
- **Make the UI the source of truth for task code** — rejected: unsuitable for
  multi-file projects, code review, IDE workflows, and normal CI systems.
- **Ship a full language runtime inside every task bundle** — rejected for
  now: conflicts with host runtime resolution in ADR-026 and increases bundle
  size and security surface.
- **Invent a new archive/distribution format for task bundles** — rejected:
  ADR-016 already ships a content-addressed, digest-verified, fetch-by-digest
  package format built for exactly this shape of problem; a second format
  would duplicate tested server and worker code for no benefit (Decision 2).
- **Accept an author-declared schema at face value** — rejected: unverifiable
  against the real function, it is a hand-synced second declaration of the
  same fact the AIP-108 comparison identified as the core failure mode to
  avoid (Decision 1).
- **Package third-party dependencies in v1** — rejected for v1: conflates a
  large, security-sensitive problem (lockfiles, a shared cache, supply-chain
  scanning) with the much smaller, high-value problem of project-relative
  imports; deferred to its own ADR once the latter has shipped (Decision 3).

## Follow-ups

- Define the versioned TaskBundle manifest as an ADR-016 payload kind, and
  its upload API.
- Draft the ADR-030 amendment for bundle-scoped module loading (TypeScript
  `vm.SourceTextModule` or equivalent; Python per-bundle `sys.path` scoping)
  — a prerequisite for implementation, not an afterthought.
- Add the `pipeline-ir-2.1.json` schema entry and canonicalization rule for
  `task_bundle`, the `task-bundles` execution feature, and the differential
  fixtures proving cross-SDK byte-identical IR for it.
- Extend the pool's sub-key to include the bundle digest; benchmark warm
  reuse under per-bundle granularity.
- Add TypeScript single-file and project-bundle prototypes (project files
  only, no third-party dependencies).
- Add Python project-bundle support while preserving decorator ergonomics
  (same v1 scope).
- Add core validation and execution tests for bundle digest references,
  including the tenant-isolation and digest-tamper cases from Decision 6.
- Add UI task-node metadata and source-link display; confirm inline tasks
  compile to `code`, never a synthesized bundle.
- Add CLI dry-run output showing discovered files, derived schema, runtime,
  and final bundle digest.
- Open a follow-up ADR for third-party dependency packaging once project-file
  bundling has shipped and been used.
- Open a follow-up ADR for JVM packaging once a JVM worker ADR exists.

## Update (2026-09-05)

Status corrected to `accepted`: this ADR's `task-bundle/1` design shipped
and released in v0.11.0, and brokoli-sdk already emits it
(`@task(package="bundle")`, `brokoli-sdk#90`). ADR-033 later defines a
second format, `task-bundle/v2`, for a new `task` IR node; the two do not
replace each other, and `task-bundle/1` on the `code` node keeps working
unchanged. See ADR-034 for the full reconciliation between this ADR and
ADR-032/033.
