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

There are two deliberately different Arrow boundaries:

- **Fused native stages in one task child** pass an `arrow.RecordReader`
  directly through the Arrow C Data Interface. An ADBC source may feed DuckDB,
  then an ADBC or native sink, without a `common.DataSet`, row map or Arrow IPC
  file between stages. This path is ephemeral: a child crash restarts the
  segment from its last durable input.
- **Durable or distributed boundaries** use Arrow IPC artifacts. Checkpoints,
  retries, fan-out, remote-worker transfer and durable intermediate outputs
  require bytes that outlive a process, so IPC's serialization cost is an
  explicit correctness trade, not a failed attempt at zero-copy.

The Arrow C Data Interface makes a low-copy in-process handoff possible, but
this ADR does **not** promise end-to-end zero-copy. Network drivers may still
decode and allocate before presenting an Arrow reader, and native consumers may
import into their own vectors. Record ownership, release order, schema fidelity
and memory use must be demonstrated by correctness and benchmark tests before
any path is advertised as zero-copy.

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

## Update (2026-10-04): Arrow boundary measurements

The first prototype measured a Dremio OSS Flight SQL source over a DuckDB
generated TPC-H SF1 `lineitem` scan: 6,001,215 rows, 16 typed columns and a
1.11 GB Arrow IPC result. These findings refine, rather than replace, the
decision above:

| Path | Median rows/s | Heap allocation/op | Allocations/op |
| --- | ---: | ---: | ---: |
| Go Flight SQL ADBC receive only | 426,052 | 1.53 GB | 254,001 |
| Go Flight SQL ADBC to Arrow IPC | 426,763 | 1.58 GB | 306,673 |
| Go Flight SQL ADBC to native DuckDB ADBC | 372,178 | 1.54 GB | 371,730 |
| Prebuilt Arrow reader to native DuckDB ADBC, 1M rows | 2,739,309 | 7 KiB | 71 |

The native DuckDB `ArrowArrayStream` handoff avoids row materialization and
does not copy the full result into the host Go heap. The host's pure-Go Flight
SQL driver's gRPC/protobuf decode dominates that host heap accounting. Arrow
IPC adds a measurable but secondary serialization cost.

A matched three-run comparison against the same Dremio SF1 query subsequently
tested Apache's Flight SQL driver through the ADBC driver manager. The host
pure-Go driver median was 525,042 rows/s, 1.51 GB/op and 255,779 host Go
allocations/op. The driver-manager path's median was 432,191 rows/s, 28.4 MB/op
and 228,982 host Go allocations/op. This result is not an allocation-saving or
throughput conclusion.

Inspection of the official `libadbc_driver_flightsql.so` v1.12.0 binary with
`go version -m` shows it is Go 1.26.5 built with `-buildmode=c-shared`, with
Arrow Go, gRPC and protobuf dependencies. Loading it embeds a second Go runtime
whose heap and GC are outside the host benchmark's `B/op` and allocation
accounting. One `/usr/bin/time -v` sample for each path reported essentially
equal peak RSS (1,100,928 KiB host Go; 1,100,416 KiB driver manager). The
numbers therefore show an accounting shift, not lower total process memory.

The fixed-order three-run samples also do not establish an 18% throughput
difference: pure-Go samples ranged from 11.10 to 13.48 seconds/op and
driver-manager samples ranged from 12.67 to 15.23 seconds/op. Any comparative
claim requires interleaved runs in one controlled session, at least ten samples
per path, fixed `GOMAXPROCS` and Dremio state, and `benchstat` analysis. Native
drivers must additionally be tested under concurrent pulls against the task
cgroup budget because each embedded Go runtime can apply `GOMEMLIMIT` as if it
owned the whole limit.

Accordingly, new Arrow-native plans must request a fused same-task-process
capability when they can use it, for example `adbc:flightsql`, `duckdb`, and
`same-task-process`. The scheduler may insert an IPC artifact only when
durability or distribution requires it. Flight SQL does not presently justify
a driver-manager source path over the pure-Go source. The next comparative
targets are a C++ ADBC PostgreSQL/libpq driver against the existing pgx hot
path, and a C++ Flight client baseline against Dremio. No end-to-end zero-copy
or total-memory claim is earned until ownership, throughput, and concurrent
cgroup-memory tests pass.

## Update (2026-10-04): Controlled comparison follow-up

Ten alternating, one-iteration Dremio Flight SQL runs were collected with a
fixed `GOMAXPROCS=8` and analysed with `benchstat`. The pure-Go receive path
was 12.57 s/op +/- 7% (477,400 rows/s +/- 7%); the driver-manager path was
12.48 s/op +/- 6% (481,000 rows/s +/- 5%). The distributions overlap at this
precision and do not support a throughput-selection claim. Their host Go `B/op`
values remain non-comparable because the
driver-manager library contains its own Go runtime.

A separate PyArrow 25.0.1 Flight transport baseline, which uses Arrow C++
transport with Python only as orchestration, received the same result at
544,189, 570,990 and 583,445 rows/s over three warm runs. Its process peak RSS
was 1.84 GiB. It establishes a faster client ceiling for this setup, not a
memory-efficient worker default.

The native PostgreSQL ADBC driver is a different candidate: its official
library has no Go build metadata and is native C++. In a five-run first-pass
read of one million typed rows, Brokoli's bounded pgx stream median was
2.53 s/op (394,931 rows/s) and the native Arrow reader median was 1.92 s/op
(520,884 rows/s). Host Go allocation was 424 MiB/op versus 24 KiB/op, but this
does not establish total allocation. Direct test-binary RSS samples were
58 MiB for pgx and 76 MiB for native ADBC. These source-read results merit a
type-fidelity, cancellation, concurrent-cgroup-memory and properly interleaved
follow-up; they do not yet change the PostgreSQL COPY write path.
