# ADR-045: Optional native worker capabilities

**Status:** proposed
**Date:** 2026-10-04

## Context

Brokoli's default binary is deliberately CGO-free. Its built-in database
connections use Go-native drivers and are appropriate for the operational
hot paths that Brokoli already owns: PostgreSQL, MySQL, SQLite, SQL Server
and ClickHouse. That keeps the control plane small, portable and simple to
install.

That shape does not cover the emerging columnar data-plane use cases well:

- DuckDB is useful for local lake queries, Parquet/Iceberg work and bounded
  cross-source transforms, but embeds native code.
- ADBC presents a common Arrow-oriented API over separately distributed
  drivers. Its Go driver manager wraps the C driver manager and requires
  CGO. A driver is a shared library plus a manifest, not a Go import the
  control plane should carry.
- A worker must not receive a task requiring a native library or driver it
  does not have. Today the plugin package system can install verified `.bkg`
  archives by slug, but it does not describe ADBC drivers or route work by
  worker capability.

A separate worker product would solve the build problem but make the common
single-binary deployment worse. A person running `brokoli serve` should not
need to discover and operate a second application merely because one pipeline
uses a local columnar transform. At the same time, a faulty C driver must not
be able to take down the control plane or every task on a worker.

## Decision

Brokoli keeps **one command-line interface and one process model**, with
optional native data-plane capabilities supplied by build flavor and verified
driver packages.

### 1. One CLI, explicit modes

The command surface is one `brokoli` program:

```text
brokoli serve                 # control plane and embedded local worker
brokoli worker --server ...   # external worker using the same executable
brokoli worker task ...       # internal child-task entry point
```

`serve` retains today's all-in-one behavior. When a task requires process
isolation, the embedded worker re-executes `os.Executable()` as `worker task`
with a task envelope supplied over a private pipe or file descriptor. An
enterprise deployment may run the exact same binary in `worker` mode on a
separate machine or pool. There is no separately branded or separately
configured worker product.

The parent process owns scheduling, credential resolution, logs, cancellation
and checkpoint persistence. A task child owns native driver calls and exits
after one task, or after a deliberately bounded pool lifetime. A child crash
is classified as a retryable task failure with its exit signal recorded; it
does not crash the scheduler or API process.

### 2. Two distribution flavors, identical commands

The release produces two flavors of the same CLI:

| Flavor | Contents | Intended use |
| --- | --- | --- |
| `default` | Existing pure-Go control plane and native hot-path drivers | Current deployments, API/scheduler nodes, ordinary database pipelines |
| `full` | Default contents plus CGO-enabled ADBC driver manager and DuckDB worker support | Workers that run ADBC, DuckDB or Arrow-native task stages |

The default flavor remains `CGO_ENABLED=0` and does not link DuckDB, the ADBC
driver manager or arbitrary shared libraries. The full flavor uses explicit
build tags, initially `adbc` and `duckdb`; a worker advertises which tags were
compiled in rather than assuming every full build is identical.

Both artifacts accept the same commands and configuration. A capability error
names the absent feature and the compatible artifact, rather than producing an
unknown connection or driver error.

Binary size, platform coverage, static versus dynamic linking, and license
obligations are release gates. They are measured on every supported
GOOS/GOARCH before an artifact is advertised; this ADR makes no size promise.

### 3. Optional drivers are installed by slug

ADBC drivers remain files outside the Brokoli executable. Brokoli owns their
installation directory and does not depend on the machine-wide driver-manager
search path or require users to install the external `dbc` tool.

```text
brokoli driver list
brokoli driver install <slug>
brokoli driver remove <slug>
```

The curated driver index is separate from the core binary and contains, at a
minimum, a canonical slug, version, supported platforms, archive URL,
manifest location and SHA-256 digest. The install command downloads only an
allowlisted slug, verifies the archive and library digests before extraction,
installs atomically into Brokoli's data directory, and writes no credentials
to the driver manifest. Private or air-gapped installations use an
operator-configured mirror, following the existing plugin-index model.

Installed drivers are addressed by canonical slug, for example
`flightsql`, `duckdb`, or a vendor-maintained driver slug. The scheduler and
run record carry the selected slug, version and content digest, never an
unverified filesystem path. A driver update is an explicit new install and
does not silently change a pipeline's execution environment.

The existing signed `.bkg` plugin catalog remains the delivery mechanism for
optional connector packs. A pack may declare that it requires `adbc:<slug>`
and/or `duckdb`; the driver library itself is a separately verified artifact.
New long-tail connectors begin as pack-defined source/sink nodes rather than
new compiled `ConnectionType` values. A future saved-connection abstraction
for pack-defined connectors must be separately designed; it cannot make a
missing pack look like a built-in connection.

### 4. Capability-based task routing

Every worker reports an immutable capability document at registration and on
change:

```json
{
  "build": ["purego", "adbc", "duckdb"],
  "adbc_drivers": [{"slug": "flightsql", "version": "...", "sha256": "..."}],
  "duckdb": {"version": "..."},
  "platform": {"os": "linux", "arch": "amd64"}
}
```

A task declares the capabilities it requires before dispatch. The scheduler
selects only a compatible worker. It fails closed when none exists, with an
error that names the missing build feature or driver slug. It must not send a
task to an incompatible worker and discover the absence after credential
resolution or partial execution.

