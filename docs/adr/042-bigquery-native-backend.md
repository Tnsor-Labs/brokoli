# ADR-042: Native BigQuery backend

**Status:** proposed
**Date:** 2026-09-27

## Context

The connection catalog already advertises BigQuery, but the engine has no
BigQuery driver. BigQuery is not a database/sql server: it exposes HTTPS APIs,
executes queries as jobs, authenticates with Google credentials, and charges
for bytes processed. A database/sql wrapper would hide those semantics while
forcing the engine to model a non-connection as a host, port, DSN, and SQL
dialect.

The release binary is a single pure-Go executable cross-compiled for Linux,
macOS, and Windows on amd64 and arm64. Credentials must follow the existing
connection secret-reference path and must never be embedded in a URI. The
engine's existing database backend abstraction also requires that unsupported
capabilities are explicit rather than silently routed through a generic SQL
path.

## Decision

Implement BigQuery as a native backend beside the `database/sql` backends,
using Google's official pure-Go Go client libraries. Route a BigQuery
connection by connection type before URI/SQL-driver detection; do not add a
third-party `database/sql` wrapper.

The backend will use a resolved `models.Connection` only at the boundary and
will translate it into a BigQuery-specific configuration. The configuration
will contain the Google project, optional dataset and location, optional
billing project, and authentication mode. `ExtraRef` will carry service-account
JSON when explicitly configured; otherwise the client will use Application
Default Credentials or workload identity. No credential value will be placed
in `BuildURI`, logs, persisted plans, or error messages.

The first implementation phase will expose these operations:

- **Read:** submit a parameterized query job and consume results in bounded
  batches. Query configuration must support a caller-supplied maximum bytes
  billed and a dry-run/metadata path for validation and cost estimation.
- **Append:** write batches through the BigQuery Storage Write API, using a
  committed stream and retry-safe offsets where supported by the API.
- **Overwrite:** write to a temporary staging table, then replace the target
  table through a BigQuery query job, with cleanup on success and failure.
- **Upsert:** remain refused in the first phase unless the caller supplies
  key columns and the backend can prove the generated staging-plus-`MERGE`
  operation is supported for the target schema.

Column discovery will not issue an unbounded query. It will use BigQuery job
metadata/dry-run and an explicit zero-row query shape, and it will preserve the
estimated bytes processed so callers can apply a cost policy before execution.

The backend will advertise only capabilities covered by contract and live
integration tests. It will not register a BigQuery `dbdialect` or claim SQL
pushdown, URI addressing, generic SQL DDL, or database/sql pooling.

## Consequences

### Positive

- BigQuery's jobs, credentials, billing, streaming, and retry semantics remain
  visible instead of being distorted into a SQL connection model.
- Service-account credentials use the existing external-secret and encryption
  controls and do not leak through connection URIs.
- The implementation remains compatible with the six pure-Go release targets.
- Cost-sensitive validation can reject or warn before a query scans data.
- Capability refusals are explicit and can be expanded only with tests.

### Negative

- The engine needs a native backend dispatch surface in addition to its
  database/sql path.
- Google client libraries increase dependency graph and binary size; the PR
  must record cross-target build and license-scan results.
- BigQuery table/schema conversion, job polling, retries, and temporary-table
  cleanup add backend-specific implementation and integration-test work.
- Append and overwrite have different atomicity and retry semantics from SQL
  transactions; those semantics must be documented to pipeline authors.

### Deferred

- Storage Read API plus Arrow delivery for large reads.
- Query pushdown planning and a BigQuery-specific dialect, if the planner can
  preserve cost and correctness guarantees.
- Fully atomic overwrite and upsert behavior across partitioned and clustered
  tables.
- Dataset/table discovery and UI fields beyond the existing connection form.
- Regional endpoint selection, reservations, and advanced job labels.

## Alternatives considered

- **`database/sql` wrapper** — rejected because it introduces a second
  translation layer, cannot represent BigQuery job/cost semantics cleanly, and
  would make native streaming and capability reporting misleading.
- **Generic REST calls** — rejected because the official client provides
  authentication, retries, job polling, and typed API models; maintaining
  those concerns locally would increase risk.
- **Credential JSON in the URI** — rejected because URIs are logged, persisted,
  and passed through generic connection code; secret references already solve
  storage and resolution.
- **Streaming inserts as the only write path** — rejected because they do not
  provide the desired staging/replace semantics for overwrite and complicate
  exactly-once retry behavior.

## Follow-ups

- Confirm the official client dependency is pure Go and run all six
  `CGO_ENABLED=0` cross-builds.
- Measure binary-size delta and run the repository license scan.
- Define the native backend interface and dispatch point without weakening
  ADR-024's capability/refusal rules.
- Add unit tests for connection-to-client configuration and credential
  redaction before adding live BigQuery integration tests.
- Implement read, append, overwrite, and capability gates as separate phases;
  do not advertise unsupported upsert or pushdown behavior.
