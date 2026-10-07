# Native ADBC drivers

A `source_db` node can read through a native [ADBC](https://arrow.apache.org/adbc/)
driver: a shared library, installed separately from Brokoli, that returns
query results as Apache Arrow. Brokoli loads it only inside a short-lived,
isolated worker process, one per query, so a faulty driver can fail a node
but cannot take the server down.

- [What is supported](#what-is-supported)
- [Builds that can run native drivers](#builds-that-can-run-native-drivers)
- [Installing a driver](#installing-a-driver)
- [The driver catalog](#the-driver-catalog)
- [Pinning a connection to a driver](#pinning-a-connection-to-a-driver)
- [Flight SQL connections](#flight-sql-connections)
- [How a run reads through a driver](#how-a-run-reads-through-a-driver)
- [Testing a connection](#testing-a-connection)
- [Errors](#errors)
- [Security](#security)
- [Workers on other machines](#workers-on-other-machines)
- [Environment variables](#environment-variables)
- [API](#api)

## What is supported

| Connection type | Driver (catalog name) | Reads through a native driver |
| --- | --- | --- |
| **Flight SQL** (`flightsql`) | `flightsql` | Always. Flight SQL has no other path, so a Flight SQL connection must be pinned to a driver build. |
| **PostgreSQL** (`postgres`) | `postgresql` | Only when the connection is pinned to a driver build. Unpinned, it keeps the built-in Go driver. |
| **SQLite** (`sqlite`) | `sqlite` | Only when pinned, as for PostgreSQL. |
| **MySQL** (`mysql`) | `mysql` | Only when pinned. See [MySQL connections](#mysql-connections). |
| **ClickHouse** (`clickhouse`) | `clickhouse` | Only when pinned, and over ClickHouse's HTTP interface. See [ClickHouse connections](#clickhouse-connections). |

A driver serves only the connection types in this table. The catalog lists
others (Trino, DuckDB, Snowflake, Databricks, BigQuery): they install and
load, but no connection can be pinned to them yet. The Drivers page and both
driver APIs say so (`usable_by`), and saving a connection pinned to a driver
that does not serve its type is refused.

| Operation | Status |
| --- | --- |
| `source_db` query | Supported, on the streaming path and the batch path |
| Dry run (preview) | Supported; the query is stopped once the sample has arrived |
| **Test connection** | Supported: runs `SELECT 1` through the pinned driver, in the same isolated worker a run uses |
| Run parameters (`${param.x}`) in the query | Refused with an error; see [below](#run-parameters) |
| `sink_db`, `migrate`, and other nodes | A pinned PostgreSQL, SQLite, MySQL or ClickHouse connection uses its built-in Go driver there. A Flight SQL connection is refused by any node but `source_db`. |

## Builds that can run native drivers

Loading an ADBC driver needs cgo. The standard Brokoli release binaries are
built without it and **cannot run native drivers**. They can still list,
install and remove drivers, and save connections pinned to one, so a server
can manage drivers for workers that run them. A native source on such a
server fails with:

```text
this Brokoli build cannot load native ADBC drivers; run it on a worker built with native driver support
```

To build a binary that can, compile with cgo and the `adbc` tag:

```sh
CGO_ENABLED=1 go build -tags adbc -o brokoli .
```

`GET /api/capabilities` reports which kind of build answered:

```json
"native_adbc_drivers": { "status": "native_worker_enabled", "reason": "..." }
```

`status` is `native_worker_unavailable` on a build without support.

## Installing a driver

Drivers are installed into the driver directory: `BROKOLI_DRIVER_DIR`, or
`$XDG_DATA_HOME/brokoli/drivers`, or `~/.brokoli/drivers`. Every build lives
in its own directory, named by its identity:

```text
<driver dir>/<name>/<version>/<library sha256>/
    manifest.json
    lib/libadbc_driver_flightsql.so
```

Several versions or builds of one driver can be installed side by side; a
connection names exactly one. A build sitting at a path that does not match
its own manifest is ignored.

A driver archive is a `.tar.gz` holding a `manifest.json` and the library it
names:

```json
{
  "name": "flightsql",
  "version": "1.12.0",
  "os": "linux",
  "arch": "amd64",
  "library": "lib/libadbc_driver_flightsql.so",
  "entrypoint": "AdbcDriverFlightSQLInit",
  "library_sha256": "2c9ead4e46ce85223d152bd1b9aae33823c1de5bd20a5b2a888b4b75024d2d4e",
  "archive_sha256": "0000000000000000000000000000000000000000000000000000000000000000"
}
```

| Field | Rule |
| --- | --- |
| `name` | Lowercase letters, digits, `_` and `-`, starting with a letter; at most 64 characters. |
| `version` | Letters, digits, `.`, `_`, `+` and `-`, starting with a letter or digit. It becomes a directory name, so nothing path-like is accepted. |
| `os`, `arch` | Must match the machine (`runtime.GOOS`/`GOARCH`). |
| `library` | A clean relative path inside the archive. |
| `entrypoint` | The driver's ADBC init function. |
| `library_sha256` | SHA-256 of the library. Checked on install, on every scan, and again immediately before the library is loaded. |

Unknown manifest fields are refused: a manifest describes native code about
to be loaded, and a field this build does not understand might be one it
should have obeyed.

**From the command line**, with an archive and its digest:

```sh
brokoli drivers install flightsql-1.12.0-linux-amd64.tar.gz --sha256 <archive sha256>
```

or from the [catalog](#the-driver-catalog):

```sh
brokoli drivers install flightsql --catalog            # newest installable release
brokoli drivers install flightsql --catalog --version 1.12.0
brokoli drivers list
brokoli drivers inspect flightsql
brokoli drivers remove flightsql
```

`brokoli drivers remove` cannot see the server's connections, so it does not
check which are pinned to the driver; removal through the server does.

**From the server**, on the Drivers page or through the [API](#api).
Installing and removing take the `drivers.manage` permission, which only the
built-in admin role holds: a driver is native code that every worker on the
host loads, for every workspace, so it belongs to whoever operates the
deployment, not to a workspace.

A running server sees an install made through its own API at once, and one
made by another process (the CLI) within 30 seconds.

## The driver catalog

A catalog is a JSON index of driver releases that the server and the CLI
install from by name. **There is no default catalog.** An operator sets
`BROKOLI_DRIVER_INDEX` to its URL. The index decides which native code a
one-click install loads, and it is not signed yet, so trusting one is a
decision for the operator, not the binary.

```json
{
  "version": 1,
  "drivers": [
    {
      "name": "flightsql",
      "display_name": "Apache Arrow Flight SQL",
      "version": "1.12.0",
      "os": "linux",
      "arch": "amd64",
      "archive_url": "https://mirror.example.com/flightsql-1.12.0-linux-amd64.tar.gz",
      "sha256": "<archive sha256>",
      "docs_url": "https://mirror.example.com/flightsql/README.md",
      "lifecycle": "supported",
      "license": "Apache-2.0",
      "advisories": []
    }
  ]
}
```

- `sha256` is the digest of the **archive**. The archive is downloaded,
  checked against it, and its manifest must then name the same `name` and
  `version` as the entry, or the install is refused. The digest proves the
  bytes are the ones listed; the name check proves they were listed under
  the right name.
- `archive_url` may be `http://` (useful for a mirror inside an air-gapped
  network), because the archive is verified by digest. `docs_url` and
  `icon_url` must be `https://`: they are shown to people and verified by
  nothing.
- `lifecycle` is `supported`, `deprecated` or `revoked`. A revoked release
  is still listed, so an operator can see what pinned connections use, but
  cannot be installed.
- Unknown fields are ignored, so a catalog can add fields without breaking
  older servers. A change older servers must not misread raises `version`.
- Browsing the catalog and its documentation uses a copy at most five
  minutes old. An install always reads it fresh, so a release revoked
  minutes ago is not installed from a cached copy.

## Pinning a connection to a driver

A connection names the build it reads through by its identity:

```json
{
  "conn_id": "warehouse_flight",
  "type": "flightsql",
  "host": "flight.example.com",
  "port": 32010,
  "login": "loader",
  "password_ref": "secret://vault-prod/warehouse/loader#password",
  "driver_identity": {
    "name": "flightsql",
    "version": "1.12.0",
    "library_sha256": "2c9ead4e46ce85223d152bd1b9aae33823c1de5bd20a5b2a888b4b75024d2d4e"
  }
}
```

`library_sha256` is the installed **library's** digest, as `GET /api/drivers`
lists it, not the catalog's archive digest. A connection is pinned to exactly
one build: installing a newer version does not move it, so a pipeline's
execution environment never changes without someone changing the
connection.

When a connection is saved:

- a Flight SQL connection without `driver_identity` is refused;
- an identity that is incomplete or malformed is refused;
- `driver_identity` on a type no driver serves is refused, and so is a pin
  to a driver that does not serve the connection's type (a MySQL connection
  pinned to the `postgresql` driver, say);
- a ClickHouse connection pinned on its native TCP port (9000, 9440) is
  refused: the native driver uses the HTTP interface.

Whether the build is installed is **not** checked on save, because the
server saving a connection need not be a worker that runs it. The worker
that runs it checks.

On update, **leaving `driver_identity` out keeps the current pin**, so a
client that predates the field, or a script that sends only what it changes,
cannot unpin a connection by accident. Send `"driver_identity": null` to
unpin.

Removing a build that connections are pinned to is refused with `409`:

```json
{
  "error": "connections are pinned to this driver release",
  "conns": ["warehouse_flight"],
  "other_workspace_count": 2,
  "detail": "Repoint or delete these connections before removing the driver release."
}
```

Only connections in your own workspace are named; the rest are counted.

## Flight SQL connections

| Field | Meaning |
| --- | --- |
| Host | The Flight SQL server. |
| Port | Default `32010`. |
| Schema | Optional; becomes the URI path. |
| Login, Password | With both, sent as `authorization: Basic ...`; with only a password, as `authorization: Bearer <password>`. The password can be a [secret reference](secret-references.md). |

The URI is `grpc+tcp://<host>:<port>[/<schema>]`.

Extra settings:

```json
{
  "headers": { "x-tenant": "analytics" },
  "adbc_options": { "adbc.flight.sql.client_option.with_max_msg_size": "67108864" }
}
```

- `headers` are sent on every call, as `adbc.flight.sql.rpc.call_header.<name>`.
- `adbc_options` are passed to the driver as given. `driver`, `entrypoint`
  and `uri` are set by Brokoli from the pinned build and the connection, and
  are refused as options.

For a pinned PostgreSQL or SQLite connection, the URI is the one the
connection always builds, and `adbc_options` in its extra settings are passed
to the driver the same way.

## MySQL connections

The `mysql` driver is the ADBC Driver Foundry's MySQL driver; it also serves
MariaDB. A pinned MySQL connection is given to it as:

| Driver input | From the connection |
| --- | --- |
| `uri` | `mysql://<host>:<port>/<schema>?<driver options>`; port defaults to 3306 |
| `username`, `password` options | Login and password. Never in the URI. |
| URI query parameters | The connection's MySQL driver options from its extra settings (`tls`, `charset`, `collation`, `parseTime`, `loc`, `timeout`, `readTimeout`, `writeTimeout`, `interpolateParams`, `maxAllowedPacket`, `clientFoundRows`), which the driver hands to the Go MySQL driver unchanged |

`DECIMAL` columns arrive exactly, as decimal strings, whatever width the
driver chooses for them.

## ClickHouse connections

The `clickhouse` driver is ClickHouse's own ADBC driver. It talks to the
**HTTP interface**, not the native TCP protocol the built-in driver uses, so
a pinned ClickHouse connection's port must be the HTTP port:

| Driver input | From the connection |
| --- | --- |
| `uri` | `http://<host>:<port>/` (port defaults to 8123), or with `"secure": true` in the extra settings `https://<host>:<port>/` (port defaults to 8443) |
| `?database=` | The connection's schema |
| `username`, `password` options | Login and password. Never in the URI. |

Saving a pinned ClickHouse connection on port 9000 or 9440 (the native
protocol) is refused with that reason. The built-in driver's options
`dial_timeout`, `read_timeout` and `compress` are not passed: the native
driver would send them to the server as ClickHouse settings.

## How a run reads through a driver

For each `source_db` node whose connection reads through a native driver:

1. The node's stored connection is looked up by `conn_id`. Nothing else in
   the node's configuration decides whether a native driver is used or which
   one: a pipeline author can write any key into a node, and none of them
   selects native code.
2. The pinned build is checked to be installed on the worker, and, for Flight
   SQL, the host is checked against the [outbound policy](#security). Only
   then are credentials resolved.
3. A fresh `brokoli worker task` child is started. It receives the query and
   the pinned identity on stdin, looks the identity up in its own driver
   directory, hashes the library, loads it, and runs the query.
4. The child streams the result as Arrow IPC to the parent, which writes it
   straight into the run's artifact store (streaming path) or decodes it into
   rows (batch path, dry run). There is no temporary file.

### Run parameters

Run parameters bind as driver placeholders, and each ADBC driver has its own
placeholder syntax and binds parameters through Arrow records. Until that is
implemented, a query containing `${param.x}` is refused with an error rather
than sent to the database with the literal text in it.

## Testing a connection

**Test** on a connection that reads through a native driver runs `SELECT 1`
through the pinned build, in an isolated worker, exactly as a run would. On a
server whose build cannot load native drivers, the button is disabled and the
API answers with the [build error](#builds-that-can-run-native-drivers).

## Errors

A failed query names the step it failed at, carries the driver's own message,
and the end of what the driver printed:

```text
native ADBC execute query failed: I/O: [FlightSQL] connection error: desc = "transport: Error while dialing: dial tcp 10.0.4.7:32010: connect: connection refused" (Unavailable; ExecuteQuery)
```

| Stage | Meaning |
| --- | --- |
| `read request` | The worker could not read its request or apply its resource limits. |
| `load driver` | The pinned build is not installed on this worker, its library no longer matches its digest, or this build cannot load drivers. |
| `open database` | The driver refused the options (a bad `adbc_options` value, for example). |
| `connect` | The driver could not open a connection. |
| `execute query` | The server refused the query, or could not be reached (Flight SQL connects lazily, at the first call). |
| `read results` / `write results` | The result stream failed partway. |

If the worker dies without reporting (a crash in the driver), the error says
it exited unexpectedly and how, with the end of its output.

Every message is scrubbed of the request's credentials before it is recorded:
option values, the password, and each part of a value such as
`Basic <base64>`, so a driver echoing its authorization header does not put
the token in the run log.

## Security

- **The driver is chosen only from the stored connection** and named by its
  identity. The worker resolves the identity in its own driver directory and
  verifies the library's digest immediately before loading it.
- **The worker does not inherit the server's environment.** It gets `PATH`,
  `HOME`, `LANG`, `TZ`, `TMPDIR`, `LC_*`, `SSL_CERT_FILE`, `SSL_CERT_DIR` and
  `KRB5_CONFIG`, plus whatever `BROKOLI_NATIVE_PASS_ENV` lists
  (comma-separated, or `*` for everything). The server's encryption key,
  database URL and cloud credentials are not passed to driver code.
- **Resource limits** are applied by the worker to itself before any driver
  loads: `BROKOLI_NATIVE_MEMORY_MB` (address space), `BROKOLI_NATIVE_CPU_SECONDS`
  and `BROKOLI_NATIVE_OPEN_FILES` (default 1024). Soft and hard limits are
  both set, so loaded code cannot raise them.
- **Outbound policy.** A Flight SQL driver does its own networking, outside
  the dial-time guard Brokoli's own HTTP clients use. Before a Flight SQL
  host is handed to it, every address the host resolves to is checked against
  Brokoli's outbound policy: private, loopback and link-local
  addresses and cloud metadata endpoints are refused unless allowed with
  `BROKOLI_OUTBOUND_ALLOW_CIDRS`, `BROKOLI_OUTBOUND_ALLOW_PRIVATE` or
  `BROKOLI_OUTBOUND_ALLOW_LOOPBACK`. The driver resolves the name again when
  it connects, so a DNS answer that changes in between is not caught. Pinned
  PostgreSQL, SQLite, MySQL and ClickHouse connections keep the policy their built-in drivers
  have.
- **Installed builds are not public.** `GET /api/capabilities` is readable
  without signing in and says only whether native drivers can run; the
  installed builds and their versions are listed by `GET /api/drivers`, for
  signed-in users.
- **Installing and removing** take `drivers.manage`.

## Workers on other machines

A run whose pipeline reads through native drivers carries the capability tags
of each pinned build:

```text
native-adbc
native-adbc:flightsql
native-adbc:flightsql:1.12.0:2c9ead4e46ce
```

The tags are derived from the identity alone, so a scheduler with no drivers
installed can still route the run. A worker advertises the tags of the builds
installed in its own driver directory and, on a queue that routes by
capability, is offered only runs it can execute. A worker handed a run it
lacks a build for, by a queue that cannot route, returns it to the queue and
waits two seconds before asking for another.

## Environment variables

| Variable | Default | Meaning |
| --- | --- | --- |
| `BROKOLI_DRIVER_DIR` | `$XDG_DATA_HOME/brokoli/drivers`, else `~/.brokoli/drivers` | Where driver builds are installed and looked up. |
| `BROKOLI_DRIVER_INDEX` | none | URL of the driver catalog. Without it, catalog installs are unavailable. |
| `BROKOLI_NATIVE_PASS_ENV` | none | Further environment variables the native worker may see; `*` for all. |
| `BROKOLI_NATIVE_MEMORY_MB` | unlimited | Address-space limit of each native worker. |
| `BROKOLI_NATIVE_CPU_SECONDS` | unlimited | CPU-time limit of each native worker. |
| `BROKOLI_NATIVE_OPEN_FILES` | 1024 | Open-file limit of each native worker. |

## API

| Method and path | Permission | Does |
| --- | --- | --- |
| `GET /api/drivers` | signed in | The builds installed on this server, with each `library_sha256` and `usable_by` (the connection types that can use it), and whether this build can run them (`native_worker_enabled`). |
| `GET /api/drivers/catalog` | signed in | The catalog's releases for this platform, marked `installed` and with `usable_by`, plus installed builds the catalog does not list. `configured` is false without `BROKOLI_DRIVER_INDEX`. |
| `GET /api/drivers/catalog/{name}/{version}/docs` | signed in | The release's documentation, as Markdown. |
| `POST /api/drivers/catalog/{name}/install[?version=]` | `drivers.manage` | Installs a catalog release: `201` with the installed build, `404` if the catalog has no such release for this platform, `409` if no catalog is configured, `502` if the catalog or archive could not be fetched or did not match its digest, `422` if the archive is invalid. |
| `DELETE /api/drivers/{name}[?version=]` | `drivers.manage` | Removes every build of the driver, or of one version: `204`, `404` if not installed, `409` if connections are pinned to it. |
