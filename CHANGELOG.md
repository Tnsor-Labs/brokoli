# Changelog

All notable changes to Brokoli are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project loosely follows [Semantic Versioning](https://semver.org/).

Every entry names its owner. With more than one contributor in the
codebase, "who shipped this" is part of the record, not something to
reconstruct from git archaeology.

## [Unreleased]

## [0.10.71] - 2026-08-25

MySQL becomes a backend on the same footing as Postgres. Two of these are
data-correctness fixes on the MySQL write path, and one is a behaviour change
that will fail pipelines which were previously succeeding — read the upsert
item before upgrading.

### Fixed

- **MySQL string literals did not escape backslashes** (#338) — @hc12r.
  Values are rendered into SQL text rather than bound as parameters, and the
  escaping doubled the quote character while doing nothing about backslash.
  MySQL treats a backslash as an escape inside a literal unless
  `NO_BACKSLASH_ESCAPES` is set, which is not the default.

  So an ordinary Windows path arrived altered:

  ```
  C:\Users\report.csv   ->   C:Usersreport.csv
  ```

  and a value ending in a backslash escaped the quote meant to close the
  literal, after which MySQL parsed the following text as SQL rather than
  data. Verified against a live MySQL 8.4 before and after the fix.

  Postgres was never affected: `standard_conforming_strings` makes a
  backslash an ordinary character there, which is why this survived — the
  escaping was correct for the backend it was written against.

  **Upgrading does not repair data already written; re-running the pipeline
  does.** The rows worth checking are text values containing backslashes.

- **MySQL passwords containing URI metacharacters could not authenticate**
  (#338) — @hc12r. The DSN was assembled with `net/url`, which
  percent-encodes userinfo, while go-sql-driver reads credentials verbatim.
  Any password containing `@ : / ? # %`, a space, or non-ASCII failed with
  `Access denied` and nothing pointing at the cause. Credentials are now
  carried verbatim; driver options keep their encoding, because those the
  driver does unescape.

- **A MySQL overwrite was not atomic** (#344) — @hc12r. `GenerateSQL`'s
  contract is that clear-then-insert runs as one transaction. On MySQL,
  `TRUNCATE TABLE` implicitly commits, so an overwrite with `truncate: true`
  destroyed the previous rows the moment the clear ran — a failure in any
  later statement rolled back nothing and left the table empty.

  The `truncate` option now degrades to a transactional `DELETE` on MySQL.
  It keeps its measured advantage on Postgres, where `TRUNCATE` rolls back.

  `create_table: true` has the same MySQL caveat and it is now documented
  rather than fixed: `CREATE TABLE` also commits implicitly, so a run that
  creates its target and then fails leaves the empty table behind.

- **A driverless connection failed with an error naming nothing** (#346) —
  @hc12r. A connection whose type has no compiled-in driver — Oracle is the
  live example, offered in the catalog — failed a run with a message naming
  neither the connection nor its type, because the sentence explaining it
  went to the server's stdout. Resolver warnings now reach the run's node
  log, where the pipeline author reads them.

### Changed

- **MySQL upsert now requires `key_columns`, and validates them** (#344) —
  @hc12r. **This will fail pipelines that previously succeeded, and that is
  the point.**

  MySQL's `ON DUPLICATE KEY UPDATE` merges on whichever unique index the
  inserted row collides with; the statement cannot name one. `key_columns`
  was therefore accepted and silently ignored — rows merged on whatever
  index the schema happened to have, which may not be the columns
  configured, rewriting rows nobody addressed.

  `key_columns` is now required for MySQL upsert, as it already was for
  Postgres and SQLite, and is checked against `information_schema` before
  the write: the named columns must be exactly a unique index of the target
  table, or the run fails naming the indexes the table actually has.

  If a table carries additional unique indexes beyond the one named, the run
  logs a warning listing them — a row can still collide there and MySQL
  offers no way to prevent it, so the check does not pretend otherwise.

  A pipeline that starts failing after this upgrade was merging on a key it
  did not configure. The fix is to name the real one.

- **MySQL upsert emits the row-alias form, requiring MySQL 8.0.20 or later**
  (#344) — @hc12r. `VALUES()` in `ON DUPLICATE KEY UPDATE` has been
  deprecated since 8.0.20 (April 2020) and warns on every statement.
  Verified against live 8.4, which accepts both and warns only on the old
  one. MariaDB has no alias form and is not in the connection catalog.

- **Three MySQL driver options are now accepted** (#344) — @hc12r.
  `interpolateParams`, `maxAllowedPacket` and `clientFoundRows`.
  `multiStatements` remains deliberately rejected: generated SQL is
  interpolated rather than parameter-bound, so allowing multiple statements
  per call would turn any escaping defect into arbitrary statement
  execution rather than a corrupted value.

### Added

- **Large MySQL writes stream instead of materialising** (#345) — @hc12r.
  An append or overwrite into MySQL goes through `LOAD DATA LOCAL INFILE`
  fed from an in-process stream: rows travel as data, never as SQL text, and
  the worker holds one batch regardless of dataset size. Previously a MySQL
  sink always materialised — the bounded-memory property applied to reads on
  any driver but writes only on Postgres.

  Measured at 1,000,000 rows against MySQL 8.4:

  ```
  LOAD DATA          7.0s   ~143k rows/s   one batch of memory
  INSERT fallback   11.5s    ~87k rows/s   one batch of memory
  statement path    11.0s    ~91k rows/s   whole dataset + ~60MB of SQL text
  ```

  The statement path's wall time flatters it; its real cost is the memory,
  which is what this removes.

  The fast path needs `local_infile=1` on the server, which is off by
  default in MySQL 8. When the server refuses, Brokoli degrades to batched
  `INSERT`s inside the same transaction — same contents, same atomicity,
  still one batch of memory. Nothing to configure; the choice is made per
  write by asking the server.

- **A same-server MySQL pipeline runs inside the database** (#346) — @hc12r.
  Pushdown, previously Postgres-only, now applies to MySQL on the same
  terms: every transform rule is proven equivalent by running it both ways
  against a live server and comparing, and anything unproven runs in the
  engine.

  Making the differential corpus shared between backends immediately found a
  real bug: the aggregate compiler emitted a bare `GROUP BY`, which is
  harmless on Postgres where text equality under a deterministic collation
  is byte equality, but MySQL's default collations are case-insensitive and
  collapsed `Lisbon` and `lisbon` into one group where the engine keeps two.
  Grouping is now byte-wise on both backends; on Postgres the emitted SQL
  changed and the results did not.

  One difference between backends is documented rather than closed:
  `filter_rows` on a boolean column qualifies on MySQL and not on Postgres,
  because MySQL's `BOOLEAN` is a `TINYINT` whose driver reports the server's
  own `1`, making the comparison numeric on both sides.

- **`sink_api` streams** (#337, #327) — @hc12r. A `sink_api` node consumes
  its input in batches rather than materialising it, so posting a large
  dataset no longer bounds the run by worker memory. `source_api` cannot
  stream and the reason is recorded with it.

### Internal

- **A database backend is described in one place** (#341, #340, #343,
  ADR-024) — @hc12r. `pkg/dbdialect` holds what a backend is: quoting,
  literal escaping, type classification, the shape probe, and the casts and
  collation that make a SQL comparison agree with the Go one. Postgres was
  migrated onto it with no behaviour change, and MySQL is the second
  implementation. A backend may claim a capability only when a differential
  test proves it against the reference path.

  One dead parameter turned out to be load-bearing: the pushdown compiler
  had threaded a dialect through since it was written, and the only
  production caller passed the literal `"postgres"`, correct solely because
  an upstream gate refused everything else first.

- **The live-database tests run in CI** (#339) — @hc12r. Postgres and MySQL
  service containers, so the differential suites that back every equivalence
  claim run on every push rather than on one laptop. They had only ever run
  locally.

## [0.10.70] - 2026-08-24

An integer of a million or more could be changed as it moved between nodes,
and on some pipelines that happened silently. That is the item to act on, and
it was found while benchmarking the other one: a pipeline whose source and
sink are on the same server now runs inside the database.

### Fixed

- **Integers stopped surviving a streamed node boundary** (#334) — @hc12r.
  Reproduced against the v0.10.69 tag. A `source_db` to `sink_db` copy of a
  million rows failed:

  ```
  copy: ERROR: invalid input syntax for type integer: "1e+06"
  ```

  Values moved between nodes as NDJSON, and the decode never asked
  `encoding/json` to preserve number syntax, so every number returned as a
  `float64`. Rendering one with `%v` is not the text that went in:

  ```
  {"id":1000000}           -> float64 -> "1e+06"
  {"big":9007199254740993} -> float64 -> "9.007199254740992e+15"
  ```

  The second line is the worse one — the value changed, not its spelling.

  The failing case is the lucky one. Against an integer column Postgres
  rejects it, which is loud and safe. Against a **text** column it is accepted
  and stored, so the pipeline succeeds and the data is wrong; and anything
  past 2^53 loses digits wherever it lands. The same encoding backs resume
  artifacts and code-node input staging, so a value could change identity
  between a run and its resume. Any table with a million rows has ids in this
  range, so this is the ordinary shape of the data rather than an edge case.

  Fixed at the decode sites rather than in the renderers: the original text is
  preserved and values normalise to what the materialising path already
  produces. The code node's own output decode is covered too, so a Python
  script returning `1000000` no longer has it rendered as `1e+06`.

  `copyEscape` also formats floats explicitly instead of falling through to
  `%v`. Decoding should make that unreachable, but a renderer that can
  silently change a value is not worth leaving on the strength of "should" —
  that is exactly how this survived, since the CSV writer had already patched
  the symptom for file sinks and database sinks kept it.

  **Upgrading does not repair data already written; re-running the pipeline
  does.** The runs worth checking are the ones that succeeded against text
  columns, not the ones that failed.

  One consequence, recorded rather than hidden: JSON cannot distinguish `150`
  from `150.0`, and the decoder resolves that towards an integer so a bigint
  id survives. A float that happens to be integral therefore comes back as an
  integer. Nothing downstream can observe it — the CSV and `COPY` writers
  render both identically, aggregation accepts both, and a `number` type
  assertion matches both.

### Added

- **A same-server pipeline runs inside the database** (#324, ADR-023) —
  @hc12r. Where a `source_db` and a `sink_db` name the same server and the
  transform between them can be expressed as SQL, the segment becomes one
  `INSERT INTO … SELECT`. The engine issues a statement and reads a row count;
  no row crosses the worker.

  Measured on 1,000,000 rows between two tables on one Postgres server:

  ```
  read through the engine   12s
  run in the database        2s
  ```

  Both wrote identical data — a four-column join matches on every row, with
  none unique to either side. The win is time rather than memory; streaming
  had already bounded memory. What disappears is parsing and re-encoding a
  million rows to move them somewhere they already were.

  There is nothing to switch on and no setting on a node. The engine decides,
  the way it already decides whether to stream, and reads rows the ordinary
  way whenever it cannot prove the two produce the same result.

  It refuses by default and the conditions are strict: the same host, port,
  database, user and password as written, without DNS resolution; Postgres
  only; one consumer per node; `append` or `overwrite`; and every transform
  between them already proven equivalent by comparison against a live
  database. Two records reaching the same machine under different names are
  treated as different servers, and so is the same server reached as two
  different roles — running one role's query inside another's write is not a
  performance question.

  `BROKOLI_DATA_PLANE=interpreted` forces every pipeline back onto the
  reading path, for incidents.

  A pushed-down segment says so in the run log, naming the nodes the database
  absorbed and the rows written. Every node in the segment is credited with
  that count: those nodes did produce the rows, they simply never held them,
  so reporting zero for a source would answer "how many rows did this read"
  with a number true of the engine and false of the pipeline.

- **The SQL compiler the above depends on** (#323, #332) — @hc12r. A
  transform's plan compiles to SQL as a second backend beside the Go
  executor. Only rules whose two backends can be shown to agree are compiled:
  `drop_columns` and `rename_columns`, `filter_rows` on a text or numeric
  column, and `count`, `min` and `max` grouped by text columns.

  Brokoli's transform semantics are not SQL semantics, which is why the list
  is short. A value is compared as its rendered text, a missing value renders
  as `<nil>` and is compared like any string, and an ordered comparison is
  numeric only when both sides parse as numbers. The generated SQL reproduces
  that rather than doing the idiomatic thing — casting to double precision so
  the same precision is lost, comparing text under the C collation because Go
  compares bytes, and resolving a NULL row to the constant the Go matcher
  would produce.

  Every rule runs both ways against a live Postgres and the rows are compared;
  rules the compiler declines are asserted to be declined, so nothing starts
  being pushed down without a passing comparison. `sum` and `avg` are refused
  because floating-point addition is not associative and the database may
  aggregate in a different order than the engine reads — the same four values
  total `1` one way and `0` the other. Grouping by a numeric column is refused
  because Brokoli groups on rendered text, so `10.50` and `10.5` are two
  groups where SQL sees one.

- **ADR-023** (#333) — @hc12r. Records the decision the above implements:
  a pipeline edge carries a reference whose *kind* says where the data is, and
  whether the engine needs to read it at all, so that work can stay where the
  data already is. Includes the resolutions for staging lifetime, resume
  semantics, outbound safety, row counts, and declared validation.

## [0.10.69] - 2026-08-24

Streaming reaches `source_file`, and two bugs that had been hiding behind the
fact that it did not. Both of those were live in v0.10.68: one let a node type
an organisation's plan blocks execute anyway, and one silently discarded the
edits a Python code node made to its rows. Neither announced itself.

### Security

- **A node type blocked by an organisation's plan executed anyway on the
  streaming path** (#321) — @hc12r. The gate lived inside `runNodeLogic`. The
  streaming path dispatches through `runNodeStreamed`, which never consulted
  it, so a gated node ran whenever its upstream produced a reference and the
  run reported plain success.

  What kept it hidden is that enforcement had become a side effect of a
  memory-management decision: the same pipeline was gated below the streaming
  threshold and ungated above it, and every node type that learned to stream
  widened the hole a little further. `source_db` streaming since v0.10.67 was
  already enough to reach it; teaching `source_file` to stream made an
  ordinary two-node pipeline take that path, which is how it surfaced.

  The check now sits at the one point both execution paths pass through.
  Keeping a copy inside each is the arrangement that failed.

### Fixed

- **A code node's edits to its rows were silently discarded when the input
  arrived by reference** (#321) — @hc12r. Reproduced against the v0.10.68 tag:

  ```python
  for r in rows:
      r["name"] = r["name"].upper()
  output_data = {"columns": columns, "rows": rows}
  ```

  produced the original rows, unchanged, and the run succeeded.

  `_LazyRows.__iter__` returned a fresh generator on every iteration, parsing
  fresh dicts out of the NDJSON file. The script mutated those, they were
  dropped at the end of the pass, and the output writer re-read the untouched
  file. The harness called that the untouched lazy passthrough, which held
  until the object handed back was one the script had written to. It needs no
  unusual code to hit — any streamed input feeding the most ordinary Python
  idiom there is.

  Edits are now journaled to disk: a row written to during a pass is appended
  to a journal as the pass moves past it, and the journal becomes the file
  later passes read. Rows before the first edit are byte-copied rather than
  re-encoded, and a pass abandoned early carries the unread remainder over
  verbatim, so the dataset stays complete either way. Only scripts that
  actually mutate pay for it; a read-only pass never opens a journal, and
  memory stays constant in both cases.

  Editing a row after the pass has already moved past it cannot be placed
  correctly and now raises, with an explanation of what to do instead, rather
  than being dropped. The previous test suite covered four ways to read a
  by-reference input and none to write to one, which is how this shipped.

### Added

- **`source_file` streams, so a large CSV no longer kills the worker** (#321)
  — @hc12r. Measured on identical pipelines differing only in their source
  node, 1,000,000 rows writing a 112 MB CSV under a 512 MiB hard container
  limit: `source_file` was OOM-killed after 3 seconds (exit 137) where
  `source_db` completed in 17 MiB. Without a hard cap, `source_file` peaked at
  653 MiB resident for a 108 MB file, roughly six times the file on disk —
  `GOMEMLIMIT` does not prevent it, being a soft target the process grows past
  before the cgroup intervenes.

  The file is now read into the blob store batch by batch and the node returns
  a reference. The same run finishes at 17 MiB with output byte-identical to
  the `source_db` run, and the full chain — `source_file` into a `code` node
  editing every row into `sink_file` — completes inside the same 512 MiB limit
  with all 1,000,000 edits applied.

  CSV only, for a structural reason rather than an unfinished one: JSON's
  column set is the union of keys across every object in the file and is not
  known until the last one is parsed, so a single-pass reader would have to
  guess the columns from the first batch or read the whole file to find them.
  Formats that cannot stream keep the materialising path.

### Removed

- **`pkg/loaders/streaming_loader.go`** (#321) — @hc12r. 258 lines
  implementing exactly this feature, with four passing tests and no caller
  outside its own package. It could not be adopted as it stood: it signalled
  errors by sending a nil row and discarding the error, so a truncated file
  was indistinguishable from a complete one; it stored `""` where the
  materialising loader stores `nil` for an empty field, so whether streaming
  engaged would have changed the data; it took no context, so a cancelled run
  leaked the reader until EOF; and it sent one row per channel operation where
  the consumer wants batches. Each of those is now a test on the replacement.

## [0.10.68] - 2026-08-24

A pass over the connection subsystem and the authentication middleware.
Nearly everything here shares one shape: something was true of the system
and nothing said so — a connection that was not encrypted, a credential
that had been destroyed, a request that had never been authenticated, a
build that could not open the database type it offered.

### Security

- **A server could serve every permission-gated route with no
  authentication at all** (#316) — @hc12r. `NewServer` mounts the
  authentication middleware only `if userStore != nil`, and `serve.go`
  left it nil on two paths: a store whose `RawDB()` is not a `*sql.DB`,
  silently, and a `NewUserStore` error, behind a `WARNING` it then
  continued past. Neither is exotic — `NewUserStore` fails when the users
  table cannot be created, which is what a database still accepting no
  connections produces, and this control database did it repeatedly under
  load while the fix was being written.

  With no middleware mounted, no request carries claims, and
  `HasPermission` read a missing claim as open mode and returned `true`.
  Demonstrated: an unauthenticated request through a `settings.edit` gate
  returns `200`. Unlike the count bug in #306 this does not clear when the
  database recovers — the middleware is never mounted, so the process
  serves everything to everyone for its whole lifetime.

  Fixed at both layers, either of which closes it. Open mode is now a mark
  `JWTAuth` deliberately sets when it passes an unconfigured system
  through, and `HasPermission` allows an unauthenticated request only when
  that mark is present; a request that never met the middleware carries no
  mark and is denied. Separately, a user store that cannot be built is
  fatal — a container that exits gets restarted once the database is
  ready, where one that starts without authentication stays that way until
  somebody reads the log. A genuinely fresh install still bootstraps, which
  is asserted by test alongside the bypass.

- **Connection records could not require TLS, and did not say they were
  not using it** (#317) — @hc12r. `BuildURI` emitted no query string, so
  no `sslmode` was ever set and libpq's default applied: `prefer`, which
  attempts TLS and falls back to an unencrypted connection without an
  error if the server declines. Measured against a local Postgres with
  `ssl = off`, a connection record came up with `encrypted=false` and
  nothing in the product reported it. The `extra` field, which is where an
  operator would look, was read for API connections and ignored for
  database ones.

  Driver options now come from `extra` for Postgres, Redshift, MySQL, SQL
  Server, and Snowflake. The allowlist is over keys rather than values:
  libpq accepts `host`, `dbname`, and `user` as connection parameters, and
  honouring those from `extra` would let the field redirect a connection
  away from the record it belongs to. Values reach the driver verbatim so
  the driver validates them — a misspelled `sslmode` fails the connection
  loudly instead of being dropped, because an option that vanishes quietly
  leaves an operator believing they have encryption they do not have.

  No default changed. An unconfigured connection still gets the driver's
  own default; what is new is that the demand is expressible.

### Fixed

- **A REST read-modify-write destroyed connection credentials** (#317) —
  @hc12r. `GET` a connection, change the host, `PUT` it back — the
  ordinary way to use the API, and the way the docs describe it. Reads
  mask encrypted refs as `encrypted://********`, and the update path
  treated a non-empty ref as authoritative, so the mask was written back
  as the credential and the real one was gone. Both the password and the
  `extra` blob. The `PUT` returns `200` with the edit applied, so nothing
  signals the loss; the connection fails at the next run with a decryption
  error that points at the crypto layer rather than at the write that
  caused it. What made it easy to miss is that the obvious sentinel was
  already handled — the bare password had an explicit `!= "********"`
  guard — and `maskRef` later introduced a second sentinel nothing
  checked. Both are named constants now, normalised in one place.

- **The connection test reported success for connections that ignored
  their own options** (#317) — @hc12r. Found by driving a running server
  rather than the package: a connection carrying `"sslmode": "require"`,
  tested against a Postgres with TLS switched off, answered `Connected
  successfully` for a session in the clear. The Test handler decrypted the
  `extra` blob into a local map so the HTTP paths could read headers out
  of it and left the encrypted value on the connection, and `BuildURI`
  reads `c.Extra` — an encrypted blob parses as no options at all. The
  runtime path was never affected, since the connection resolver puts
  plaintext back before building the URI; the only thing misreporting was
  the button whose purpose is to say whether the connection works.

- **The connection test ignored the operator's outbound network policy**
  (#317) — @hc12r. `netguard.Outbound()` reads
  `BROKOLI_OUTBOUND_ALLOW_CIDRS` and `BROKOLI_OUTBOUND_ALLOW_PRIVATE`;
  `netguard.Default` is hardcoded and reads neither. Pipeline nodes use
  the first, the test button used the second. An operator whose internal
  services sit on `10.20.0.0/16` could run pipelines against them while
  the test button called the same connection unreachable.

- **Connection types with no compiled driver blamed the build** (#317) —
  @hc12r. The catalog offers Snowflake, BigQuery, Databricks, Oracle, and
  SQL Server; only pgx, mysql, and sqlite are compiled in. That surfaced
  as database/sql's `unknown driver "snowflake" (forgotten import?)`,
  which reads as a defect rather than a product limitation. The error now
  names the limitation and lists the drivers that are available, and it
  names the scheme rather than the URI, which carries the password.

### Changed

- **One connection URI builder instead of two** (#317) — @hc12r. The
  engine carried a second builder that handled Redshift, Snowflake, and
  SQL Server and pinned `sslmode=require`. It had a table test asserting
  exactly that, and it had never been called — so the suite read as though
  Brokoli required TLS to Postgres while the live path asked for nothing,
  and those three connection types fell through to `return c.Host`. A
  `conn_id` pointing at a Redshift cluster injected a bare hostname as the
  node's `uri`, dropping the port, database, and credentials, and reached
  the Postgres driver as a malformed DSN whose error named neither the
  connection nor the reason.

  The two are now one, built through `net/url` rather than `fmt.Sprintf` —
  the dead builder interpolated credentials into a format string, so a
  password containing `@` or `/` changed which host the URI pointed at.
  `BuildsURI` reports whether a type has an engine driver at all, and the
  resolver no longer fabricates a URI for the six that do not.

### Added

- **The server reports which version it is running** (#315) — @hc12r.
  `/api/system/info` returns the build version, and the UI shows it in the
  sidebar and in Settings. Answering "what is actually deployed here"
  previously meant reading the image tag from outside the product, which
  is a different question and can disagree.

## [0.10.67] - 2026-08-23

### Security

- **Two authentication controls that silently disabled themselves**
  (#306) — @hc12r. Both were found by watching a running deployment
  rather than by reading code.

  `UserCount()` discarded its query error and returned `0`. Zero users
  means "fresh install", and a fresh install serves every `/api/auth/*`
  path without authentication so the first admin can be created. So any
  transient database failure — a restarting server, an exhausted pool, a
  network blip — presented a live system as an unconfigured one, and
  while it lasted `JWTAuth` waved requests through, `CreateUserHandler`
  skipped its admin check, and the account it created was forced to
  `admin`. An unauthenticated `POST /api/auth/users` during that window
  creates an administrator. Observed happening on its own, twice, while a
  Postgres backend was being OOM-killed under load; it needs no attacker
  to cause the outage, only to be present during one. `UserCountErr` now
  reports the failure and every caller refuses rather than assuming zero;
  `UserCount` keeps its signature but returns `-1`, deliberately not `0`,
  so a missed caller cannot read a failure as "unconfigured".

  Account lockout never worked on Postgres. The store layer implements
  `LoginAttemptStore` correctly for both dialects and nothing called it —
  `UserStore` had its own copy written against a raw `*sql.DB`, which had
  to guess the column types the store had already chosen and guessed
  wrong: an integer into a `boolean success`, an RFC3339 string into a
  `timestamptz attempted_at`. Every error on that path was discarded, and
  `IsLocked` reports "not locked" when its count fails, so five wrong
  passwords locked nothing on any Postgres deployment. Confirmed on a
  live instance before fixing: `login_attempts` present, **0 rows after
  thousands of logins**. `UserStore` now delegates to the store and no
  longer creates the table; `IsLocked` treats an unreadable count as
  locked, which costs nothing because the count only fails when the
  database is unreachable and authentication needs the same database.

### Added

- **Sources and sinks stream, so dataset size is bounded by disk rather
  than worker memory** (#307, closes #304) — @hc12r. The streaming
  executor covered `code` and `transform` only, so every run materialised
  the whole source result before anything downstream could stream it, and
  a query larger than the memory budget failed with no configuration that
  helped — raising the budget moved the wall, raising it far enough
  brought back the OOM kill it was added to prevent. `source_db` now
  flushes NDJSON batches into the blob store and returns a reference,
  `sink_db` pulls batches into `COPY`, and `sink_file` writes csv and
  json incrementally (`sql` keeps the batch path: it reads one cell from
  the first row). Measured on a worker capped at 460 MiB: 200k rows and
  106 MB of CSV went from an OOM kill to a 12.5s success, and 1,000,000
  rows writing 528 MB completed in 60s with 11 MiB of live heap.
  Equivalence is asserted rather than assumed — the streamed and
  materialising query paths share one row scanner and are compared over
  5000 rows across seven columns against a real Postgres, and the
  streamed file writer is compared byte-for-byte with the buffered one at
  five batch sizes.

- **`sink_db` takes `truncate`** (#312, closes #299) — @hc12r. An
  overwrite clears with `DELETE`, which leaves one dead tuple per removed
  row, so a table replaced on every run sits at a multiple of its live
  size and each clear scans the leftovers. Ten overwrite cycles of a
  50,000-row table: `DELETE` 7013ms / 500,000 dead tuples / 137 MB
  against `TRUNCATE` 2591ms / 0 / 13 MB. Off by default and deliberately
  so: `TRUNCATE` takes an `ACCESS EXCLUSIVE` lock, so anything reading
  the table blocks for the whole load where `DELETE` lets readers keep
  seeing the previous contents. Right for a staging table nothing queries
  mid-load, wrong for one backing a dashboard, and the platform cannot
  tell which it is looking at. Ignored on SQLite, which has no `TRUNCATE`
  and already optimises a whole-table `DELETE` into the same thing.

- **File nodes say when they are on storage the next run cannot see**
  (#309, closes #305) — @hc12r. `source_file` and `sink_file` read and
  write the filesystem of whichever worker runs them, so with more than
  one replica each pod has its own copy. Within a run that is fine; every
  node of a run executes on one worker. Across runs it is a coin flip,
  and the quiet outcome is the dangerous one: the later run lands on a
  pod holding an older copy, reads it, and succeeds against stale data.
  Demonstrated with two workers holding different files at one path — a
  green run, and whichever pod dispatch picked decided the contents.
  Brokoli cannot make a local filesystem shared, so it now reports the
  hazard at startup, at pipeline validation, and on a failed read, and
  records which pod and how old the file was on every read and write. A
  deployment with one worker, or with `BROKOLI_DATA_DIRS_SHARED=1` set
  over real shared storage, is completely silent.

### Fixed

- **Postgres writes go through COPY instead of rendered SQL** (#300,
  #302, closes #282) — @hc12r. The sink rendered every value into SQL
  text: 25,000 rows of five columns became 1.9 MB of literals to ship and
  parse a statement at a time. `COPY` carries them as data. The text
  format is used rather than binary because the server parses a COPY text
  field exactly as it parses a literal, so a string still lands correctly
  in a numeric or timestamptz column — which is what every file and API
  source produces. Reading the destination types from the catalogue and
  coercing in Go was measured rather than assumed and lost: 240ms against
  155ms on a table with a primary key, because the server's time goes to
  index maintenance rather than parsing.

  The encoders are buffered, which turned out to be the larger half.
  `io.Pipe` hands each write straight to the reader goroutine and blocks
  until consumed, so writing row by row cost one scheduler round-trip per
  row — 25k rows spent ~490ms in handoffs, and the first cluster
  comparison showed COPY and the old path within noise of each other. The
  same pattern was in `EncodeArrowJSON`, which runs on every node output
  artifact and every spilled dataset: 160ms to 70ms. Per sink node, end
  to end: 25k rows 816ms to 228ms, 100k rows 2877ms to 680ms — about 91%
  of what `psql` achieves loading the same rows inside the database
  container with no network at all.

- **Concurrent overwrites of the same table no longer collide** (#311)
  — @hc12r. An overwrite says "this table's contents become exactly these
  rows", and two of those at once have no defined outcome. Unserialised
  they interleaved and failed two ways, both seen with four pipelines
  writing one table: `deadlock detected`, and `duplicate key value
  violates unique constraint` — the second because one transaction's
  `DELETE` cannot see the other's uncommitted rows, so both insert the
  same keys. Neither is something the pipeline author can fix. Overwrite
  now takes an `EXCLUSIVE` lock inside its existing transaction: writers
  wait for each other, readers keep seeing the previous contents until
  the winner commits. 29/32 concurrent runs succeeded before, 112/112
  after.

- **The engine and the loaders agree on which directories are usable**
  (#309) — @hc12r. There were two allowlists. The engine kept a
  configurable one (`BROKOLI_DATA_DIRS`, defaulting to `/data`, `/tmp`
  and the working directory) and checked a path before handing it to a
  loader; the loader checked again against a hardcoded pair — the working
  directory and the system temp directory — and refused anything else. So
  they disagreed on their own defaults: `/data` was in the engine's list
  and unreachable in practice, and configuring a new directory passed the
  first check and failed the second with an error naming no directory and
  no setting. Mounting shared storage at `/data` and configuring it
  correctly failed every run until this.

- **The event bridge says when it has caught up** (#313, closes #308) —
  @hc12r. `Bridge` returned nothing, so nothing could tell "the bridge has
  applied everything" from "the bridge is still working", and its tests
  slept a fixed 50-100ms before asserting. Under load that produced
  failures shaped exactly like real defects — 192 of 200 log entries, a
  dashboard snapshot missing only its most recent run — and cost two
  preflight runs before being recognised. It now returns a channel closed
  once the last event is applied; every fixed sleep in those tests is
  gone.

- **Preflight reports which package failed** (#301) — @hc12r. The race
  suite piped into `tail -5`, which throws away the `--- FAIL` lines and
  the failing package name, so a failure showed as a bare `FAIL` with
  nothing to act on and the only way to learn more was to re-run the
  whole suite. Output now goes to a log in full.

- **The `replace` mode alias is pinned by a test** (#303) — @hc12r.
  `replace` is an alias for `overwrite` in two separate places on the COPY
  path, and nothing asserted it. Dropping it from one would have turned a
  replace into a silent append.

### Behaviour Changes To Read Before Upgrading

- Postgres `append` and `overwrite` writes now go through `COPY` rather
  than generated `INSERT` statements. Values are carried as data and
  parsed by the server exactly as literals were, so results are
  unchanged; `BROKOLI_SINK_COPY=0` returns to the statement path without
  a different build. Upsert and table creation keep the statement path.

- `overwrite` takes an `EXCLUSIVE` table lock for the duration of its
  transaction. Concurrent writers to the same table now wait instead of
  interleaving. Readers are unaffected.

- `api.UserCount()` returns `-1` when the count cannot be determined,
  where it previously returned `0`. Any caller treating a non-positive
  result as "no users configured" must be checked; prefer `UserCountErr`.

- `sodp.Bridge` now returns a channel. Existing calls that ignore the
  return value keep compiling and behave identically.

- `BROKOLI_DATA_DIRS` is now honoured by the file loaders as well as the
  engine, so a directory configured there becomes genuinely usable. An
  empty value falls back to the defaults rather than allowing nothing,
  because unset and empty are indistinguishable to most tooling and
  reading empty as "disable every file node" would break a deployment
  silently. To restrict access, name the directories that are allowed.

- A deployment running more than one worker without shared storage for
  the data directories now logs a warning at startup and warns on
  pipelines that read a file they do not write. Set
  `BROKOLI_DATA_DIRS_SHARED=1` once the directories are on shared storage
  to confirm it and silence the warnings.

## [0.10.66] - 2026-08-23

### Fixed

- **Cursor pagination accepts the parameter name it hands out** (#296)
  — @hc12r. Responses return the next position as `cursor` and the
  endpoints read only `after`, so a client feeding the response's own
  cursor back had it ignored, got page one again with `has_next` still
  true, and looped forever hammering the server with no way to tell.
  Against a pipeline with 110 runs, walking by `cursor` produced 1000
  rows over 40 pages, 975 of them duplicates; it now terminates after 5
  pages with 110 unique ids. `after` still works and wins if both are
  given.

- **The outbound allowlist covers every path a pipeline uses** (#297) —
  @hc12r. `BROKOLI_OUTBOUND_ALLOW_CIDRS` reached only the API fetcher,
  so `sink_api` and webhook hooks stayed on the closed default: an
  operator who allowlisted their internal range could read from an
  internal service but not post back to it, with an error that gave no
  hint the allowlist did not cover that path. All three now resolve the
  same policy.

## [0.10.65] - 2026-08-23

### Fixed

- **Audit entries find their tenant even outside the org middleware**
  (#293) — @hc12r. #291 stamped the org from the request context, which
  only the enterprise-middleware routes have; pipeline, connection and
  variable entries were still written tenant-less and stayed invisible
  to the org-filtered audit query. The org is now resolved from the
  context, then the signed session token, then the org resolver.
  Verified on a cluster: connection changes now appear in the audit API
  where they were previously recorded and unreachable.

### Added

- **Run totals describe the deployment, not one process** (#294) —
  @hc12r. `/metrics` exposed counters held in whichever process
  answered, and runs execute on workers — so the API, the stable scrape
  target, reported zero runs and zero successes while the fleet was
  busy, and a worker's own counters disappeared when autoscaling
  removed the pod. `brokoli_runs_by_status{status=...}` is derived from
  the runs table and cached for ten seconds; a query error serves the
  last known numbers rather than reporting an idle fleet. The
  per-process counters are unchanged. `store.Store` gains
  `CountRunsByStatus`.

## [0.10.64] - 2026-08-23

### Fixed

- **Audit entries record which tenant an action happened in** (#291) —
  @hc12r. The enterprise audit query filters by the caller's org, and
  core's hook had nowhere to put one: `extensions.AuditEntry` had no
  metadata field, so every entry core records — pipelines, connections,
  variables, runs — was written without a tenant and could never be
  read back. A live instance held 67 entries and returned 1. The 66
  invisible ones were every pipeline and connection change on the
  system, leaving "who changed this connection?" unanswerable through
  the product while the row sat in the database. `AuditEntry` gains an
  additive `Metadata` map and `api.AuditLog` stamps the request's org
  into it; single-tenant deployments record no org rather than an empty
  one. Entries written before this keep their empty metadata.

## [0.10.63] - 2026-08-23

### Fixed

- **A literal is decided by the value's type, not its printed text**
  (#287) — @hc12r. `formatValue` rendered everything with `%v` and then
  guessed the type back out of that text, so any string that looked
  like something else was written as that something else: `"00123"`
  stored as `123`, a card number stored as a numeric, `"1.50"` as
  `1.5`, `"true"` rejected outright by a text column, and `""`
  collapsed into NULL. Found by round-tripping a table of awkward
  values and diffing it against the source — two of ten rows came back
  wrong, with nothing failing. Strings are now always quoted, which
  every dialect coerces correctly into numeric, boolean and date
  columns. Empty strings resolve where the ambiguity actually lives:
  the CSV loader emits nil for an empty field (so CSV to database is
  unchanged end to end) and the writer keeps `""` as an empty literal,
  with `EmptyStringAsNull` restoring the old writer behaviour.

### Added

- **The worker's shutdown drain window is configurable** (#288) —
  @hc12r. It was a fixed 25 seconds, which fits Kubernetes' default
  grace period but not real pipelines: a 56-second job was abandoned by
  every graceful eviction — a KEDA scale-down, a node drain, a rolling
  deploy — and the run was then failed rather than retried. With worker
  autoscaling on, the fleet destroyed work every time it shrank.
  `BROKOLI_WORKER_DRAIN_TIMEOUT` accepts a duration or bare seconds;
  the enterprise chart derives it and `terminationGracePeriodSeconds`
  from one value so they cannot drift apart.

## [0.10.62] - 2026-08-22

### Fixed

- **The scheduler never picked up pipelines created after it started**
  (#284) — @hc12r. `Start()` registers what exists at boot and
  `SyncPipeline` keeps an in-process scheduler current, but in
  distributed mode the scheduler is its own pod while `SyncPipeline` is
  called from the API's process. Nothing carried the change across and
  nothing re-read the store, so a scheduled pipeline created after the
  scheduler pod started simply never ran — no error, no missed-run
  warning, nothing in the run list, until someone restarted the pod.
  The scheduler now reconciles its cron entries against the store every
  30 seconds, picking up additions and applying disables, reschedules
  and deletions. Found by deploying a `* * * * *` pipeline and watching
  it not fire while an older one kept firing beside it.

- **`S3StoreConfig.CredentialsProvider` was declared twice** (#285) —
  @hc12r. #271 and #272 added the same field independently and both
  merged, leaving `main` unable to compile. One declaration remains,
  documenting the precedence the code already implements.

## [0.10.61] - 2026-08-22

Found by running realistic pipelines — migrations, parallel extraction,
incremental loads, failure injection — against real databases and a
k3s cluster, rather than by reading the code.

### Fixed

- **Timestamps reached databases in Go's own format** (#276) — @hc12r.
  `formatValue` fell through to `%v` for a `time.Time`, emitting
  `2026-08-22 00:00:00 +0000 UTC`, which no dialect parses. Every
  pipeline carrying a DATE or TIMESTAMP column from a database source
  into a database sink failed on the write. Each dialect now declares
  its layout, and dialects whose literal carries no zone get the value
  in UTC rather than reinterpreted in the session timezone.

- **Sinks reported zero rows** (#281) — @hc12r. `row_count` came from a
  node's output, and sinks are terminal, so the run summary answered
  "how many rows did we load?" with 0 for every sink. Terminal nodes now
  report what they consumed.

### Changed

- **Node parallelism scales with the worker** (#277) — @hc12r. It was
  the constant 4: with six independent extracts, two waited about 3.5
  seconds for a slot while their database sat idle. The default now
  derives from CPUs and is clamped by the memory budget, with
  `BROKOLI_MAX_PARALLEL_NODES` overriding.

- **Admission backpressure applies only under memory pressure** (#278)
  — @hc12r. A worker waited 3 seconds after every admission whatever its
  memory situation, capping throughput at roughly one run per four
  seconds per worker. Measured on a queue that stayed full with 75% of
  memory free: 20 runs took 39.3s before, 12.6s after — 3.1x. The guard
  is unchanged where memory is genuinely tight.

### Added

- **Oversized query results fail instead of killing the worker** (#279)
  — @hc12r. A result too large for the pod took the whole process down
  with an OOM kill, losing every co-running run, and was reported 27
  seconds later as "interrupted mid-execution" with no mention of
  memory. The scan now measures rows against a budget (30% of the
  memory limit, `BROKOLI_DATASET_MEMORY_BUDGET` to override) and fails
  that one run in about a second, naming the size, the budget and the
  remedies.

- **Outbound allowlist for self-hosted deployments** (#280) — @hc12r.
  `source_api` blocked all private addresses with no production
  override, so an on-premise install could not reach its own internal
  APIs. `BROKOLI_OUTBOUND_ALLOW_CIDRS` permits named ranges (preferred),
  `BROKOLI_OUTBOUND_ALLOW_PRIVATE` opens all of them. Default unchanged.

## [0.10.60] - 2026-08-22

### Fixed

- **One-sided `range` quality checks were inverted** (#274) — @hc12r.
  `min` and `max` each defaulted to `0`, so a check written as
  `{"rule":"range","params":{"min":0}}` evaluated as `[0, 0]` and
  reported every positive value as a violation — a blocking gate
  asserting the opposite of its intent. An omitted bound now means
  unbounded on that side, and a range check with neither bound is
  reported as misconfigured rather than passing silently.

- **Schema-qualified sink tables never resolved** (#274) — @hc12r.
  `quoteIdent` wrapped a dotted name in a single pair of quotes, so a
  sink writing to `analytics.daily_revenue` asked for a table whose
  name contains a dot and failed with `relation does not exist` while
  that relation existed. Each part is now quoted separately, with the
  injection guard still applied across the whole identifier and empty
  parts rejected.

## [0.10.59] - 2026-08-22

### Added

- **S3 stores accept a credentials provider** (#272) — @hc12r.
  `S3StoreConfig` could only express a static access-key pair, so
  credentials that expire and are re-minted — an STS session behind
  `aws.CredentialsCache`, for instance — could not be passed in at all.
  The new `CredentialsProvider` field is additive and wins over the
  static pair when both are set; leaving it nil keeps the previous
  behavior exactly, which the new tests pin alongside the two new
  paths.

### Fixed

- **Sidebar nav array is typed** (#272) — @hc12r. The bare literal made
  TypeScript infer a union whose narrower members lack `disabled` and
  `badge`, making every read of those properties in the markup an
  error. No behavior change.

## [0.10.58] - 2026-08-22

### Added

- **S3-compatible `artifact.Store` backend** (#269) — @hc12r. Closes #268
  (M1) and the "cloud-storage backend" item `ADR-012` has carried as
  deferred since it was first accepted. A third `artifact.Store`
  implementation alongside `LocalDiskStore`/`SQLArtifactStore`'s blob
  store — content-addressed, namespace-scoped, no tenant or
  multi-bucket awareness — working against any S3-compatible provider
  via a configurable endpoint and path-style addressing. Writes stream
  through a real multipart uploader in bounded, part-sized chunks
  rather than buffering the whole value, verified by a test that
  tracks the largest single read request the SDK ever makes against a
  24MiB payload rather than relying on code review alone. Reads verify
  their checksum incrementally as the caller consumes the stream,
  rather than eagerly before returning any bytes, to avoid the same
  buffering cost on the read side. Does not include a full
  `engine.ArtifactStore` wrapper — that manifest/fencing layer is a
  separate, larger design decision left for whoever builds on this,
  per the ADR-012 update landing alongside it.

## [0.10.57] - 2026-08-21

### Fixed

- **Dark mode restraint pass: gradient washes, decorative green, and a
  green-tinted "neutral" ramp** (#263) — @hc12r. Direct user feedback on
  the live product ("green eating the whole screen," "hard to read")
  surfaced three separate problems the v1.4/v1.5 identity pass had
  missed by never validating against the running app: large-surface
  gradients built from `--accent-glow` (a token with no light/dark
  distinction) reading as haze on a near-black canvas; individually-
  small decorative green touches (kicker text, icon tiles, step
  badges, a sidebar pill) stacking into visual noise across one view;
  and, once those were fixed, a deeper issue — every foundational dark
  surface still carried a faint green component in its own RGB ratio,
  which reads as a cast across a full screen even with zero "green UI"
  on it. Introduces `--surface-wash` (transparent in dark, light-only
  elsewhere) for the first problem, a functional/decorative split for
  the second, and adopts a genuinely neutral (no hue identity) surface
  ramp plus dedicated `--bk-brand-field`/`--bk-selected` tokens for the
  third, per `brokoli-visual-identity-v1.6-neutral-dark.html`.

## [0.10.56] - 2026-08-20

### Changed

- **Apply Brokoli visual identity v1.4 + v1.5** (#261) — @hc12r.
  Introduces the v1.5 theme-role token layer (`--bk-canvas/-shell/-
  surface-1/2/3/-border[-strong]/-text-*/-brand-*`), re-points dark
  theme's semantic tokens to alias it, and rolls out the new
  `PageHeader` standard header component across the app in place of
  three inconsistent treatments (a gradient hero, a free-floating
  title, and card-based headers). Fixes two real bugs found along the
  way: dark theme's canvas/input backgrounds were literally
  `#000000`, and dark theme cards still cast a real box-shadow despite
  the spec calling for none. Also fixes the FOUC-prevention script,
  which read a `localStorage` key that theme.ts never wrote to, so the
  anti-flash guard never actually worked.

### Fixed

- **PageHeader clipping absolutely-positioned overlays** (#262) —
  @hc12r. `AlertDrawer`'s dropdown panel (meant to float below the
  header) was silently clipped by `overflow:hidden` on the header
  card, introduced by the v1.4/v1.5 rollout. Removes the blanket
  clip in favor of per-section corner rounding, so header content can
  round its own corners without cutting off anything positioned
  absolutely inside the header row.

## [0.10.55] - 2026-08-19

### Fixed

- **SSRF-hardening regression: self-referential sample data broken on
  k8s** (#260) — @hc12r. The SSRF fix in #253 rejected a legitimate
  self-referential sample-data request pattern under Kubernetes
  networking, where a pod reaching its own service IP looks identical
  to an SSRF attempt from the outside. Narrows the check to distinguish
  the two cases instead of blocking both.

## [0.10.54] - 2026-08-19

Note: `v0.10.53` was tagged but its release build failed on a flaky
timing-sensitive test (`TestRecoverNonTerminalRunsEventuallyFailsAfterRepeatedDefers`
in `engine`, unrelated to the commits it carried) and was never
published. Every change it would have shipped is included here,
along with what merged immediately after.

### Security

A targeted audit (5 parallel passes: multi-tenant scoping, injection/
traversal, secrets, SSRF, RBAC completeness) across core and EE found
9 concrete issues. Read together they weren't nine unrelated bugs —
two systemic gaps (ownership checks and outbound-URL safety
implemented per-handler, by hand, with no enforcement point that fails
closed when a handler forgets) wearing nine symptoms. ADR-022 (below)
is the design record; these are the direct fixes:

- **SSRF: `sink_api`, pipeline hooks, and connection test had zero
  protection** (#253) — @hc12r. Introduces `pkg/netguard`, the single
  sanctioned SSRF-safe HTTP client, and requires it at every outbound
  call site this audit found unprotected.
- **Org-scoping gap in run handlers and OSS RBAC role conflation**
  (#254) — @hc12r. Run handlers were missing an org-ownership check
  present elsewhere in the codebase; OSS's RBAC model conflated two
  roles that EE keeps distinct.
- **Second-order SQL injection via unescaped identifiers** (#256) —
  @hc12r.
- **Plaintext source password leaking into migrate-node run logs**
  (#257) — @hc12r.
- **Webhook token comparison not actually constant-time** (#258) —
  @hc12r. A correct constant-time-compare helper already existed
  elsewhere in the codebase; this call site wasn't using it.
- **Kubectl argument injection in k8s secret ref resolution** (#259)
  — @hc12r.
- **Static API key auth never actually worked past bootstrap** (#251)
  — @hc12r. Found via local end-to-end SDK testing, not code review:
  `APIKeyAuth` and `JWTAuth` are independent stacked middlewares, and a
  validated static key produced no signal `JWTAuth` could recognize —
  every `Client(api_key=...)` call beyond initial admin bootstrap
  silently required a JWT a static key was never going to produce.
  `APIKeyAuth` now stamps admin-equivalent claims into the request
  context on success, in the same shape a real JWT produces.

### Added

- **Drag-and-drop node placement from the palette to the canvas**
  (#247) — @hc12r.

### Fixed

- **CI hang: drop `--with-deps` from playwright install, add job
  timeouts** (#248) — @hc12r.
- **sodp: broadcast a mutation's delta before acking it, not after**
  (#249) — @hc12r. Ordering bug found while chasing an apparently-flaky
  test; the delta broadcast and the ack were sequenced against spec
  §8.3.

### Changed

- **Apply official brand identity to README and favicon** (#250) —
  @hc12r. Replaces the old placeholder favicon (unrelated shape,
  unrelated colors) with the official Forked-B mark, and recolors
  README badges from an arbitrary blue to the official brand green.

### Docs

- **ADR-021: browser-based login for the CLI and SDK** (`brokoli auth
  login`) (#252) — @hc12r.
- **ADR-022: enforce tenant isolation and outbound-request safety
  structurally** (#255) — @hc12r. Design record for the systemic
  pattern behind the security batch above — status: proposed, since
  the structural store-layer scoping decision is a larger migration
  than the direct fixes already shipped.

## [0.10.52] - 2026-08-18

### Changed

- **`extensions.RunCancelRelay` renamed to `RunCancelBroadcaster`** (#244)
  — @hc12r. Pure rename, no behavior change: `Registry.CancelRelay` →
  `CancelBroadcaster`, `Engine.CancelRelay` → `CancelBroadcaster`. Frees
  "Relay" as a product name ahead of the ecosystem strategy work below —
  this mechanism is unrelated pub/sub run-cancellation, not the proposed
  private-execution-gateway product. `CancelRelayedRun`/`BroadcastCancel`
  keep their names.

### Docs

- **ADR-012, ADR-015, ADR-016, ADR-017 promoted from proposed to
  accepted** (#245) — @hc12r. Each ADR's core decision is shipped and
  load-bearing in production (artifact/dataset plane, physical
  execution plans, plugin packaging/distribution, worker protocol v2
  instance dispatch); each gets a dated Update section documenting what
  shipped and what legitimately remains deferred.

- **ADR-020: adopt the ecosystem framing, Phase 1 portfolio** (#246) —
  @hc12r. New ADR formalizing the decidable slice of the "Brokoli
  Ecosystem" strategy proposal: Phase 1 portfolio (Brokoli/Nodus/Relay/
  Strata), the OSS/Enterprise boundary principle, and the rule that
  productization follows architecture. Status: proposed — an open
  decision, not self-approved.

## [0.10.51] - 2026-08-17

### Added

- **Connector work-unit planning protocol** (#234) — @JefferMarcelino.
  Protocol-layer slice of M3 (#39, ADR-013): a source plugin can now
  optionally break an already-discovered stream into independent work
  units via a new `plan` command (`CmdPlan`/`MsgWorkUnit`), instead of
  one blocking `read` for the whole stream. `NodeTypeDecl.SupportsPlan`
  opts a node type in; `Runner.ReadUnit` threads a planned unit's params
  back through `read` via `ReadParams.Unit`. Wire format and host
  plumbing only — no engine/scheduling wiring yet, and a plugin that
  only implements `read` is completely unaffected.

- **Physical run instances, pre-run plan, and Chronicle** (#239) —
  @hc12r. The run details view now shows Nodus physical instances
  beneath the logical run summary (node, instance key, status, attempt,
  rows, duration), the pre-run physical execution plan, and Chronicle,
  an exploratory view over a run's events.

- **Lineage observed metadata and column mappings** (#242) — @hc12r.
  The lineage graph now carries observed schema and statistics per node
  (row count, column count, per-column null%/unique%/min/max) and
  evidence-backed column mappings across shared assets, sourced from a
  bounded runtime profile persisted after every successful node without
  adding profiling latency to the run itself.

- **Password login can be disabled by an enterprise integration**
  (#241) — @hc12r. `api.PasswordLoginEnabledFunc`, an optional hook
  (nil by default, matching every existing deployment) an EE
  integration can set to require OAuth instead of local passwords.

### Fixed

- **Instance-level artifact writes were not fenced against stale
  attempts** (#240) — @hc12r. ADR-017's instance dispatch wrote a
  physical instance's artifact and only afterward settled the execution
  attempt through fencing, so a worker that stalled past the
  dispatcher's timeout and then finally delivered its result could
  silently overwrite a retry's already-written, correct output.
  `FencedArtifactWriter` guards the write with `(attempt, fencing
  generation)` ordering, extending the same race protection
  `CompleteAttempt`/`FailAttempt` already gave the attempt's status to
  the data underneath it.

## [0.10.45] - 2026-08-16

### Fixed

- **Same-store WorkOrder cancellation** — core workers now observe durable
  run cancellation while executing physical instances, including in-flight
  code processes and source API page requests.

## [0.10.44] - 2026-08-16

### Fixed

- **Remote WorkOrder cancellation** — workers now observe durable run
  cancellation and terminate in-flight code-node processes instead of
  continuing physical instance execution or settling late results.

## [0.10.43] - 2026-08-16

### Added

- **Capability-aware physical placement** (#236) — @hc12r. Placement
  requirements now travel from the run trigger through core WorkOrders and
  JobQueue jobs, allowing Nodus to deliver physical instances only to
  workers advertising the requested capabilities.

## [0.10.42] - 2026-08-16

### Added

- **Durable physical-instance dispatch** (#236) — @hc12r. Enterprise
  WorkPool and core workers can now execute dynamic-expansion instances as
  fenced WorkOrders while the control plane keeps orchestration and artifact
  settlement. Whole-pipeline queue execution remains compatible.

- **Eligible pagination pages as physical instances** (#143) — @hc12r.
  Offset and numbered `source_api` pages with dataset-visible termination can
  be dispatched concurrently to workers, retried independently, and settled
  through execution-attempt fencing. Metadata-driven termination,
  checkpointed pagination, rate limiting, artifact responses, and unsupported
  strategies retain the existing in-process path.

### Behaviour Changes To Read Before Upgrading

- With instance dispatch enabled, eligible offset and numbered pagination
  pages run on WorkPool or JobQueue workers rather than the API process. Page
  configurations using response metadata, checkpoints, rate limiting, or
  artifact responses continue to use the existing local pagination path.

## [0.10.41] - 2026-08-15

### Added

- **Users carry a display name and email** (#232) — @hc12r. An
  account's username is an identity key, and for SSO accounts it is
  provider-prefixed, so using it as the label greeted people with a
  string that was never their name. `users` gains optional
  `display_name` and `email` columns (added by `ALTER TABLE` on boot,
  so existing installs need no migration step), read back on every
  user load and carried in the session token as claims omitted when
  empty. `SetProfile` writes only non-empty values, so one provider
  can never blank what another supplied. The UI resolves labels
  through a single `userLabel()` helper. Identity still keys on
  username: nothing here creates, merges, or re-points an account.

## [0.10.40] - 2026-08-15

### Added

- **Collapsible sidebar and grouped navigation** (#229) — @hc12r. The
  sidebar collapses to a 64px icon rail via a chevron in the brand
  zone, and nav items are grouped, with the Data group collapsible.
  Both states persist and apply pre-paint, so there is no width flash
  on load. Below 1025px the rail is forced as before; the duplicated
  60px collapsed width is unified onto the 64px token. Settings moves
  to a footer utility cluster with the user row and status controls.

- **Visual connector catalog** (#229) — @hc12r. Creating a connection
  starts from a searchable card grid grouped by category, each type
  with a glyph and one-line description, then a form step with a
  summary tile and back control; editing is unchanged.
  `GET /api/connection-types` additionally serves `description` and
  `icon` per type, and per-field hints now surface as input
  placeholders. A handler test pins the payload shape.

- **Local preflight script** (#229) — @hc12r. `preflight.sh` runs
  every CI gate locally (formatting, vet, UI build and checks, race
  tests, build smoke, gosec vs baseline, govulncheck, licenses) so
  regressions surface before a push, not in CI. CONTRIBUTING
  documents the local-first workflow.

## [0.10.39] - 2026-08-15

### Added

- **Application-surface redesign** (#221) — @hc12r. The pipeline
  inventory gains run metrics, filtering, saved views, density
  controls, and a five-run history strip per pipeline (new
  `run_history` field in `GET /api/pipelines/summary`); the DAG editor
  gains a searchable node library, explicit save state, and undo for
  node and edge movement. Settings, Variables, Connections, Plugins,
  API Integrations, Calendar, and the Dashboard adopt one
  control-center hierarchy — operational hero, section navigation,
  bordered control surfaces, deliberate empty/loading/error states —
  with width caps removed from management routes and layouts hardened
  against horizontal overflow. Existing routes and graph behavior are
  preserved.

## [0.10.38] - 2026-08-15

### Fixed

- **Cancellation converges mid-node, with or without the relay** (#226)
  — @hc12r. Third live-found cancellation bug of the arc, surfaced by
  the Redis kill-tests: a Redis restart permanently killed every pod's
  relay subscription (the receive loops exited on first error — fixed
  in brokoli-ee alongside this), after which cancels returned 200 with
  intent durably recorded while a 45-second single-node run completed
  anyway. Wave boundaries never arrive during a node, and single-node
  pipelines have none at all. A per-run watcher now polls the durable
  cancel_requested flag every 5 seconds while nodes execute and
  cancels the run context the moment it appears: any run converges
  within one poll interval of a cancel landing, regardless of relay
  transport health or pipeline shape. The relay is now a latency
  optimization, not a correctness dependency.

## [0.10.37] - 2026-08-15

### Fixed

- **SQL artifact store wires in for every distributed deployment**
  (#222) — @hc12r. Live-verifying v0.10.36's blob reclaim found it
  dormant: the SQL artifact store was gated behind ADR-017 instance
  dispatch (which nothing enables), so distributed deployments still
  ran local-disk artifacts — leaving worker disks growing AND resume
  broken by placement luck (a resumed run claims on any worker; the
  original run's artifacts lived on the original worker's own disk).
  Distributed mode now always uses the SQL store: cross-pod artifact
  reads work, and terminal blob reclaim engages on the deployment
  shape it was written for. Single-process mode keeps local disk.

## [0.10.36] - 2026-08-15

### Fixed

- **Everything that accumulates is now managed** (#217, #218, #219) —
  @hc12r. The production-readiness review's verdict was "everything
  that executes is solid; everything that accumulates is unmanaged."
  Three fixes close it:
  - **Lost queue deliveries recover** (#217, fixes half of #216): the
    dispatch outbox finally has its consumer — a sweep on every
    queue-wired instance re-enqueues pending runs whose committed
    outbox intent is older than a grace window. Live-verified gap: a
    Redis restart wiped the queue and stranded 5 accepted runs
    pending forever. Idempotent enqueue + the claim CAS make the
    sweep safe everywhere with no leader gating. The helm chart's
    Redis gains AOF + a PVC in brokoli-ee so the sweep is backstop,
    not routine.
  - **Transient blob scratch reclaimed at run-terminal** (#218, fixes
    #215): the 3-hour soak measured ~450MB/hour/worker of spill and
    reference-passing blobs nothing reclaimed. On SQL-artifact
    deployments those blobs are pure transport and are now deleted
    the moment a run finalizes (success, failed, or cancelled). On
    local-disk deployments the blobs ARE the artifacts and are
    deliberately untouched — the new TransientBlobJanitor capability
    encodes exactly that conditional.
  - **Scheduled run retention** (#219, fixes #214):
    BROKOLI_RUN_RETENTION_DAYS drives a 6-hourly purge that deletes
    expiring runs' artifacts first, then the rows (events and node
    runs cascade). Unset keeps runs forever — exactly the old
    behavior.

## [0.10.35] - 2026-08-14

### Fixed

- **The remaining cancel-loss windows, closed with durable intent**
  (#207) — @hc12r. Two windows where a cancel could still vanish
  after v0.10.34's relay: a claimed run waiting for a concurrency
  slot was not yet registered as cancellable (minutes-long window
  under saturation — precisely when users cancel things), and a
  relay message could be lost outright (fire-and-forget transport,
  or the owning process dying mid-cancel). The runner now registers
  before the semaphore wait, and a new runs.cancel_requested column
  records intent durably BEFORE any acting: the Runner re-checks it
  at every wave boundary (so cross-instance cancel now works even
  with no relay configured, at node-boundary latency) and recovery
  honors it when closing out a run with no recoverable path —
  cancelled, not recovery-failed. A too-late cancel never rewrites
  genuinely completed work. ADR-018 flipped to accepted in the same
  cycle (#209); remote-WorkOrder cancellation filed as #208.

## [0.10.34] - 2026-08-14

### Fixed

- **Run cancellation now works in distributed deployments** (#205) —
  @hc12r. Found live-verifying v0.10.33 on a real cluster: every
  cancel against the API pod returned 404 while the run kept
  executing on its worker — Engine.CancelRun only ever consulted its
  own process's active-runner map, so cancellation simply did not
  work in scheduler mode, and never had. Three fixes in one seam:
  the new extensions.RunCancelRelay (enterprise transport, mirrors
  JobQueue) broadcasts cancels to the instance that owns the run;
  the new store.PendingRunCanceller cancels queued, unclaimed runs
  with a compare-and-swap that ClaimPendingRun can never override
  (exactly one of claim and cancel wins); and Runner.Cancel is now
  durable across the pre-Execute window (previously a silent no-op
  against a nil cancel func — under semaphore saturation that window
  lasts minutes). Resolution order: local runner, pending
  compare-and-swap, relay broadcast, loud error. Single-process
  deployments behave exactly as before.

## [0.10.33] - 2026-08-14

### Fixed

- **Cancelling a run could record it as failed** (#203) — @hc12r.
  CancelRun and the runner's node-failure exit path raced on the
  run's terminal status: a cancel that killed an in-flight node made
  the node fail with "pipeline cancelled", and the runner then
  persisted the run as FAILED — plus a dead-letter-queue entry, a
  failure alert, and a critical notification — whenever its goroutine
  won the write race. Cancellation now wins over failure on every
  exit path. Found by TestExecutionContext_ObservesRunCancellation
  flaking about 1 in 5 full-suite runs; the test was right, the
  product lost the race.
- **Engine test teardown races** (#203) — @hc12r. 27 test sites
  across 13 files built engines without draining their background
  goroutines before store close and TempDir removal (the #94-family
  teardown flake). All now route through a shared
  drainEngineOnCleanup helper.

## [0.10.32] - 2026-08-14

### Fixed

- **add_column arithmetic silently stored expression text as data**
  (#201) — @hc12r. Found live verifying ADR-019 Milestone 2:
  "quantity * unit_price" put the literal STRING of the expression in
  every row, and a downstream aggregate summed it to zero with no
  error anywhere — since the rule's beginning, in pure batch.
  `*`/`/`/`-` now evaluate as arithmetic when every operand resolves
  numerically; div-by-zero and non-numeric column values yield nil
  (aggregates skip it); the `+`-concat and bare-word-literal
  behaviors are pinned and untouched, and hyphenated column names
  stay literals via the all-operands-numeric guard.

## [0.10.31] - 2026-08-14

### Added

- **Streaming hash aggregation (ADR-019 Milestone 2)** (#199) —
  @hc12r. An `aggregate` rule was a hard barrier — one anywhere in a
  transform forced the whole node to materialize its input. Rule
  lists now compile to a stream plan: row-local prefix streams per
  batch, the aggregate folds batches into per-group accumulator
  state, and suffix rules (anything, including sort) run on the
  grouped output. Incremental accumulators replicate the batch
  implementation's exact semantics, held to equivalence by test.
  Fan-out replay of ref outputs is pinned by test (it fell out of
  Milestone 1's per-consumer blob opens); concurrent cross-node
  stages are deferred with the reason recorded in ADR-019 (the
  re-iterable lazy-rows compat contract requires a seekable file — a
  FIFO would deadlock `len(rows)` — so pipelined subprocess input
  needs a future explicit per-script opt-in).

## [0.10.30] - 2026-08-14

### Added

- **Lazy rows, emit, and generator outputs in code nodes (ADR-019
  Milestone 1.5)** (#195) — @hc12r. Bounds the Python side of a code
  node, the residual peak Milestone 1 left behind. `rows` becomes a
  disk-backed, re-iterable lazy view in NDJSON mode: iteration
  streams in constant memory (double loops still work — each pass
  re-reads the file), while `len(rows)`/indexing transparently
  materialize for full backward compatibility. New `emit(row)` /
  `begin_emit(columns)` write output row-by-row with no output list
  at all, and generator `output_data["rows"]` becomes legal — a
  fully-streaming script holds near-zero Python memory. Input column
  order now travels from the ref via `BROKED_INPUT_COLUMNS`,
  preserving order that map keys never did.

### Fixed

- **Fault-injection tests leaked engine goroutines into teardown**
  (#195) — @hc12r. The CI flake that blocked two PRs: the fault-test
  harness never drained the engine's background goroutines (the
  missing "test half of #94"), so trigger-mode fan-out's in-flight
  SQLite connection recreated WAL files during `t.TempDir` removal —
  "TempDir RemoveAll cleanup: directory not empty". Both engine
  constructions in the file now close the engine before the store,
  before the directory.

## [0.10.29] - 2026-08-14

### Fixed

- **Stream engagement gets its own threshold** (#192, ADR-019) —
  @hc12r. Live measurement of v0.10.28 showed reference-passing never
  engaged for the benchmark workload: it was gated on the spill
  threshold (64 MiB encoded), while the datasets that OOM'd pods
  encode to ~15-20 MiB and cost 5-6x that as in-memory map rows.
  Spilling parks memory already paid; streaming prevents it being
  paid — different economics, different knob.
  `BROKOLI_STREAM_THRESHOLD_BYTES` (default 8 MiB encoded), negative
  disables reference-passing entirely.
- **Toolchain pinned to go1.25.13** (#192) — @hc12r. Seven newly
  published Go standard library vulnerabilities affect go1.25.12 and
  broke govulncheck CI on every branch; the toolchain directive makes
  the fix independent of what patch release the CI runner has cached.

## [0.10.28] - 2026-08-13

### Added

- **Reference-passing dataflow (ADR-019 Milestone 1)** (#190) —
  @hc12r. A stream-capable node whose input is held by blob reference
  processes it in bounded memory and hands its output back as a
  reference — code nodes go ref-to-ref (the engine no longer
  materializes their input or output datasets at all; the Python
  wrapper's NDJSON files are stream-copied to and from the blob store
  with rows counted in transit), and transforms whose rules are all
  row-local stream batch-wise without the whole-input clone. Engaged
  by the existing spill threshold: streaming is the completion of
  spilling — data large enough to spill now lives on disk its whole
  life instead of visiting memory first. Scheduling, retries,
  attempts, recovery, previews, and the ADR-010 resume-artifact
  contract are preserved exactly (artifacts for streamed outputs are
  a zero-copy manifest write via the new optional RefArtifactWriter
  capability where the store supports it). Blocking rules (sort,
  dedup, aggregate), multi-input nodes, expansion nodes, and dry runs
  keep the batch path unchanged, as ADR-019 barriers. Also fixes file
  mode's longstanding column-order loss via a wrapper sidecar.

## [0.10.27] - 2026-08-13

### Fixed

- **Per-run memory divisor sized from measurement, not a guess**
  (#186, ADR-018) — @hc12r. v0.10.26's adaptive concurrency default
  assumed 256 MiB per concurrent run; measuring a single 100k-row run
  directly against its pod's cgroup showed a ~284 MiB peak — two
  concurrent runs put ~570 MiB of largely live memory on a 512 MiB
  pod, past the ceiling where no GC setting helps, which is exactly
  why GOMEMLIMIT alone didn't stop the OOMs. The divisor becomes
  512 MiB: a 512 MiB pod runs one pipeline at a time, and demand
  beyond that surfaces as queue depth an autoscaler answers with
  more replicas.

## [0.10.26] - 2026-08-13

### Added

- **Resource defaults now adapt to the container's own memory limit**
  (#184, ADR-018) — @hc12r. Two changes, both no-ops on bare hosts:
  `GOMEMLIMIT` is auto-set to 90% of the cgroup limit when the
  operator hasn't set one (without it, Go's collector paces itself
  off GOGC alone with no idea a hard ceiling exists — on a small pod
  the runtime sails heap+garbage straight through the cgroup limit
  and the kernel OOM killer fires where an aggressive GC cycle would
  have sufficed); and `BROKOLI_MAX_CONCURRENT_RUNS`'s default becomes
  memory-derived, `clamp(limit/256Mi, 1, 4)` — a 512Mi pod takes 2
  concurrent runs instead of a hardcoded 4, letting demand surface as
  queue depth an autoscaler answers with more replicas rather than
  overcommitting one pod. Explicit `GOMEMLIMIT` /
  `BROKOLI_MAX_CONCURRENT_RUNS` values always win.

## [0.10.25] - 2026-08-13

### Fixed

- **Recovery's transition grace period was too narrow for real pod
  contention** (#181, ADR-018) — @hc12r. Found live installing KEDA
  and testing memory-aware admission control (#176/#178) together
  with elastic worker scaling (EE-ADR-008) for the first time — the
  first time this deployment genuinely had several concurrent runs
  sharing one worker pod under real CPU contention. A node transition
  under that contention took ~12s where an uncontended pod sees
  single-digit milliseconds, exceeding the original 10s grace period
  (#169) and reproducing the identical false-reclaim race a second
  time. Widened to match `reclaimSweepInterval` (20s) — the same "at
  most one extra sweep cycle" cost the original 10s already accepted
  for true orphan detection, measured against contested-pod behavior
  instead of an idealized quiet one.

## [0.10.24] - 2026-08-13

### Fixed

- **Memory-aware admission control missed a burst-admission race**
  (#178, ADR-018) — @hc12r. Found immediately by redeploying #176 and
  re-running the same concurrent stress test it was meant to fix: a
  cgroup memory reading is a snapshot of usage right now, and says
  nothing about a job that was just admitted but hasn't started
  allocating yet. A burst of simultaneous requests could still land
  two jobs on the same worker about a second apart — confirmed
  directly from worker logs, both starting within one second of each
  other on the same pod, which then OOMKilled shortly after (the same
  outcome as before the memory check existed). A 3-second settle
  delay since a worker's last successful admission now closes this,
  trading a small amount of throughput under a genuine burst for
  actually preventing it rather than only helping the slower,
  staggered-arrival case a bare headroom check already covered.

## [0.10.23] - 2026-08-13

### Added

- **Memory-aware admission control for `--mode worker`** (#176,
  ADR-018) — @hc12r. A worker no longer claims a new job from the
  distributed queue when its container is genuinely low on memory,
  checked via real cgroup v2/v1 usage against the container's own
  limit — not a predicted or configured concurrency count. Every
  existing admission control (`maxParallel`,
  `BROKOLI_MAX_CONCURRENT_RUNS`, `workerSlots`) bounded how many
  things ran, never how much memory they were allowed to use, which
  is the actual root cause behind the OOM findings #169/#171/#173
  already fixed the aftermath of. Fails open when cgroup limits can't
  be determined (bare host, most dev environments) — a strict
  addition to existing admission control, never a replacement.
  Escape hatch: `BROKOLI_MEMORY_BACKPRESSURE_DISABLED=1`.

### Fixed

- **`SQLArtifactStore` silently disabled node-level spilling** (#176,
  ADR-018) — @hc12r. `cmd/serve.go` swaps `eng.ArtifactStore` to
  `SQLArtifactStore` alongside instance-level remote dispatch — the
  deployment shape most likely to run large pipelines across real
  worker pods — and `SQLArtifactStore` didn't implement
  `BlobStoreProvider` at all, so every node's output stayed fully in
  memory regardless of size in that entire deployment mode, with no
  error or warning. Now backed by a local-disk blob store for
  intra-run spill scratch space, matching the default
  `LocalDiskArtifactStore`'s behavior.

## [0.10.22] - 2026-08-13

### Fixed

- **Recovery left a dangling `NodeRun` stuck "running" forever on
  runs it correctly marked failed** (#173) — @hc12r. Found
  live-testing #169/#171, re-running the same concurrent stress test
  once more to confirm them: `markRecoveryFailed` only ever touched
  the run row and appended a run-level event, so the specific node a
  "no recoverable path" determination was blamed on was left exactly
  as the dead process last wrote it — visible in the API/UI as a node
  stuck "running" indefinitely on a run whose own status was already
  terminal. Not a new regression from #169/#171; present since the
  original recovery feature, just newly surfaced by the same stress
  test. `closeDanglingNodeRun` now settles that node and appends a
  matching failure event, mirroring the normal failure path, so a
  recovered run's full history is consistently terminal end to end.

## [0.10.21] - 2026-08-13

### Fixed

- **Recovery's periodic sweep could defer a genuinely orphaned run
  forever** (#171) — @hc12r. Follow-up to #169, found immediately by
  redeploying it and re-running the same concurrent stress test.
  `recoverRun` unconditionally appends `RunEventRecoveryStarted` at
  the top of every call, before #169's grace-period check ever runs
  — including a pass that only ends up deferring — so reading the
  run's last event naively for recency always found *that same
  pass's own just-appended event*, which is by definition always
  fresh. A genuinely orphaned run therefore deferred forever, on
  every sweep tick, and was never actually reclaimed. Confirmed live:
  4 runs left mid-flight by an OOMKilled worker sat "running" with
  `since_last_event` under 5ms on every 20s sweep pass for several
  minutes straight. `lastGenuineActivity` now skips recovery's own
  event types when computing the recency signal.

## [0.10.20] - 2026-08-13

### Fixed

- **Recovery's periodic sweep could false-positive-orphan a live run mid
  node-transition** (#169) — @hc12r. `reconcileExecutionAttempts` only
  sees non-terminal `execution_attempts` rows, so the instant between
  one node going terminal and the Runner claiming the next node's
  attempt has no lease at all — not because the run is orphaned, but
  because nothing has asked for one yet. Before the periodic reclaim
  sweep (#167), `RecoverNonTerminalRuns` only ran once per process at
  startup, so this window was effectively never hit; a sweep ticking
  every 20s hits it routinely under real concurrent load. Found
  live-testing #167/brokoli-ee#85/brokoli-ee#86 against a real
  multi-worker deployment under a 10x-concurrent-run stress test: 7 of
  7 runs whose worker was OOMKilled mid-flight had a node permanently
  orphaned as "running" this way, on top of the run itself being
  correctly marked failed. A 10s grace period on the run's most recent
  event now defers this specific classification instead of immediately
  concluding orphaned — negligible added latency against a 20s sweep
  interval, and it does not affect genuinely orphaned runs, which stay
  reclaimed on the next pass once the grace period elapses.

## [0.10.19] - 2026-08-12

### Added

- **`extensions.JobQueueRenewer`** (#167) — @hc12r. Optional capability
  interface (`RenewClaim(jobID string) error`) a `JobQueue`
  implementation can support to keep a claim's visibility timer reset
  while genuinely still processing it. `cmd/serve.go` starts a 10s
  heartbeat goroutine alongside both whole-pipeline and `WorkOrder` job
  execution when the configured queue implements it. Fixes a real race
  found live-testing under sustained concurrent load: a Redis Streams
  consumer-group job whose processing legitimately exceeded the 30s
  visibility timeout had its claim silently reassigned via
  `XAUTOCLAIM` mid-flight, so the true owner's own eventual `Ack`
  failed with "job is not claimed" even though the work itself
  completed correctly.
- **Periodic reclaim sweep on the scheduler leader** (#167) — @hc12r.
  `RecoverNonTerminalRuns` was already idempotent and safe to call
  repeatedly, but was only ever called once, at process startup — so a
  run whose execution attempt died after startup (lease expired, no
  process left to complete it) stayed "running" forever with nothing to
  reclaim it. `Scheduler.runReclaimSweep` now calls it on a 20s ticker,
  gated on the same leader-election check `catchUpMissedRuns` already
  uses.
- **`engine.SQLArtifactStore`** (#161, #167) — @hc12r. A Postgres/SQLite-
  backed `ArtifactStore`, replacing the default local-disk store when
  remote instance dispatch is enabled. Found live: a dynamic-expansion
  instance dispatched to a remote worker pod wrote its result artifact
  to that pod's own local disk, invisible to the dispatcher pod reading
  it back, so the run failed with "result could not be read back" even
  though the worker finished successfully. Uses the same database every
  pod in a distributed deployment already connects to — no new
  infrastructure required.

## [0.10.18] - 2026-08-12

### Changed

- **Remote dynamic-expansion instances now dispatch concurrently** (#164,
  ADR-017) — @hc12r. Found under real load in a Kubernetes deployment: a
  30-item expansion node with 6 idle worker pods still ran no faster
  than one worker, because instances were dispatched strictly one at a
  time, waiting for each to fully settle before starting the next.
  Remote dispatch (`Engine.InstanceJobQueue` set) now fans out up to 16
  instances at once; local execution is unchanged. No config needed —
  existing opt-in behavior via `BROKOLI_INSTANCE_DISPATCH=1` (#158) is
  unaffected.

## [0.10.17] - 2026-08-12

### Added

- **ADR-017 dispatcher-side wait-and-fetch loop** (#155) — @hc12r. The
  engine can now enqueue a dynamic-expansion instance for remote
  execution and wait for its result, instead of always running it
  in-process — `Engine.InstanceJobQueue`, opt-in, nil by default
  everywhere, zero behavior change for existing deployments.
- **ADR-017 worker-side execution of a `WorkOrder`** (#156) — @hc12r.
  `ExecuteInstanceWorkOrder`/`ExecuteInstanceJob` let a worker sharing
  the dispatcher's store actually run a dispatched instance and settle
  its claim; wired into `cmd/serve.go`'s `--mode worker` loop, the first
  real consumer.
- **`Engine.InstanceJobQueue` now actually wired in `cmd/serve.go`**
  (#158) — @hc12r. #155/#156 shipped the mechanism, but nothing wired
  the dispatcher side into either core's own binary or the enterprise
  binary (which reuses this same command) — found while testing the
  mechanism end-to-end against a real deployment, not by any test in
  the original PRs. Opt-in via `BROKOLI_INSTANCE_DISPATCH=1`.
- **`extensions.RunJob.DeliveryCount`** (#159) — @hc12r. Separates the
  queue-transport's own delivery/redelivery counter from `Attempt`
  (which mirrors `models.ExecutionAttempt.Attempt`, the node/instance
  execution-attempt identity a `WorkOrder`-bearing job settles under).
  Found live-testing ADR-017 in Kubernetes: the enterprise Redis-backed
  `JobQueue` was overwriting `Attempt` with its own delivery count on
  every dequeue, which silently broke every remote instance dispatch —
  no test caught it because every earlier test used a simplified
  fake/channel queue that never exercised real redelivery bookkeeping.

## [0.10.16] - 2026-08-12

### Added

- **Dynamic-expansion items get durable claim/lease/fencing** (#149,
  ADR-017) — @hc12r. Extends #144's node-level wiring down to instance
  granularity: each expansion item now gets its own claim/lease/fencing
  record, generalizing #145's in-memory-only retry-skip into the same
  durable mechanism nodes already have. Same-process only.
- **ADR-017's claim-payload work-description envelope** (#150) —
  @hc12r. `extensions.RunJob` gains `InstanceKey` and a `WorkOrder`
  field (`InstanceWorkOrder`): node type, script, config, the instance's
  input row, run params, timeout — what a remote claimant would need to
  execute one physical instance. Purely additive; nothing populates it
  yet.
- **ADR-017 output-reference delivery design** (#151) — @hc12r. Answers
  how a completed remote instance's output gets back: push not pull,
  `ArtifactStore` gains an instance dimension, `ExecutionAttempt` stays
  control-plane only, a worker→server endpoint modeled on the existing
  checkpoint-forwarding handlers, reusing ADR-012's inline-vs-spill
  threshold.
- **`ArtifactStore` gains instance-level identity** (#152, ADR-017) —
  @hc12r. Key widens from `(run_id, node_id)` to `(run_id, node_id,
  instance_key)`, byte-identical paths for the empty-instance-key case
  (every existing artifact stays readable), purely additive.

## [0.10.15] - 2026-08-12

### Added

- **Node-level execution attempts now go through claim/lease/fencing**
  (#144, #90 M3) — @hc12r. `store.ExecutionAttemptStore`'s claim/lease/
  fencing contract existed but had no production caller below the
  pipeline-level outbox row; node dispatch now creates, claims, acks, and
  settles its own attempt row with a short renewed lease
  (`store.DefaultLeaseDuration`/`RenewInterval`), so a crashed process's
  lease becomes reclaimable within seconds instead of being deferred to
  for however long the node's own configured timeout is. Optional
  capability throughout — a store that doesn't implement
  `ExecutionAttemptStore` is unaffected.
- **Dynamic-expansion node retries skip already-succeeded siblings**
  (#145, #90 M3) — @hc12r. A retry of a `.expand()` node used to
  re-execute every item from scratch, even ones that had already
  succeeded before a later sibling failed. Now only items that failed or
  were never reached run again; reused items still get their own
  `models.ExpansionInstance` row for the new attempt, so per-attempt
  history stays complete.
- **ADR-017 (proposed): worker protocol v2, instance-level dispatch**
  (#146) — @hc12r. Scopes moving the unit of worker claim from a whole
  pipeline run to a physical instance (node/page/expansion-item) — what
  #90 M3's acceptance gate (a worker killed mid-instance, fenced out, its
  instance retried by a different worker without disturbing successful
  siblings) actually needs. Status proposed; implementation gated on
  co-design review.
- **`execution_attempts`' claim key gains an instance dimension** (#147,
  ADR-017 slice 1) — @hc12r. `(run_id, node_id, attempt)` widens to
  `(run_id, node_id, instance_key, attempt)`, matching the physical-plan
  instance-identity scheme, so a pagination page or expansion item will
  be able to claim/lease/fence independently of its siblings instead of
  colliding with them. Purely additive — every existing caller passes an
  empty instance key and is unaffected. Migrates a live database in
  place on both backends.

## [0.10.14] - 2026-08-12

### Added

- **`sink_db`/`migrate` generate their own write SQL** (#135, #136,
  brokoli-sdk#12) — @hc12r. `source -> sink_db(table, mode)` now works on
  its own — no upstream `sql_generate` node needed. Both `sink_db` and
  `migrate` support `append`/`overwrite`/`upsert` (upsert keyed on
  `key_columns`; `ON CONFLICT` on Postgres/SQLite, `ON DUPLICATE KEY
  UPDATE` on MySQL), generating the SQL from the input rows directly. The
  older `sql_generate -> sink_db` pattern still works unchanged.
- **`filter_rows` comparison operators** (#133, brokoli-sdk#14) —
  @hc12r. `filter_rows` conditions now support `>`, `<`, `>=`, `<=` (in
  addition to the existing `=`/`==`/`!=`/`in [...]`), with numeric
  comparison when both sides parse as numbers and lexicographic
  otherwise. An unrecognized condition is now a run-time error, not a
  silent keep-all-rows pass-through.
- **`sink_api` resolves `conn_id`, matching `source_api`** (#139, closes
  #137) — @hc12r. The connection resolver had no case for `sink_api`, so
  a `conn_id` on that node type was silently ignored — no base URL, no
  headers, no Basic Auth ever got injected. It now resolves identically
  to `source_api` (relative/absolute URL, merged headers, Basic Auth).
- **`retry_backoff` actually selects a backoff strategy** (#140, closes
  #138) — @hc12r. The retry loop hardcoded exponential backoff and never
  read the `retry_backoff` config key. `"linear"` and `"fixed"` now work
  as documented, alongside the existing default `"exponential"`.
- **`GET /api/plugins` carries each plugin's payloads** (#141, #110 M3)
  — @hc12r. The list response now includes each plugin's
  runtime/os/arch/requires payload list, so a caller can resolve its own
  feasibility (`plugins.SelectPayload`) without downloading the archive
  first — the data prerequisite for per-worker plugin availability.

### Fixed

- **Physical-plan e2e test flake under parallel load** (#134) — @hc12r.
  Inline `NewEngine(s).RunPipeline(...)` calls that never closed the
  engine left background event goroutines writing to the store after the
  test returned, occasionally landing a write during `t.TempDir()`
  cleanup (`directory not empty`). Test-only fix; no production change.

## [0.10.13] - 2026-08-11

Full curated notes with per-change ownership:
[v0.10.13 release](https://github.com/Tnsor-Labs/brokoli/releases/tag/v0.10.13).

### Added

- **Plugin distribution UI and curated index** (#126, #129, #130, #131) —
  @hc12r. Plugins are now installable without the CLI: a Plugins page lists
  installed plugins, uploads a `.bkg`, and removes; a curated static index
  (`GET /api/plugins/index`, `BROKOLI_PLUGIN_INDEX` override, ADR-016 §5)
  is browsable and installable by name (`POST /api/plugins/index/{name}`),
  with the archive's sha256 verified against the index digest before any
  bytes reach the installer — no plugin executes without a digest match.
  Install/remove errors are surfaced verbatim, so a wrong-platform or
  tampered package shows the server's named reason.
- **Capability advertising for plugin packaging** (#127) — @hc12r.
  `GET /api/capabilities` now reports `supported_packaging_versions` and
  `supported_runtime_classes` (native, python, node, jvm), so clients can
  tell what a deployment understands before uploading — the same
  negotiate-before-you-act contract as `supported_ir_versions`.
- **Durable physical run instances** (#123, #125, ADR-015 §8, #90) —
  @hc12r. A run's physical instances (one per plain node, one per resolved
  expansion item) are projected at `GET /api/runs/{id}/instances` and, once
  a run records them, persisted in a durable `physical_instances` table
  (read durable-first with a projection fallback for older runs). A run now
  answers both what it *planned* (`/plan`) and what it *ran* (`/instances`)
  in the physical model.

### Fixed

- **Plugin runner SIGTERM grace on the spec path** (#128) — @hc12r. The
  install-time `spec` probe now applies the same SIGTERM-then-SIGKILL grace
  as the main run path (`Cancel` + `WaitDelay`) instead of killing a hung
  plugin immediately with no chance to shut down cleanly.
- **SODP dashboard eviction test no longer flakes after midnight** (#124) —
  @hc12r. The test derives its expected day buckets from the seeded
  timestamp rather than the wall clock.

## [0.10.12] - 2026-08-11

Full curated notes with per-change ownership:
[v0.10.12 release](https://github.com/Tnsor-Labs/brokoli/releases/tag/v0.10.12).

### Added

- **Plugin package format and management API** (#117, #118) — @hc12r.
  Plugins ship as a single `.bkg` archive (manifest + per-platform/runtime
  payloads) that installs on any supported host, or fails at install with
  a named reason: payload selection by os/arch and runtime (native,
  python, node, jvm with version checks), mandatory integrity hash, and a
  `spec` drift check — nothing lands before all three pass.
  `GET/POST/DELETE /api/plugins` and `GET /api/plugins/{name}/archive`
  install (from an uploaded archive), list, remove, and serve plugins;
  install and remove hot-reload the node-type registry without a restart.
  ADR-016.
- **Physical execution plans** (#119, #120, #121) — @hc12r. The authored
  logical pipeline and the per-run physical plan are now distinct: a
  planner expands the graph into scheduled stages and work units
  (pagination page-groups, dynamic-expansion fan-out) with deterministic
  instance keys and honest runtime-resolved counts. `GET
  /pipelines/{id}/plan` explains the plan before execution; every run
  persists its plan snapshot (`GET /runs/{id}/plan`) so recovery and
  audit see the plan as-decided, not a recomputation. Foundation for
  independently scheduled, retryable physical instances. ADR-015.

### Changed

- **The `Store` interface is composed from focused capability
  interfaces** (#120) — @hc12r. The 100+-method contract is now
  `PipelineStore`, `RunStore`, `AlertStore`, and the rest, embedded into
  `Store`. Pure refactor — no behavior change — but a new capability is
  added as its own interface instead of force-breaking every
  implementation, and a consumer can depend on the narrow slice it needs.

## [0.10.11] - 2026-08-10

Full curated notes with per-change ownership:
[v0.10.11 release](https://github.com/Tnsor-Labs/brokoli/releases/tag/v0.10.11).

### Added

- **Stateless validation** (#114) — @hc12r. `POST /api/pipelines/validate`
  runs full structural + executable validation on a request-body IR
  document without persisting anything.
- **`supported_execution_features` on `/api/capabilities`** (#115) —
  @hc12r. The list of pipeline semantics this host can actually
  *execute* — `conditional-routing`, `dynamic-expansion`, `union`,
  `pagination-checkpoints` — with an honesty rule: a feature is listed
  only when a pipeline using it runs. SDK preflight gates on it, so
  compile-only features are refused at deploy instead of failing at run
  time.
- **Cross-model contract fixtures** (#115) — @hc12r. A real SDK-compiled
  IR 2.1 payload is validated against the canonical schema on every test
  run; the SDK repo validates its emissions against a vendored schema
  copy, so contract drift in either direction fails a suite naming the
  payload.

### Changed

- **Unknown top-level fields are rejected** (#114) — @hc12r. Create,
  update, and JSON import decode fail-closed: an unrecognized field
  returns `400` naming it. Forward-compatible payloads belong under the
  new `extensions` namespace, which is persisted in both stores and
  echoed back uninterpreted. **Breaking** for clients that sent junk
  fields the server used to silently drop.
- **The JSON→YAML import fallback is gone** (#114) — @hc12r. A JSON type
  mismatch on `/api/pipelines/import` used to fall through to the YAML
  parser (YAML parses JSON) and return `201` with a silently mutilated
  pipeline — missing capabilities, params, hooks, SLA and dependency
  fields. A JSON body now gets the JSON path, full stop.
- Prose-only docs changes (Markdown, ADRs, RFCs) skip the Go CI pipeline
  (#113) — @hc12r; `docs/schema/**` deliberately still runs everything.

## [0.10.10] - 2026-08-10

Full curated notes with per-change ownership:
[v0.10.10 release](https://github.com/Tnsor-Labs/brokoli/releases/tag/v0.10.10).

### Added

- **Plugin subprocess cancellation** (#100) — @JefferMarcelino. Cancelling
  a run or hitting a node timeout now terminates the running plugin
  process: `SIGTERM`, then `SIGKILL` after the grace window (killed
  directly on non-Unix). Plugin-backed nodes no longer run to completion
  after a cancel. ADR-013 M2.
- **Canonical pipeline IR schema** (#111) — @hc12r.
  `docs/schema/pipeline-ir-2.1.json` is the machine-readable statement of
  the wire contract (ADR-014), bound to `models.Pipeline` by two-way
  contract tests: model/schema drift now fails CI naming the exact
  property. Adds `github.com/santhosh-tekuri/jsonschema/v6` (Apache-2.0).

### Changed

- **Persistence is fail-closed** (#106) — @hc12r. Create, update, import,
  clone, and rollback run full executable validation before anything is
  written: unknown node types are rejected with `400 … has unsupported
  type` instead of executing as silent successful no-ops, cycles and
  missing required config are caught at save time, and rolling back to a
  no-longer-valid snapshot is refused. Updates that leave the graph
  byte-identical (pause, rename, schedule) skip executable validation so
  legacy rows stay operable. The editor's Run/Preview/Check flows now
  bail out loudly when a save is rejected instead of silently executing
  the previously persisted graph, and the zero-node Blank template is no
  longer listed as creatable. Dry-run responses carry an `error` field
  alongside partial results.

### Fixed

- **Pipeline hooks persist** (#111) — @hc12r. `hooks` was decoded, echoed
  on the create response, and silently dropped by the store — gone on the
  next read. Both SQLite and Postgres now persist it through create,
  update, and every read path.
- **Dashboard day counts bucket in one timezone** (#108) — @hc12r.
  `runs_today` compared a local date against UTC-stored timestamps, so
  non-UTC servers miscounted for the first hours of every local day (and
  the corresponding test flaked after midnight).

### Roadmap

- **ADR-016 proposed: plugin packaging, runtime resolution, and
  distribution** (#105) — @hc12r. One published archive with per-payload
  runtime classes, install-time feasibility resolution, an install
  API/UI, worker fetch-by-digest, and a curated index. Discussion: #104;
  implementation tracker: #110.

## [0.10.9] - 2026-08-09

Full curated notes with per-change ownership:
[v0.10.9 release](https://github.com/Tnsor-Labs/brokoli/releases/tag/v0.10.9).

### Added

- **Conditional edge routing — IR 2.1** (#97, #101) — @hc12r. Condition
  nodes route true/false branches as first-class edges: skipped branches
  propagate transitively, convergence nodes wait for all inputs to
  resolve, and routing decisions, skipped outcomes, and resume lineage
  persist atomically across SQLite, PostgreSQL, JSON, YAML, clone, and
  version snapshots. #101 turned it on: the server now advertises IR 2.1
  in `supported_ir_versions` (while `CurrentIRVersion` stays 2.0),
  rejects malformed conditional graphs and unsupported expressions
  before persistence, and the UI authors branch metadata and renders
  skipped node states.

### Changed

- **SLA fields are labeled as an enterprise capability** (#98) — @hc12r.
  The deadline/timezone fields are stored by every edition but only
  monitored by an enterprise deployment; the editor now says so with a
  tag and tooltip instead of letting a community install configure a
  deadline nothing watches.

### Fixed

- **Connection Basic Auth applies on the read path, not only on writes**
  (#93) — @hc12r. `source_api` reads — the first fetch and every
  paginated page — now carry connection credentials, which were
  previously silently dropped on reads while the same connection worked
  on a sink. Deliberate precedence: connection credentials win over a
  handwritten `Authorization` header.
- **The engine can be shut down** (#96) — @hc12r. `Engine.Close(ctx)`
  refuses new dispatch, stops starting background work, and waits —
  bounded by the caller's context — for every goroutine the engine owns.
  Ends `database is closed` logs at shutdown and the `TempDir RemoveAll`
  CI failures caused by orphaned fan-out goroutines.
- **`GET /api/capabilities` is public again** (#102) — @hc12r. Exact
  anonymous capabilities discovery now passes the API-key, JWT/open-mode,
  workspace, and enterprise SSO auth layers, with per-layer regression
  coverage; every other API method and path stays protected. Unblocks
  SDK capability preflight on authenticated deployments.

## [0.10.8] - 2026-08-09

Full curated notes with per-change ownership:
[v0.10.8 release](https://github.com/Tnsor-Labs/brokoli/releases/tag/v0.10.8).

### Added

- **The artifact plane** (#85, #88, #89) — @hc12r. `pkg/artifact`:
  `ArtifactRef`/`DatasetRef` with a versioned manifest and a
  content-addressed local store; `response="artifact"` returns a real
  reference; node outputs above `BROKOLI_SPILL_THRESHOLD_BYTES` (default
  64 MiB) spill automatically. ADR-012, updated per milestone.
- **Persisted alert inbox, run events over HTTP, org-wide DLQ** (#80) —
  @hc12r.
- **Connector protocol `MsgProgress`** (#66) — @JefferMarcelino. First
  milestone of ADR-013, and the author's first shipped change. 🎉
- **Keyset pagination for run history** (#84) — @hc12r. `?after=`/`?limit=`
  cursors; `?page=` now returns real rows and a real total.
- **`union`/`dataset_map`/`dataset_filter` node types, executable
  `.expand()` fan-out** (#34, #35) — @hc12r.
- **source_api execution contract**: response contracts and pagination
  execute (#33); bounded page fan-out (#43); persisted, resumable
  checkpoints (#45, #53); page-retry config keys (#48) — @hc12r.
- **Database-backed, admin-editable pipeline templates** (#71, #72) —
  @hc12r.

### Changed

- **Dashboard rebuilt as a triage console**; light theme rebuilt on
  surface tones; dashboard figures read from the database instead of
  evicted live state (#83, #81) — @hc12r.
- **`response="artifact"` no longer inlines the body** — the output row
  is `uri`/`media_type`/`size_bytes`/`checksum` (#88) — @hc12r.
- Plugin node-type gating runs before dispatch (#65); manifest kinds
  propagate into node capabilities (#63) — @hc12r.

### Fixed

- `runs.org_id` populated at creation, legacy rows backfilled (#54);
  artifact/checkpoint stores wired into purge (#51); empty JSON array is
  zero records (#46); sample data 404 (#68); SODP presence cleanup (#56);
  `PORT` env synced with `--port` (#69); Data Quality template config
  (#70) — @hc12r.

> Releases 0.7.2 through 0.10.7 predate this file's revival; their
> histories live in the
> [GitHub releases](https://github.com/Tnsor-Labs/brokoli/releases).

## [0.7.1] - 2026-04-13

### Fixed

- **SSRF guard no longer blocks worker self-references.** `pkg/fetchers`
  rejected relative `/api/samples/...` URLs once they were resolved against
  `BROKOLI_SERVER_URL`, because in k8s/docker deployments the server
  hostname resolves to a private cluster IP (10.x / 172.16.x / ClusterIP) —
  which is exactly what `isBlockedHost` rejects. Workers running in a
  separate pod couldn't fetch sample data from the API they were already
  authenticated to. The fix tracks whether a URL originated as a relative
  path (an implicit trusted self-reference to the Brokoli server) and skips
  the SSRF check in that case only. Absolute URLs with private IPs are
  still blocked. Also removes a dead `HasPrefix` check that ran after the
  URL had already been rewritten to `http://...` form.

## [0.7.0]

### Added

- **SODP realtime stack** — `/api/ws` now speaks the State-Oriented Data
  Protocol ([orkestri/SODP](https://github.com/orkestri/SODP)) instead of a
  hand-rolled JSON event hub. Binary msgpack framing, per-key subscriptions,
  delta fanout, and reconnect-resilient via the protocol's `RESUME` frame.
  See [`docs/realtime.md`](docs/realtime.md) for the architecture, key model,
  wire format, and rate limits.
- **`pkg/sodp`** — new in-process Go implementation of SODP v0.1: versioned
  state store with ring-buffer delta log, per-org tenant-isolated fanout bus,
  per-session rate limiting and watch caps, JWT auth, eviction goroutine for
  completed run state. 50+ unit tests, 12 integration tests against a real
  WebSocket, and a cross-language test that runs `@sodp/client@0.2.1` from
  Node.js against the live server.
- **`api/eventbus_bridge_test.go`** — distributed-mode test: worker publishes
  to `EventBus` (in-memory in OSS, Redis pub/sub in enterprise), API pod
  subscribes and forwards events into the SODP fanout. The bus → bridge → SODP
  path is the only way events cross pod boundaries in distributed deployments.
- **`api/ws_jwt_context_test.go`** — regression test for the JWT-on-WebSocket
  context-propagation fix below.
- **Calendar redesign** — replaced the previous flat-grid heatmap with a
  GitHub-style horizontal layout (weeks as columns, days as rows). Each day
  cell shows a vertical color split sized by the actual success / running /
  failed proportions instead of going solid red on any failure. Activity
  intensity is log-scaled relative to the busiest day in the range. Includes
  a sparkline trend strip, muted data-viz palette local to the page, and a
  proportional detail bar with per-day success rate.

### Changed

- **WebSocket protocol on `/api/ws` is now binary msgpack, not JSON.** Any
  external tooling that opened `/api/ws` and parsed JSON event objects must
  switch to a SODP client. Reference clients available for TypeScript
  (`@sodp/client`), Python (`sodp`), and Java (`io.sodp:sodp-client`), or
  speak the wire format directly using the schemas documented in
  `docs/realtime.md`. The Brokoli UI handles this transparently via
  `ui/src/lib/ws.ts` — no UI-side migration required when upgrading.
- **`@sodp/client@^0.2.0`** is now a runtime UI dependency. Pinned in
  `ui/package.json`. Self-hosters who build the UI from source will pull it
  automatically via `npm install`.
- **`api.Hub` removed.** The pre-SODP `Hub` type and `handlers_ws.go` were
  deleted. `RegisterRoutes` now takes `*sodp.Server` instead of `*Hub`.
  Internal callers only.
- **`bridgeCh` buffer is 512 events.** Engine events that overflow it are
  dropped with a warning log line, instead of stalling the engine's event
  channel. This is a deliberate backpressure choice for high-frequency log
  events; the cap is sized for typical workloads but is not yet load-tested
  at thousands of events per second.
- **Calendar palette is now muted** (forest sage / terracotta / steel blue
  inspired by Observable's data-viz palettes) for large color regions where
  the global bright `--success`/`--failed` were "hot." The global theme is
  unchanged — every other status indicator across the app still uses the
  vivid colors where high saturation reads correctly at small sizes.

### Fixed

- **Multi-tenant isolation collapsed on the WebSocket path.** The HTTP
  `JWTAuth` middleware validated tokens on `/api/ws` upgrades but did not
  propagate `claims` or `org_id` into the request context. The SODP server
  read claims from the context to enforce per-session tenant filtering, found
  nothing, and treated every authenticated WebSocket session as the default
  org — meaning users from `org=acme` would receive realtime events from
  `org=widgets` and vice versa. The non-WebSocket branch of the same
  middleware was correct; the WebSocket branch is now aligned with it.
  `api/ws_jwt_context_test.go` locks this in as a regression test.
- **`Pipelines.svelte` run counts reset to 1 after each WebSocket event.**
  Two stacked bugs: the WS event handler replaced the cached pipeline-runs
  entry with a fresh single-element list missing the `_total/_success/...`
  counter fields; and the Run-button click handler did its own optimistic
  REST refetch that wiped the same fields a millisecond earlier. The handler
  now mutates the cached counters in place and tracks `seenStarted` run IDs
  to handle late-arriving terminal events; the click handler no longer
  refetches.
- **`Connections.svelte` and `Variables.svelte` empty-state CTAs were
  no-ops.** Both pages set a phantom `showCreateModal` variable that didn't
  exist; the actual modal state is `showModal`. The CTAs now call the proper
  `openCreate()` handler that initializes the form and opens the modal.
- **`RunIndicator` panel overflowed off-screen.** The expanded "Recent Runs"
  panel was anchored to the floating button's `left: 0`, so a 300px-wide
  panel attached to a button at `right: 24px` clipped past the viewport. Now
  anchored to `right: 0` so it expands leftward into the page.
- **WebSocket connection burned through retries before login.** `App.svelte`
  created the SODP client unconditionally on mount, before the user had
  authenticated. The client hit `503` (open mode) or `401` (locked mode) for
  the first 4-8 attempts and then sat in a 30-second backoff loop, missing
  events that fired immediately after login. Connection lifecycle is now
  driven reactively from `$authUser`: open on login, close on logout. No
  retries are wasted against an unauthenticated session.
- **`DecodeFrame` rejected bodyless frames.** Heartbeat and ack frames with
  a nil body decoded into an empty `msgpack.RawMessage`, which the body
  decoder treated as EOF. `DecodeFrame` now treats `len(raw[3]) == 0` as a
  nil body explicitly.
- **`sodp.Server` had a concurrent-write race on the WebSocket connection.**
  Handler responses (HELLO, STATE_INIT, RESULT, ERROR) wrote directly to the
  connection while the write-pump goroutine also wrote, which `gorilla/websocket`
  doesn't support. All handler responses now go through the session's `Send`
  channel; the write pump is the sole writer to the connection.

### Security

- The WebSocket JWT-context fix above is a security fix in addition to a
  correctness fix. The data leak only manifested in multi-tenant deployments
  with multiple orgs, but in those deployments it was complete: every realtime
  event visible to any org was visible to every other org. Single-tenant /
  community-mode deployments were never affected.

---

## Prior history

This file starts with the SODP migration. For commit-level history before
this point see `git log` — the project did not maintain a CHANGELOG previously.
