# ADR-042: Native BigQuery backend

**Status:** proposed
**Date:** 2026-09-27

## Context

The connection catalogue advertises BigQuery, and nothing can use it: there
is no driver, and the connection test says so. #685 asked for a design
before code, because BigQuery fits none of the assumptions the database
backends share.

### What BigQuery is

- An HTTPS API that runs **jobs** (query, load, copy), not a wire protocol
  with sessions. There is no host and port, and no connection pool to speak
  of.
- Authenticated with Google credentials: a service-account key (a JSON
  document), Application Default Credentials (the identity of the machine
  the process runs on), or workload identity federation (a short-lived token
  from an issuer the customer's Google Cloud project trusts).
- **Billed by bytes processed.** A query that scans a large table costs
  money whether or not the pipeline uses the rows. A probe written the
  `SELECT ... LIMIT 0` way still scans. Load jobs, copy jobs and dry runs
  are free.

### What the engine assumes

Every database path in the engine takes a **URI string**. The connection
resolver turns a node's `conn_id` into `resolved["uri"] = conn.BuildURI()`
before the node runs (`engine/connection_resolver.go`), and 11 call sites
then open a `database/sql` connection from that URI: `QueryDatabase`,
`StreamQueryDatabase` and `ExecuteSQL` in `engine/database.go`, the three
bulk writers, the column probes for pushdown and SQL transforms, and schema
transfer. The backend for a URI is chosen by its scheme through the claims
in `pkg/dbdialect` (#361).

So "route BigQuery by connection type" has nothing to route on at the point
where the engine opens a connection: by then there is only a URI. The
dispatch point is the design decision, not a follow-up.

### Constraints this must meet

- **Pure Go**, for the six `CGO_ENABLED=0` release targets (#685).
- **No credential in a URI.** URIs are logged, persisted and passed through
  generic code.
- **ADR-041's identity rules.** On a server that runs pipelines for several
  teams, the machine's own identity belongs to the operator, not to a
  workspace. Application Default Credentials is exactly that identity.
- **ADR-022's outbound policy.** Every request a node makes goes through
  `netguard`.
- **ADR-024's refusal rule.** A capability not built and tested is refused
  by name, never approximated.

### What the research found

From Google's documentation and the client source (`cloud.google.com/go/bigquery`
v1.85.0, 2026-09-24):

| Question | Finding |
| --- | --- |
| Pure Go? | Yes. No `import "C"`; `CGO_ENABLED=0` builds pass for linux, darwin and windows. Apache-2.0. |
| Size | About 17-19 MB added to a standalone binary. Its effect on Brokoli's 77 MB binary is not measured yet. It depends on `apache/arrow/go/v15`, while Brokoli ships `arrow-go/v18`, so the binary would carry two copies of Arrow. |
| Load jobs | Load from any `io.Reader` (`bigquery.NewReaderSource`): NDJSON, CSV, Avro, Parquet, ORC, no Cloud Storage needed. **Free.** **Atomic**: "either all records get inserted or none do". 1,500 per table per day, failures included; 100,000 per project per day; 15 TB per job. |
| Overwrite | A load job with `WRITE_TRUNCATE_DATA` "overwrites the data, but keeps the constraints and schema of the existing table", atomically. `WRITE_TRUNCATE` also replaces the schema and removes row-level access policies. |
| Storage Write API | gRPC. Rows must be protobuf messages built from a descriptor at run time (no Arrow in the Go writer). $0.025/GiB past 2 TiB a month. Only **pending** streams commit several batches atomically; a **committed** stream makes each batch visible as it is written. Append only. |
| `CREATE OR REPLACE TABLE ... AS SELECT` | A billed query. Removes row-level access policies. Cannot change the kind of partitioning. |
| Reads | Query results page through REST (100,000 rows per response, 3.7-7.5 GB/min). The Storage Read API (gRPC, Arrow) is faster, $1.10/TiB past 300 TiB a month; the Go client switches to it automatically once enabled. |
| Cost control | `maximum_bytes_billed`: a query estimated above it fails **without charge**. A dry run is free and returns the bytes it would process and the result schema. |
| Upsert | `MERGE` with an `UPDATE` clause is billed for the whole target table. At most 2 concurrent data-changing statements per table, 20 queued. |
| Credentials | `option.WithCredentialsJSON` is deprecated as a security risk; `WithAuthCredentialsJSON` takes the credential type explicitly. A workload-identity config file can tell the library to fetch any URL (`credential_source.url`) or run a local command (`credential_source.executable`). |
| Network | REST accepts a custom HTTP client. The Storage APIs are gRPC and take a custom dialer. Token fetches are a third path, with their own HTTP client option. |
| Emulator | No official one. `goccy/bigquery-emulator` (MIT, pure Go, last release 2026-06-13) supports query jobs, local load jobs, `WRITE_APPEND`/`WRITE_TRUNCATE`, dry runs and `MERGE`. It does **not** implement `WRITE_TRUNCATE_DATA` (it appends instead), `maximum_bytes_billed`, copy jobs, or atomic multi-stream commits. |

## Decision

BigQuery gets a **native backend** on Google's official Go client, not a
`database/sql` wrapper. It plugs into the engine's existing entry points
through a **secret-free `bigquery://` URI** claimed in `pkg/dbdialect`, and
looks up its credentials by `conn_id` on the machine that runs the node.
The first phase uses **REST only**: query jobs to read, **load jobs** to
append and overwrite. Credentials come through ADR-041's identity methods,
and every request goes through `netguard`.

### 1. Addressing: a URI that names where, never who

```
bigquery://<project>/<dataset>?location=<location>&billing_project=<project>
```

- `BuildURI` produces it from the connection: `schema` is
  `project_id.dataset`, as the catalogue already documents, and `location`
  and `billing_project` come from the extra settings.
- It carries **no credential**, so it is safe everywhere a URI goes today:
  logs, recorded SQL notes, lineage, persisted plans, work orders.
- `pkg/dbdialect` claims the `bigquery` scheme like any other backend. The
  claim names no `database/sql` driver. Instead, the entry points check for a
  native claim before `DetectDriver` and hand the call to the BigQuery
  implementation.

**Credentials are looked up where the node runs.** The resolved node config
keeps its `conn_id` beside the URI. The BigQuery backend resolves that
connection itself, through the run's `ConnectionResolver`, in the run's
workspace. This is the pattern remote `source_api` pages already use (#760):
a work order sent to another machine carries the reference, and the machine
that runs it reads the credential. Nothing secret enters the node config.

### 2. Identity: ADR-041's three methods

| Method | For BigQuery | Stored |
| --- | --- | --- |
| `token` | A service-account key, passed with `WithAuthCredentialsJSON(option.ServiceAccount, key)` | The key, encrypted, in the connection's extra settings or behind a `secret://` reference |
| `oidc` | Workload identity federation: Brokoli builds the external-account configuration itself, from the connection's audience and service account, around a token from ADR-041's `TokenSource` | Nothing secret |
| `ambient` | Application Default Credentials of the machine that runs the node | Nothing |

- **`ambient` follows ADR-041's switch.** A deployment that denies ambient
  identity (`BROKOLI_SECRET_STORE_AMBIENT=deny`) denies it here too, with the
  same message. Without that, any workspace on a shared server could query
  with the operator's own Google identity.
- **No uploaded external-account files.** Such a file can point the library
  at any URL or make it run a local command. Brokoli builds the configuration
  for `oidc` itself, and refuses a key whose `type` is not `service_account`.
- **Compatibility.** The catalogue's hint says the extra settings hold "Service
  account JSON key". An extra document whose top-level `type` is
  `service_account` is therefore read as the key itself. Otherwise the extra
  settings are an object with `credentials`, `location`, `billing_project`
  and `maximum_bytes_billed`.

### 3. Network: every request through `netguard`

- **REST:** the client gets an HTTP client built on `netguard.Outbound()`,
  with Google's authenticating transport layered over it.
- **Token fetches** (service-account and workload-identity exchanges): the
  auth library's own HTTP client option, also on `netguard`.
- **gRPC:** not used in phase 1. When the Storage APIs arrive (phase 2),
  their connections dial through `netguard.Policy.DialContext` via
  `grpc.WithContextDialer`.

Google's public endpoints pass the default policy. An emulator or a private
endpoint needs the operator's allowlist, as for S3 and Azure Blob.

### 4. Reading

- A `source_db` query runs as a **query job**, with named parameters where
  the node has them.
- **Every query carries `maximum_bytes_billed`.** The default is 10 GiB, set
  per connection and overridable per node. Removing the cap takes an explicit
  `0`, and the node log says the query is uncapped. A query over the cap
  fails before it runs, at no charge, and the error says so and names the
  estimate.
- **Column discovery uses a dry run**, never a `LIMIT 0` query. It is free,
  and it returns both the schema and the estimated bytes, which go in the
  node log.
- Results page through REST into the existing batch and streaming paths
  (`QueryDatabase`, `StreamQueryDatabase`), so memory stays bounded to one
  page.
- Each job carries labels (`brokoli_run_id`, `brokoli_pipeline_id`), so the
  customer's billing export and audit logs attribute every job to a run. The
  node log records each job's ID, bytes processed and bytes billed.

### 5. Writing: load jobs

A `sink_db` write is **one load job**, fed through an `io.Pipe` from the node's
batches as NDJSON, so memory stays bounded to one batch on both the batch
and streaming paths.

| Mode | Load job | Why |
| --- | --- | --- |
| append | `WRITE_APPEND` | Free, and atomic: a failed job adds nothing. |
| overwrite | `WRITE_TRUNCATE_DATA` | Free and atomic. Keeps the table's schema and constraints, where `WRITE_TRUNCATE` would replace them and drop row-level access policies. |
| create_table | `CREATE_IF_NEEDED`, with a schema from the dataset's declared column types | Same job, so a failed write leaves no empty table behind. |
| upsert | **refused by name** | `MERGE` bills a full scan of the target on every write. It needs its own design, with a cost warning. |

The quota of 1,500 load jobs per table per day, failures included, means one
write every minute or so. That fits batch pipelines. A pipeline scheduled
more often than every 5 minutes against one BigQuery table gets a validation
warning naming the quota. A high-frequency writer is what phase 3's Storage
Write API is for.

### 6. What is refused

Anything not built and tested in the phase that ships it is refused by name
(ADR-024):

- SQL generation for writes (`GenerateSQL`), pushdown, and SQL transforms
  that probe a query's columns through `database/sql`.
- `migrate` with BigQuery on either side, until it is wired to the read and
  load paths.
- dbt on a BigQuery connection (dbt's own `dbt-bigquery` adapter is a
  separate question).
- Upsert.

The message says "BigQuery does not support <operation> in this build",
not a driver error.

### 7. Testing

- **CI** runs `goccy/bigquery-emulator` as a service in
  `docker-compose.test.yml` and a CI job, like MinIO and Azurite. It covers
  reads, dry runs, append, create-table and failure paths. Using the
  container, not the Go package, keeps the emulator's SQLite engine out of
  `go.mod`.
- **The emulator cannot prove three things:** `WRITE_TRUNCATE_DATA` (it
  appends instead), `maximum_bytes_billed`, and atomicity on a real failure.
  An opt-in test against a real project
  (`BROKOLI_TEST_BIGQUERY_PROJECT`, run before each release that touches the
  backend) covers those. CI never needs Google credentials.
- **Release constraints**, as for Azure Blob (#687): all six cross-builds,
  the license gate, and the binary-size delta recorded in the PR.

## Consequences

### Positive

- BigQuery's jobs, billing and credentials stay visible, instead of being
  forced into a host, port and password.
- Writes are free and atomic from the first release.
- There are no protobuf descriptors and no gRPC in phase 1.
- The engine's entry points gain one native branch. No parallel dispatch
  surface is built.
- A query cannot run up an unbounded bill by default, and every job is
  attributed to its run in the customer's own logs.
- On a shared server, the operator's Google identity is not reachable from
  a workspace.

### Negative

- About 17-19 MB of client code, and a second copy of Arrow until Google's
  client moves to a newer Arrow.
- Reads page through REST, which is slower than the Storage Read API for
  large results, until phase 2.
- The load-job quota limits how often one table can be written.
- `WRITE_TRUNCATE_DATA` and `maximum_bytes_billed` can only be verified
  against real BigQuery, not in CI.
- Each entry point needs a native branch, and each unsupported one needs a
  named refusal.

### Deferred

- **Phase 2:** the Storage Read API for large reads (gRPC via `netguard`'s
  dialer, Arrow into the existing Arrow path).
- **Phase 3:** the Storage Write API with **pending** streams, committed
  atomically, for writers that exceed the load-job quota. `MERGE` upsert with
  a cost estimate shown before the write.
- Parquet instead of NDJSON for load jobs, if measurement shows it is worth
  the encoder.
- Pushdown and a BigQuery SQL dialect.
- Dataset and table browsing in the UI.

## Alternatives considered

- **A `database/sql` wrapper** around the official client. It fits the
  engine's URI shape with the least engine change. Rejected: it hides jobs
  and cost, writes become billed `INSERT` statements under the DML
  concurrency limit, and the credential has to reach the DSN.
- **The Storage Write API for append**, as first proposed. Rejected for
  phase 1: it needs run-time protobuf descriptors, it is gRPC, and it is
  billed past 2 TiB a month. With a committed stream a failed run leaves the
  batches already written, and only pending streams are atomic across
  batches. Kept for phase 3, with pending streams.
- **Staging table plus `CREATE OR REPLACE TABLE ... AS SELECT` for
  overwrite**, as first proposed. Rejected: a billed query, removes
  row-level access policies, cannot change partitioning, and leaves a
  staging table to clean up.
- **Staging table plus a copy job with `WRITE_TRUNCATE`.** Free and atomic,
  but it replaces the table's schema with the staging table's, removes tags,
  and needs a staging table. A load job with `WRITE_TRUNCATE_DATA` does the
  same in one step and keeps the schema.
- **A multi-statement transaction** (`TRUNCATE` then `INSERT`). Atomic and
  keeps policies, but the `INSERT ... SELECT` from staging is a billed query.
- **Hand-written REST calls.** The official client supplies authentication,
  retries, job polling and typed models. Not worth maintaining ourselves.
- **Application Default Credentials as the default**, as first proposed.
  Rejected: on a shared server it is the operator's identity, which ADR-041
  denies to workspaces.

## Follow-ups

1. Measure the binary-size delta against Brokoli's own binary, and run the
   license gate, before adding the dependency.
2. Add the `bigquery` claim to `pkg/dbdialect`, the native branch in each
   entry point, and a named refusal in every one not built.
3. Implement identity (`token` first, then `oidc` once ADR-041's
   `TokenSource` exists), the `netguard` client, reads with the byte cap and
   dry-run probes, and load-job writes.
4. Add the emulator service and CI job, and the opt-in live test.
5. Document the connection's settings, the cost defaults, the load-job
   quota, and what is refused.
