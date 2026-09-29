# Reading from Oracle

Database nodes can read from Oracle through an `oracle` connection. The
driver is [go-ora](https://github.com/sijms/go-ora), a pure-Go
implementation of Oracle's network protocol. It needs no Oracle Instant
Client and no CGO, so the single static binary keeps working on every
release target.

**Oracle is read-only in this build.** `source_db`, `migrate` sources and
**Test connection** work against it. Writes (`sink_db`, or an Oracle
`migrate` destination) are refused by name, in every mode. See
[Why writes are refused](#why-writes-are-refused).

- [Connection configuration](#connection-configuration)
- [Driver options](#driver-options)
- [Python](#python)
- [TypeScript](#typescript)
- [How values come back](#how-values-come-back)
- [Column names](#column-names)
- [Large results](#large-results)
- [Why writes are refused](#why-writes-are-refused)
- [Testing the connection](#testing-the-connection)
- [Security](#security)
- [Testing against Oracle Database Free](#testing-against-oracle-database-free)

## Connection configuration

Create a connection of type **Oracle**:

| Field | Required | Meaning |
| --- | --- | --- |
| `host` | yes | The listener's host name or address. |
| `port` | no | The listener's port. `0` or empty means `1521`. |
| `schema` | yes, unless `sid` is set | The **service name**, such as `ORCLPDB1` or `FREEPDB1`. For a pluggable database, use the PDB's service, not the container's. |
| `login` | yes | The database user. |
| `password` | yes | The user's password. It can be a [secret reference](secret-references.md). |
| `extra` | no | A JSON object of [driver options](#driver-options). |

The connection becomes this URI, which is what the driver receives:

```text
oracle://etl_reader:<password>@db.example.com:1521/ORCLPDB1
```

The login and password are URL-escaped when the URI is built, so a password
containing `@`, `/`, `:` or `#` works as typed. The URI is never shown by
the API, and the password is removed from any error the driver reports
(see [Security](#security)).

## Driver options

`extra` accepts these go-ora options, and only these. Anything else is
dropped, so an `extra` value cannot redirect the connection to another host,
user or service.

| Key | Example | Meaning |
| --- | --- | --- |
| `sid` | `"ORCL"` | Connect by SID instead of service name, for an old non-CDB database. |
| `instance name` | `"orcl2"` | Connect to one named instance of a RAC service. |
| `ssl` | `true` | Use TCPS. The listener must have a TCPS endpoint, usually on port 2484. |
| `ssl verify` | `true` | Verify the server's certificate. Leave it on; turn it off only for a test server with a self-signed certificate. |
| `connect timeout` | `30` | Seconds to wait for the listener. |
| `encryption` | `"required"` | Oracle native network encryption: `required`, `requested`, `accepted` or `rejected`, matching the server's `SQLNET.ENCRYPTION_SERVER`. |
| `data integrity` | `"required"` | Native network checksums, with the same values as `encryption`. |

For a server that requires TLS:

```json
{"ssl": true, "ssl verify": true}
```

**Oracle wallets are not supported.** A go-ora wallet is a directory on the
worker. A connection that could name one could point at another
workspace's wallet on a shared worker, so the option is not accepted. Use
TCPS with a certificate signed by a CA the worker trusts, or native network
encryption.

## Python

Pass the connection ID as `conn_id`:

```python
from brokoli import Pipeline, sink_file, source_db

with Pipeline("Oracle export") as pipeline:
    orders = source_db(
        "Read orders",
        query="SELECT order_id, customer_id, amount, created_at FROM sales.orders",
        conn_id="erp-oracle",
    )
    sink_file(
        "Write orders",
        input=orders,
        path="exports/orders.parquet",
        format="parquet",
    )
```

## TypeScript

The TypeScript option is `connId`:

```typescript
import { Pipeline } from "brokoli";

const pipeline = new Pipeline("Oracle export");
const orders = pipeline.sourceDb("Read orders", {
  query: "SELECT order_id, customer_id, amount, created_at FROM sales.orders",
  connId: "erp-oracle",
});
pipeline.sinkFile("Write orders", orders, {
  path: "exports/orders.parquet",
  format: "parquet",
});
```

The query is Oracle SQL, sent as written: `FETCH FIRST n ROWS ONLY` rather
than `LIMIT`, `FROM dual` for a query with no table.

## How values come back

Oracle stores every integer as `NUMBER`, and go-ora returns every `NUMBER`
as its decimal text. Brokoli decodes that text into the value it
represents, and never rounds:

| Oracle value | Arrives as | Example |
| --- | --- | --- |
| A whole `NUMBER` in the 64-bit range | 64-bit integer, exact | `9223372036854775807` |
| A `NUMBER` with at most 15 significant digits | double | `1.5`, `0.1` |
| A `NUMBER` with more digits than a double keeps | text, every digit | `12345678901234567890123` |
| `BINARY_DOUBLE` | double | `2.5` |
| `BINARY_FLOAT` | single-precision float | `1.25` |
| `VARCHAR2`, `CHAR`, `NVARCHAR2`, `CLOB` | text | |
| `DATE`, `TIMESTAMP`, `TIMESTAMP WITH TIME ZONE` | timestamp | |
| `NULL` | null | |

The 15-digit rule is what makes the double exact: any decimal with 15
significant digits survives the round trip through a double unchanged. A
16-digit value might not, so it stays text rather than change silently. To
get a number anyway, round it in the query (`ROUND(amount, 2)`) or cast it to
`BINARY_DOUBLE` and accept the rounding.

**Empty strings are nulls.** Oracle stores `''` as `NULL`, in
`VARCHAR2` and `CHAR` alike, so an empty value read from Oracle arrives as
null. Brokoli passes it through as it arrives, and does not guess which
nulls were empty strings. A transform comparing a column to `""` will not
match those rows. Test for null instead, or use `NVL(column, 'x')` in the
query. A test pins this.

An Oracle `DATE` has a time part. It arrives as a timestamp, midnight when
the time part is zero.

## Column names

Oracle folds unquoted names to upper case, so
`SELECT order_id FROM orders` produces a column named `ORDER_ID`. Downstream
nodes, transforms and file headers see that name. To keep lower case, quote
the alias: `SELECT order_id AS "order_id" FROM orders`.

## Large results

`source_db` reads through the same paths as every other database. Results
that fit the worker's memory budget are read whole. The streaming path reads
Oracle in batches and holds one batch at a time, with the same value
decoding. The integration tests read a 2,500-row result in 1,000-row batches
and check every value.

A `migrate` node can read from Oracle and write to a backend that accepts
writes, for example Oracle into PostgreSQL. Destination columns are matched
by name, so alias the source columns to the destination's names. For a
lower-case PostgreSQL table, quote them, as in
`SELECT order_id AS "order_id" FROM sales.orders`. Otherwise the load
fails with `column "ORDER_ID" ... does not exist`.

## Why writes are refused

Brokoli's shared statement writer generates SQL that Oracle rejects or
misreads:

- **Multi-row inserts.** `INSERT ... VALUES (...), (...)` only exists from
  Oracle 23ai. Earlier versions reject every batch.
- **Types.** `CREATE TABLE` would use `TEXT` and `BOOLEAN`. Oracle has no
  `TEXT`, and no `BOOLEAN` column type before 23ai.
- **Overwrite.** `TRUNCATE` is DDL in Oracle and commits immediately. A load
  that failed after it would leave the table empty, with nothing to roll
  back.
- **Names.** The writer quotes identifiers. A quoted lower-case name in
  Oracle is a different table from the upper-case one an Oracle user created
  without quotes.

Rather than write something wrong, every write is refused by name, whatever
the mode (`append`, `overwrite`, `upsert`, or `create_table`):

- a `sink_db` with an explicit `oracle://` URI fails pipeline validation, so
  the editor shows it;
- a `sink_db` or `migrate` destination that reaches Oracle through a
  `conn_id` fails when the node runs, before anything connects.

The message is `Oracle connections are read-only in this build`. Writing
to Oracle needs its own statement vocabulary, tested against a server, the
way the SQL Server and ClickHouse writers were.

## Testing the connection

**Test connection** opens the same driver, with the same URI, that a run
uses, and pings the server. That covers the listener, the service name, TLS,
and the credentials. A failure is reported without detail in the UI. The
reason goes to the server log, with the password removed.

## Security

- **Credentials** are stored like every other connection's and can be
  secret references. They are URL-escaped into the URI, never interpolated
  into SQL.
- **Errors.** go-ora quotes the whole connection string when it cannot
  parse it, for example `parse "oracle://user:secret@bad host:1521/x"`. The
  password is removed, in both its escaped and plain forms, from every open
  and ping error before it reaches a run log or the server log.
- **Driver options** are an allowlist. Wallets are excluded, because they
  would let a connection name a directory on the worker.
- **Least privilege.** A read-only pipeline needs `CREATE SESSION` and
  `SELECT` on what it reads, and nothing more.

## Testing against Oracle Database Free

The tests run against Oracle Database Free, using the
[gvenzl/oracle-free](https://github.com/gvenzl/oci-oracle-free) image pinned
by digest in `docker-compose.test.yml`. The image is about 2 GB, so the
service sits behind a compose profile and a plain `up -d` does not pull it:

```sh
docker compose -f docker-compose.test.yml up -d --wait oracle
export BROKOLI_TEST_ORACLE_URL='oracle://brokoli:p%40ss%2Fw%3Ard%231@127.0.0.1:55544/FREEPDB1'
go test ./engine -run '^TestOracle' -v
go test ./api -run '^TestOracleConnectionTest' -v
```

The application user's password is `p@ss/w:rd#1` on purpose, to prove that
URL syntax in a credential survives the trip to the driver. CI runs the same
tests in the `Test (Oracle / oracle-free)` job.

Against the server, the tests cover:

- the value decoding in the table above, on both the whole-result and the
  streaming path, including a multi-batch stream;
- the column probe;
- a saved connection read through a real run, keeping a 64-bit value exact;
- **Test connection** with the right and a wrong password.

Without a server, they cover:

- that the driver is compiled in;
- that connection errors carry no password;
- the decoding rules;
- that every write mode is refused, at validation and in a run.
