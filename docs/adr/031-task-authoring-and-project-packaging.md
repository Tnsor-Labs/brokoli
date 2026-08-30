# ADR-031: Task authoring and project packaging for multi-language pipelines

**Status:** proposed
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
- Explicit input and output schema metadata where inference is insufficient.
- Local invocation or test execution using the same function.
- Resource settings and runtime selection already represented by code-node
  configuration.

### 2. Task bundle contract

Each SDK produces a bundle with a common manifest and language-specific
payload:

```text
TaskBundle
|-- manifest.json
|-- source or compiled payload
|-- dependency metadata
`-- integrity metadata
```

The manifest records at least:

- Bundle format version.
- Language and runtime constraint.
- Entrypoint and task name.
- Source and dependency digests.
- Declared input/output schema, when supplied.
- Build tool and build options.
- Dependency lockfile digest.
- Files required by the task.

The bundle is content-addressed and immutable. A pipeline version references a
bundle digest; changing source, dependencies, or build configuration produces
a new digest. The server must not silently rebuild an existing digest.

The bundle is an execution artifact, not a second pipeline IR. Core continues
to validate the pipeline graph and dispatch the task according to the
language/runtime fields already defined by ADR-030.

### 3. Language-specific packaging

The common manifest is shared, but each SDK owns the native packaging step.

#### Python

The Python packager resolves the pipeline entry module, relative imports, and
declared project dependencies. It records the Python version and lockfile.
The first implementation should support the repository's declared packaging
workflow rather than silently installing arbitrary dependencies at run time.

#### TypeScript

The TypeScript packager resolves the pipeline entry module, inline or named
task functions, relative imports, `package.json`, and the selected lockfile.
The generated artifact must be executable by the Node worker without requiring
the worker to transpile user code at run time unless a later ADR explicitly
adds that capability.

#### JVM languages

JVM support uses the same bundle manifest and ships a compiled payload plus
its resolved classpath. The entrypoint identifies the class and callable
method. Java, Kotlin, and Scala SDKs may use Maven or Gradle during packaging,
but the server receives a reproducible runtime payload rather than running a
build during pipeline execution.

The JVM worker and its resource enforcement are a later implementation phase;
this ADR only fixes the packaging boundary and metadata shape.

### 4. Pipeline editor behavior

The UI displays a task as a Task node, not as a large script editor. It shows:

- Task name and language.
- Runtime constraint.
- Repository, file, and entrypoint when source metadata is available.
- Bundle digest and version.
- Input/output schema.
- Retry, timeout, memory, and CPU settings.
- Run logs, warnings, metrics, and failure diagnostics.

The pipeline editor may support a small inline-task mode for quick experiments.
Repository-backed tasks remain source-controlled artifacts; editing the graph
must not rewrite an external project without an explicit source-control
integration.

### 5. Deployment behavior

`brokoli deploy pipelines/orders.ts` or its Python equivalent performs these
steps:

1. Load the pipeline entry file.
2. Discover task entrypoints.
3. Resolve relative imports and declared dependencies.
4. Resolve the language-specific runtime and build tool.
5. Build and validate the task bundle.
6. Validate the pipeline IR and required server capabilities.
7. Upload or reuse the content-addressed bundle.
8. Deploy the pipeline IR with the bundle digest reference.

Deployment must fail before creating or updating the pipeline when bundling,
dependency resolution, runtime compatibility, or capability preflight fails.

### Decisions required from the team

Before implementation, the team must settle:

- Whether bundling happens in the SDK/CLI only, or whether a trusted build
  service is also supported.
- Which Python lockfile formats are required for the first release.
- Which TypeScript bundler and lockfile formats are supported initially.
- Whether dependencies are uploaded inside each bundle or referenced from a
  server-side cache by digest.
- Whether inline tasks and repository-backed tasks share one bundle format in
  the first implementation.
- Whether JVM support begins with a generic JAR entrypoint or a Kotlin-first
  SDK.
- Whether the UI may edit inline tasks or only inspect and configure them.

## Consequences

### Positive

- Tasks become normal, testable Python and TypeScript code rather than strings.
- Relative imports, shared helpers, and project dependencies become explicit
  deployment inputs.
- The UI can remain a graph editor while source control remains authoritative
  for task code.
- Immutable bundle digests make deployment and run history reproducible.
- Python, TypeScript, and future JVM SDKs share one server-side artifact model.
- `code` remains available for low-level scripts, migrations, and experiments.

### Negative

- SDKs need language-specific dependency discovery and build tooling.
- Uploads, artifact storage, scanning, and cache invalidation become part of
  deployment operations.
- Runtime compatibility failures move earlier into deployment, where their
  diagnostics must be clear.
- Bundle identity, source identity, and pipeline identity must all be shown in
  support tooling.

### Deferred

- A universal dependency resolver across Python, Node, and JVM ecosystems.
- Arbitrary build execution on the Brokoli server.
- JVM worker implementation and JVM-specific sandbox enforcement.
- Cross-language task composition inside one task bundle.
- Automatic source-control commits from the pipeline editor.
- On-worker TypeScript transpilation and framework-specific Python packaging.

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

## Follow-ups

- Define the versioned TaskBundle manifest and upload API.
- Add TypeScript single-file and project-bundle prototypes.
- Add Python project-bundle support while preserving decorator ergonomics.
- Add core validation and execution tests for bundle digest references.
- Add UI task-node metadata and source-link display.
- Add CLI dry-run output showing discovered files, dependencies, runtime, and
  final bundle digest.
- Create a JVM packaging spike after the Python and TypeScript contract is
  reviewed.
- Update ADR-029 and ADR-030 if the bundle changes the worker protocol or
  language execution contract.
