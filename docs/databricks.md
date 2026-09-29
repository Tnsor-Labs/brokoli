# Reading from Databricks SQL warehouses

A `databricks` connection lets `source_db` (and the source side of `migrate`)
run queries on a Databricks SQL warehouse. It is **read only** in this build:
every write is refused by name, before anything connects. See
[Why writes are refused](#why-writes-are-refused).

- [Connection configuration](#connection-configuration)
- [Driver options](#driver-options)
- [Authentication](#authentication)
- [Python](#python)
- [TypeScript](#typescript)
- [Reads](#reads)
- [Why writes are refused](#why-writes-are-refused)
- [Testing the connection](#testing-the-connection)
- [Network policy](#network-policy)
- [Security](#security)
- [Build and binary size](#build-and-binary-size)
- [Testing](#testing)

## Connection configuration

Create a connection of type **Databricks** with these fields:

| Field | Required | Meaning |
| --- | --- | --- |
| Host | yes | The workspace hostname only, such as `adb-1234567890123456.7.azuredatabricks.net` or `acme.cloud.databricks.com`. No `https://`, no port, no path: each is refused with a message saying so. |
| Port | no | Defaults to `443`. |
| Schema | yes | The warehouse's **HTTP path**, from the warehouse's *Connection details* tab: `/sql/1.0/warehouses/<id>`. A cluster path (`/sql/protocolv1/o/<workspace-id>/<cluster-id>`) is accepted too. The leading `/` may be omitted. |
| Password | yes | A personal access token (`dapi...`). Stored encrypted, or as a [secret reference](secret-references.md). |
| Extra | no | [Driver options](#driver-options), as a JSON object. |

The connection's `login` is not used: the driver authenticates with the
token alone.

Brokoli builds a URI from these fields,
`databricks://token:<token>@<host>:<port>/<http-path>?<options>`, which is
what a `source_db` node configured with an explicit `uri` takes as well.
Prefer `conn_id`: the URI carries the token.

## Driver options

Extra is a JSON object; only these keys are accepted, and any other key is
refused by name rather than passed to the driver:

```json
{
  "catalog": "main",
  "schema": "sales",
  "maxRows": 100000,
  "timeout": 600
}
```

| Key | Meaning |
| --- | --- |
| `catalog` | Initial Unity Catalog catalog for the session, so queries can name tables without it. |
| `schema` | Initial schema (database) within that catalog. |
| `maxRows` | Rows fetched per round trip from the warehouse, a positive number. The driver's default is 100,000. |
| `timeout` | Server-side query timeout, in seconds. `0` or absent means none. |
| `userAgentEntry` | Text appended to the driver's user agent, visible in the warehouse's query history. |
| `useCloudFetch` | `true` (default) lets large results download in parallel from cloud storage; `false` keeps every row on the Thrift connection. See [Network policy](#network-policy). |
| `maxDownloadThreads` | Parallel Cloud Fetch downloads, a positive number. Default 10. |
| `useArrowNativeDecimal` | The driver's setting of the same name: `true` asks for `DECIMAL` columns as native Arrow decimals. |

Refused on purpose: the driver's own authentication parameters (`authType`,
OAuth client settings) and arbitrary session parameters. The driver would
otherwise turn an unknown parameter into a session setting, and one of its
authentication modes opens a browser, which on a server waits forever.

## Authentication

Personal access tokens only. Use a token that belongs to a service principal
where your workspace allows it, rather than a person's, and grant it only
what the pipelines read:

- `CAN USE` on the SQL warehouse;
- `USE CATALOG` and `USE SCHEMA` on the catalog and schema;
- `SELECT` on the tables or views queried.

OAuth machine-to-machine and workload identity federation are not supported
yet.

## Python

```python
from brokoli import Pipeline, sink_file, source_db

with Pipeline("Databricks export") as pipeline:
    orders = source_db(
        "Read orders",
        conn_id="lakehouse",
        query="SELECT order_id, region, amount FROM main.sales.orders WHERE day = current_date()",
    )
    sink_file("Write orders", input=orders, path="exports/orders.csv", format="csv")
```

## TypeScript

```typescript
import { Pipeline } from "brokoli";

const pipeline = new Pipeline("Databricks export");
const orders = pipeline.sourceDb("Read orders", {
  connId: "lakehouse",
  query: "SELECT order_id, region, amount FROM main.sales.orders WHERE day = current_date()",
});
pipeline.sinkFile("Write orders", orders, { path: "exports/orders.csv", format: "csv" });
```

## Reads

- **Queries are Databricks SQL**, sent as written. Name tables with backticks
  where a name needs quoting (`` `main`.`sales`.`order-lines` ``); in Spark
  SQL a double-quoted name is a string.
- **Batch and streamed reads both work.** Where the run streams `source_db`
  output (the default whenever spilling is enabled), rows go to the blob
  store in batches as the driver returns them, so a large result does not
  have to fit in the worker's memory. A dry run reads in memory, as for every
  database.
- **Timeouts:** a batch read is bounded at five minutes by Brokoli; set
  `timeout` in Extra to have the warehouse enforce its own limit as well. A
  stopped warehouse is started by the first query, which can take minutes.
- **No pushdown.** Transforms after a Databricks source run in the engine,
  never inside the warehouse; Databricks does not claim the capability that
  lets a segment run in the database.
- **Column types** are whatever the driver returns for each column. They have
  not been verified against a live warehouse by Brokoli's test suite; check a
  sample of a new query's output, in particular `DECIMAL`, `TIMESTAMP` and
  nested types, before relying on it.

## Why writes are refused

`sink_db` into a Databricks connection, and `migrate` with a Databricks
destination, fail with:

```text
Databricks connections are read-only in this build: writes (append, overwrite,
upsert, create table, SQL statements) are not supported ...
```

The refusal happens at validation when the node carries an explicit
`databricks://` URI, and at run time before any connection is opened in
every case, including SQL generated by a `sql_generate` node.

The reason is the promise every other database write keeps: a failed write
leaves nothing behind. Brokoli's statement path runs an overwrite's delete,
an optional `CREATE TABLE` and the inserts in one transaction, and the
Databricks SQL driver has no transactions (`BeginTx` answers "not
implemented"). Without one, a write that failed half way would leave a
truncated table or a partial append. A Databricks write path would need its
own design -- a staging table and an atomic `INSERT OVERWRITE`/`MERGE`, or a
Delta-level mechanism -- and an equivalence corpus against a real warehouse,
as every other backend's writes have. Until then, read from Databricks and
write elsewhere.

## Testing the connection

**Test connection** opens the same driver, with the same options, that a run
uses, and runs `SELECT 1` on the warehouse. A stopped warehouse is started by
it. It fails, with the reason in the server log, when:

- the host, port or HTTP path is malformed (the message says which field and
  never repeats the URI);
- the token is missing, wrong or expired;
- the warehouse does not exist or the token cannot use it;
- the outbound policy refuses the workspace's address.

## Network policy

Every request goes through the deployment's outbound policy, as SFTP, S3,
Azure Blob and BigQuery do: the Thrift session to the warehouse, the driver's
own feature-flag and telemetry calls to the same workspace, and Cloud Fetch
downloads. Cloud Fetch downloads large results directly from the workspace's
cloud storage (S3, ADLS or GCS presigned URLs), so those hosts must be
reachable too.

Public workspaces need nothing extra. A workspace behind Private Link
resolves to private addresses, which the policy refuses by default; allow
exactly those ranges:

```sh
BROKOLI_OUTBOUND_ALLOW_CIDRS=10.20.0.0/16
```

If Cloud Fetch storage is not reachable, set `"useCloudFetch": false` so every
row comes over the warehouse connection.

The driver retries a failed request, a refused one included, several times
with backoff, so a blocked address surfaces after some seconds rather than
at once.

## Security

- The token lives only in the connection's encrypted password (or a secret
  reference). The URI Brokoli builds from it is parsed by Brokoli, not by the
  upstream driver, and a malformed one is refused with a message that carries
  no part of it. The upstream driver quotes its whole DSN in a parse error,
  which is how a host saved as `host:443` used to put the token into the
  server log and the run's error.
- The host is a bare hostname, the path must start with `/sql/`, and only
  the options above are accepted, so connection data cannot redirect the
  token or change how the driver authenticates.
- The driver may send usage telemetry to the workspace it connects to, when
  that workspace's feature flags enable it, as Databricks' own clients do.
  It goes through the outbound policy like every other request.

## Build and binary size

The connector uses the pure-Go Thrift backend of
`github.com/databricks/databricks-sql-go`. Its optional kernel backend needs
CGO and a build tag and is not compiled in; its native libraries appear in
`go.mod` only because Go lists every module a package can import under any
build tag.

Compiling the driver in adds about 8 MB to a `CGO_ENABLED=0` linux/amd64
server binary (88.1 MB to 96.3 MB). Part of that is another copy of Apache
Arrow: the driver is written against Arrow v12, which is linked beside the
v18 and v15 the server already carries.

## Testing

Unit tests run without a warehouse and cover:

- the URI parsing;
- the malformed-host cases, with a control showing the upstream driver
  leaks the token for each of them;
- that the driver dials only through the outbound policy (a loopback
  listener is never dialled under the default policy, and is under one that
  allows loopback);
- the write refusal on every path and at validation;
- Test connection routing to the compiled driver.

A live smoke test runs against a disposable warehouse when
`BROKOLI_TEST_DATABRICKS_URI` holds a full `databricks://` URI:

```sh
BROKOLI_TEST_DATABRICKS_URI='databricks://token:dapi...@acme.cloud.databricks.com:443/sql/1.0/warehouses/abc123' \
  go test ./engine -run '^TestDatabricksLive$' -v
```

CI does not run it; there is no shared warehouse.
