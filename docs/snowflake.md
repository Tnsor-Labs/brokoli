# Querying Snowflake

`source_db` can read from Snowflake through a `snowflake` connection, using
Snowflake's Go driver (`gosnowflake`). Snowflake is **query-only in this
build**: `sink_db` and `migrate` refuse to write to it, by name, until each
write mode has been proven against a real account.

- [What is supported](#what-is-supported)
- [Connection configuration](#connection-configuration)
- [Authentication](#authentication)
- [Python](#python)
- [TypeScript](#typescript)
- [Identifier case](#identifier-case)
- [Reads](#reads)
- [Writes are refused](#writes-are-refused)
- [PUT and GET are refused](#put-and-get-are-refused)
- [Testing the connection](#testing-the-connection)
- [Security](#security)
- [Building from source](#building-from-source)
- [Testing against a real account](#testing-against-a-real-account)

## What is supported

| Operation | Status |
| --- | --- |
| `source_db` query | Supported, on the batch path and the streaming path |
| **Test connection** | Supported: logs in and pings through the same driver a run uses |
| `sink_db` append, overwrite, upsert, `create_table` | Refused by name |
| `migrate` into Snowflake | Refused by name |
| `migrate` out of Snowflake (Snowflake as the source) | Supported, like any other source |
| `sql_generate` with dialect `snowflake` | Supported: renders statements with Snowflake's string escaping; running them is up to you |
| SQL pushdown | Not available: Snowflake has no pushdown registration, so transforms run in the engine |
| `PUT` / `GET` | Refused by name |

## Connection configuration

Create a connection of type **Snowflake**:

| Field | Meaning |
| --- | --- |
| Host | The account identifier (`myorg-myaccount`) or the account hostname (`myorg-myaccount.snowflakecomputing.com`). |
| Schema | `DATABASE/SCHEMA`, for example `ANALYTICS/RAW`. Either part may be left out; queries can always qualify names themselves. |
| Login | The Snowflake user. |
| Password | That user's password. Encrypted at rest; it can also be a [secret reference](secret-references.md). |
| Port | Ignored: Snowflake is always reached over HTTPS on 443. |

Driver options go in the connection's extra settings, as a JSON object:

```json
{
  "warehouse": "COMPUTE_WH",
  "role": "LOADER",
  "loginTimeout": "60",
  "application": "brokoli-orders"
}
```

| Key | Meaning |
| --- | --- |
| `warehouse` | The virtual warehouse queries run on. Without one, the user's default warehouse is used, and a user with none cannot run queries. |
| `role` | The role for the session. Without one, the user's default role is used. |
| `authenticator` | `snowflake`, or omitted. See [Authentication](#authentication). |
| `loginTimeout` | Seconds to keep retrying the login. |
| `application` | The client name Snowflake records in its query history and login history. |

The connection is turned into a URI of this shape, which is also what a
node's `uri` may hold instead of a `conn_id`:

```text
snowflake://USER:PASSWORD@ACCOUNT/DATABASE/SCHEMA?warehouse=COMPUTE_WH&role=LOADER
```

Those five options are the only query parameters accepted, whether they come
from a saved connection or a URI written into a node. The driver itself
understands many more (token files, private key files, client configuration
files, temporary directories, proxies, certificate revocation switches,
arbitrary session parameters). Several of them make the driver read or write
files on the worker, so any other parameter fails the node with an error that
names it. A password never appears in these errors.

## Authentication

Only **user and password** authentication (`authenticator` omitted or
`snowflake`) is supported. Every other authenticator is refused by name
before anything is sent:

| Authenticator | Why it is refused |
| --- | --- |
| `externalbrowser` | Opens a browser on the worker and waits for a person. |
| `username_password_mfa`, `oauth_authorization_code` | Also wait for a person. |
| `snowflake_jwt` (key pair) | Needs a private key, which the connection has no field for yet. |
| `programmatic_access_token`, `oauth`, `oauth_client_credentials`, `workload_identity` | Need a token, client credentials or a workload identity setup that this build does not wire in. |
| An Okta URL | Sends the password to a host the connection names, outside Snowflake. |

Snowflake is phasing out single-factor password sign-in for human users. Use
a user created for the pipeline, with a role limited to what it reads, and
check your account's authentication policy allows password sign-in for it.

## Python

Pass the connection ID as `conn_id`:

```python
from brokoli import Pipeline, sink_file, source_db

with Pipeline("Snowflake export") as pipeline:
    orders = source_db(
        "Read orders",
        conn_id="snowflake-analytics",
        query='SELECT order_id AS "order_id", amount AS "amount" FROM RAW.ORDERS',
    )
    sink_file("Write CSV", input=orders, path="exports/orders.csv", format="csv")
```

## TypeScript

The TypeScript option is `connId`:

```typescript
import { Pipeline } from "brokoli";

const pipeline = new Pipeline("Snowflake export");
const orders = pipeline.sourceDb("Read orders", {
  connId: "snowflake-analytics",
  query: 'SELECT order_id AS "order_id", amount AS "amount" FROM RAW.ORDERS',
});
pipeline.sinkFile("Write CSV", orders, {
  path: "exports/orders.csv",
  format: "csv",
});
```

## Identifier case

Snowflake folds unquoted identifiers to upper case, and reports result
columns the same way. `SELECT order_id FROM orders` produces a column named
`ORDER_ID`, so downstream nodes (a filter, a rename, a contract, a file's
header row) see `ORDER_ID`, not `order_id`.

To keep lower-case names, alias them with double quotes, as the examples
above do: `SELECT order_id AS "order_id"`. A double-quoted identifier is
case-sensitive in Snowflake: `"order_id"` and `ORDER_ID` are different
columns. This is also the main reason writes are refused for now: Brokoli's
statement generator quotes every identifier, so a sink writing a column
`id` would not match the `ID` column of a table created without quotes.

## Reads

`source_db` runs the query as written; Brokoli does not rewrite it. On the
batch path the result is held in memory under the worker's dataset budget,
and a result over the budget fails the node with a message saying so. When a
source's output is passed by reference, it streams in batches instead and is
bounded by the batch size rather than the whole result.

Queries run inside the connection's warehouse and role, and are billed there.
Brokoli applies no row or cost limit of its own beyond the memory budget;
put a `LIMIT` or a resource monitor on the warehouse if one is needed.

## Writes are refused

`sink_db` into a Snowflake connection fails with:

```text
sink_db: Snowflake connections are query-only in this build: writes (append, overwrite, upsert, create_table) are not supported yet
```

for every mode, and `migrate` into one fails the same way. A node whose
`uri` names Snowflake directly is refused when the pipeline is validated,
before any run. The reasons, each of which a live test has to settle before a
mode is enabled:

- **Identifier case**, above.
- **Overwrite** clears the table with `TRUNCATE`, which Snowflake commits on
  its own, outside the write's transaction: a failed load would leave the
  table empty.
- **Types and literals.** The generated column types and timestamp literals
  have not been checked against Snowflake's.

To load data into Snowflake today, write files with `sink_file` to a bucket
Snowflake can read (an S3, Google Cloud Storage or Azure Blob connection) and
load them with `COPY INTO` from an external stage, in a `source_db` query or
from Snowflake itself.

## PUT and GET are refused

Snowflake's `PUT` and `GET` copy files between the **client's** filesystem
and a stage, and the driver performs them on the client, which here is the
worker: `GET @stage file:///...` writes files on the worker, and
`PUT file:///... @stage` uploads any file the worker can read. Brokoli refuses
any statement that starts with `PUT` or `GET`, in any letter case and after
any leading comments (`/* */`, `--`, `//`), before the driver sees it:

```text
Snowflake PUT and GET are not supported: they copy files to and from the worker's filesystem. Load staged files with COPY INTO instead
```

This applies to every statement a Snowflake connection runs, whatever node
it comes from. Server-side commands that move data between stages and
tables, such as `COPY INTO`, are unaffected.

## Testing the connection

**Test connection** opens the connection through the same driver and the
same checks a run uses, logs in, and pings the session. A refused option or
authenticator, a wrong password, an unknown account or an unreachable host
all fail it; the reason is written to the server log, not shown in the
response.

## Security

- The password lives only in the connection's encrypted settings or behind a
  secret reference. It is never written into pipeline configuration by a
  saved connection, and never appears in an error message.
- Only the five options above reach the driver, so a connection or a node's
  URI cannot point the driver at files on the worker.
- `PUT` and `GET` are refused on every statement path.
- The driver's embedded native library ("minicore") is never loaded: release
  binaries are built without it, and Brokoli sets `SF_DISABLE_MINICORE=true`
  for its own process, so a binary built without the build tag does not load
  it either.
- Requests go to the account's Snowflake endpoint and, for large results, to
  the cloud storage URLs Snowflake hands back. They do not go through
  Brokoli's outbound network policy, the same as the other database drivers.

## Building from source

Build with the `minicore_disabled` tag, as releases are:

```sh
CGO_ENABLED=0 go build -tags minicore_disabled -o brokoli .
```

Without it the library is embedded in the binary (between about 25 KB and
460 KB more, depending on the platform) but still never loaded.

## Testing against a real account

There is no Snowflake emulator, and CI has no shared account, so the live
test is opt-in. Point it at a disposable database and schema:

```sh
BROKOLI_TEST_SNOWFLAKE_URI='snowflake://USER:PASSWORD@ACCOUNT/DB/SCHEMA?warehouse=WH' \
  go test ./engine -run 'TestSnowflakeLive' -v
```

Everything else -- the option and authenticator allowlists, password
redaction, the `PUT`/`GET` refusal on each statement path, the write
refusals and the string escaping -- is covered by tests that need no account
and run in CI.