The local worker started by `serve` registers the same document, so the
all-in-one and distributed paths make the same routing decision. Enterprise
queue, KEDA and worker-pool implementations carry the requirement unchanged;
labels are a deployment optimization, not the authorization decision.

### 5. Arrow reader boundary

Optional worker sources and sinks converge on a bounded Arrow reader/writer
boundary:

```go
type Source interface {
    Open(context.Context, ConnectionSpec) (arrow.RecordReader, error)
}

type Sink interface {
    Write(context.Context, arrow.RecordReader) error
}
```

Existing native Go paths remain valid. They may adapt their current row/batch
representation at the boundary rather than being rewritten as part of this
work. An ADBC source returns Arrow records through its driver, while an ADBC
sink uses the driver's supported bulk-ingest operation only after that
driver's behavior is tested.

DuckDB is an optional worker stage. It may consume a registered Arrow stream
as a view, execute a Brokoli-visible SQL transform, and emit another Arrow
reader. This is the preferred path for local joins and lake queries; it is
not a claim that DuckDB replaces the database-specific hot paths.

The Arrow C Data Interface makes zero-copy interchange possible, but this ADR
does **not** promise zero-copy. The actual Go ADBC binding, DuckDB binding,
record ownership, release order, schema fidelity and memory use must be
demonstrated by correctness and benchmark tests before the implementation is
advertised as zero-copy.

### 6. Resource, process and network isolation

Native task children receive a bounded resource policy derived from their
cgroup limit:

- DuckDB memory limit, thread count and temporary spill directory are set per
  task before any query runs.
- Arrow reader buffers are bounded and budgeted alongside DuckDB's allocator;
  the combined budget, not either subsystem alone, controls admission.
- The temporary directory is task-scoped, permission-restricted and removed
  after the task. Spill capacity is part of worker capability/health.
- Credentials are resolved for one task and passed through an inherited pipe
  or memory-backed file descriptor. They never appear in argv, driver
  manifests, task logs, checkpoints or a reusable process environment.
- DuckDB external access and extension auto-install are disabled by default.
  File or S3 access is an explicit pipeline capability, subject to the same
  outbound and filesystem policy as every other source.

An ADBC driver, DuckDB, or native extension crash kills one child process.
The parent records the exit signal, preserves an already durable checkpoint,
and applies the task retry policy. A repeated crash is surfaced as a
capability/driver failure rather than retried indefinitely.

### 7. Semantics and lineage

The control plane stores connection references, selected driver slugs and
declared capabilities. It does not store driver paths, native handles or
credentials in manifests.

SQL submitted to a DuckDB transform is Brokoli-authored/visible and is parsed
for the strongest lineage the parser can establish. Opaque SQL supplied to an
ADBC driver is recorded as table-level lineage only. The lineage record names
the source kind, driver slug/version/digest, DuckDB version when used, and
whether the reported columns are parsed or opaque.

## Consequences

### Positive

- A default installation remains as simple and portable as today.
- Users who need DuckDB or ADBC download one compatible CLI flavor and only
  the driver slugs they use.
- ADBC adds a common path for Arrow-oriented long-tail connectors without
  adding every driver dependency to the control plane.
- Worker pools can be deliberately small and specialized: a default pool for
  ordinary work, a DuckDB pool for lake transforms, and driver-specific pools
  where policy requires them.
- Native crashes have a bounded blast radius and normal checkpoint/retry
  behavior.

### Negative

- Release engineering gains a CGO matrix, native-library licensing review and
  per-platform build/test work.
- Scheduling, task envelopes and worker registration gain an explicit
  capability contract.
- Two memory managers and child-process IPC make memory/cancellation bugs
  more likely unless budgets and ownership are tested as first-class behavior.
- Connector authors must package and maintain their driver dependencies; a
  plugin slug alone is not enough to make a native driver usable.

### Not decided here

- Which ADBC drivers are admitted to the curated index. Each needs its own
  license, platform, authentication, type-fidelity and bulk-ingest review.
- Whether a future pack-defined connector can create a first-class saved
  connection type in the existing Connections UI.
- Whether task children are one-per-task or a bounded recyclable pool. The
  initial implementation is one-per-task because it is the only form that
  fully contains C-library state and credential lifetime.
- Whether DuckDB's ADBC extension can consume arbitrary ADBC drivers. It is
  not an architectural dependency until its status, security model and Arrow
  ownership behavior are independently verified.

## Implementation gates

Implementation proceeds in this order:

1. Add the worker capability document, compatibility validation and clear
   fail-closed errors, without CGO dependencies.
2. Add `worker` and `worker task` modes and prove existing pure-Go tasks can
   run, cancel, log, checkpoint and retry through the child boundary.
3. Add the signed `driver list/install/remove` catalog and content-addressed
   worker cache.
4. Add one ADBC source, initially Flight SQL, with Arrow lifecycle, bounded
   memory, cancellation and type/null test coverage.
5. Add DuckDB under the `duckdb` build tag with cgroup-aware settings and
   sandbox tests.
6. Demonstrate or reject zero-copy Arrow interchange with benchmarks and
   ownership/leak tests.
7. Add ADBC sinks and further drivers one at a time, only after each earns
   its write and transaction semantics.

Trino through ADBC is a later optional source pack. It does not replace the
separate experimental Trino integration and must pass the same driver and
capability gates.
