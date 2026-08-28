# ADR-027: ClickHouse as the third backend, append-first

**Status:** accepted — implemented; phases 0–4 landed (#382), pushdown refused on measurement (see Deferred)
**Date:** 2026-08-28

## Context

ADR-024 turned "add a database backend" from finding thirty-five branch
points into implementing an interface, and MySQL proved the climb: dialect,
types, bulk write, and a set of refusals each earned by a differential test
against a live server. The question is which backend climbs next, and
ClickHouse is the strongest candidate for three reasons.

**The constraint that deferred Oracle does not apply.** The release is one
`CGO_ENABLED=0` binary cross-compiled to six targets, which is why
`modernc.org/sqlite` was chosen over the CGO driver and why ADR-024 records
Oracle as deferred (godror needs CGO plus Instant Client). ClickHouse's
official driver, `github.com/ClickHouse/clickhouse-go/v2`, is pure Go over
the native TCP protocol, Apache-2.0, and implements `database/sql`. It fits
the release as it is. (The same test rules out DuckDB for now: it is
embedded C++ with no pure-Go driver, so it sits with Oracle in the deferred
column, and its popular dbt route is a separate ADR-025 question because
dbt-duckdb's output is a file rather than a warehouse.)

**The workload fits what Brokoli's write path just became.** The recent
write-path arc (#376, #377) made appends the fast, bounded-memory path on
both existing backends. ClickHouse is an append-oriented analytical store —
bulk insert over the native protocol is its design center — so the
capability it is best at is the one our `BulkWriter` interface models, and
the capability it lacks (upsert) is one our model refuses by name rather
than fakes.

**It is a chance to not repeat the catalog's standing dishonesty.** The
connection catalog advertises fifteen types while three drivers are
compiled in, and the failure shape depends on which dead entry you pick:
`snowflake://` and `mssql://` at least refuse by name ("no driver is
compiled in"), while `oracle://`, `bigquery://` and `databricks://` fall
through `detectDriver`'s `default:` into the Postgres driver and fail with
an error about pgx -- the wrong backend, named confidently. (That default
was flagged in ADR-024's survey and remains unfixed.) ClickHouse must not
enter the catalog in either broken shape, and this ADR states the rule
that prevents it.

### What is genuinely different about ClickHouse

The first two backends are row-store OLTP databases that agree on the
basics. ClickHouse does not, and the differences below are load-bearing —
each one either shapes a decision in this ADR or lands in the deferred
list with its reason.

- **No transactions.** There is no `BEGIN`/`COMMIT`/rollback. An insert is
  atomic per block into a single partition, and nothing larger is. Both
  existing bulk writers open a transaction and rely on it: Postgres rolls
  a failed create-and-load back to nothing, and ADR-023's resume rule —
  "inside a segment the commit is the artifact" — leans on the same
  property. A ClickHouse writer has no such transaction to open.
- **No upsert.** There is no `ON CONFLICT` / `ON DUPLICATE KEY`. The
  native alternatives are different animals: `ReplacingMergeTree`
  deduplicates *eventually*, at merge time, with query-time `FINAL` to
  force it; `ALTER TABLE ... UPDATE` is an asynchronous mutation that
  rewrites parts. Neither is the synchronous merge `mode: upsert`
  promises.
- **Columns are non-nullable by default** — the inverse of every backend
  we have. A missing value in a non-`Nullable` column becomes the type's
  default (empty string, zero), not NULL. Carrying a nullable source
  column to ClickHouse without rendering `Nullable(T)` silently converts
  NULLs into zeros, which is exactly the class of quiet corruption #360
  exists to prevent.
- **`CREATE TABLE` requires an `ENGINE` clause.** There is no neutral
  default in the language; `create_table` cannot render DDL without
  choosing one.
- **Wrapped type names.** The driver reports `DatabaseTypeName` spellings
  like `Nullable(Int64)` and `LowCardinality(String)` — the type reader
  must unwrap before classifying, a shape neither pgx nor go-sql-driver
  prepared us for.
- **No server-side affected count for inserts.** The native batch reports
  success; the row count the client knows is the count it appended.
  ADR-023 requires a path to have an honest count source before it is
  eligible; "the rows I sent, given the send succeeded" needs to be
  argued, not assumed.

## Decision

**ClickHouse becomes the third `pkg/dbdialect` backend, append-first: the
required `Dialect` surface plus `BulkWriter` via the native batch protocol,
with upsert refused by name, overwrite arriving only with its weaker
atomicity documented, and pushdown starting fully refused. It enters the
connection catalog only in the same PR that makes `source_db` actually work
against it.**

Every capability follows ADR-024's rule: claimed only when a differential
test proves it against a live server over the shared hostile-value corpus,
refusals carrying reasons that are themselves tests.

### The decisions the differences force

**Atomicity: say what is true, per backend.** `SQLGenConfig.CreateTable`
already documents two truths (Postgres rolls the creation back, MySQL
leaves the empty table). ClickHouse adds a third and weaker one: no write
of any mode is transactional, a failed load can leave a prefix of the rows,
and an interrupted overwrite can leave the table empty. This is documented
on the config and in the connection docs rather than papered over — an
operator choosing an OLAP store has chosen these semantics, and pretending
otherwise would make ADR-023's resume rule a lie. Consequence for resume: a
ClickHouse sink segment is not a "commit is the artifact" segment; a resume
that cannot prove the write completed re-runs it, and on an append that can
duplicate rows. The docs say so, and the mitigation (idempotent runs via
overwrite, or downstream dedup) is named rather than implied.

**Upsert: refuse by name, and do not counterfeit it with
`ReplacingMergeTree`.** A `mode: upsert` sink against ClickHouse fails
validation with a message naming the backend, the reason, and the native
alternatives. Mapping upsert onto a `ReplacingMergeTree` insert would
"work" in demos and lie in production: readers see duplicates until an
unscheduled merge collapses them, which is not what `mode: upsert` means on
the other backends, and the equivalence test the capability would need can
never pass. The refusal doc points at ReplacingMergeTree for users who want
eventual dedup and know it.

**`create_table`: a default engine, stated and overridable.** DDL renders
`ENGINE = MergeTree ORDER BY tuple()` — valid for any schema, no ordering
assumption smuggled in — with the engine clause overridable via node config
for anyone who knows their sort key. The generated DDL is logged, as the
#362 path already does, so the choice is visible. Nullability renders
explicitly: a nullable column becomes `Nullable(T)`; a NOT NULL column
becomes bare `T`. The type renderer refuses, by name, any canonical type it
cannot hold faithfully — the same rule as #362, with ClickHouse's
`Decimal` cap (76 digits) and zone handling (`DateTime64` precision, named
zones) worked out in implementation against the live server rather than
asserted here.

**Counts: the appended count, with the failure mode stated.** The bulk
writer reports the number of rows it appended across successfully-sent
batches. That is honest for the same reason the client-side count is honest
for a completed COPY — the server acknowledged the batches — and its
weakness is the atomicity one already documented: on a mid-write failure
the count reflects what was attempted, not what the server durably holds,
and the run fails rather than reporting it as success.

**Catalog honesty: type and capability arrive together.** `clickhouse`
is added to `models.ConnectionType`, the URI builder, `DetectDriver`, and
the UI's connection form in the same PR that makes `source_db` read from it
— never before. The connection test must run a real query
(`SELECT 1` over the driver), not a TCP dial. This is the rule the five
dead catalog entries broke; it is stated here so the next backend cannot
break it either.

### Phases

Each phase is a PR (or few) gated on preflight plus live differential
tests; the capability matrix rule means a phase can ship with later phases
refused.

- **Phase 0 — infrastructure.** `clickhouse-go` dependency (license scan
  must stay green), `docker-compose.test.yml` service pinned to the
  current LTS image, `BROKOLI_TEST_CLICKHOUSE_URL` following the existing
  env-gate convention, CI `services:` entry, preflight provisioning.
- **Phase 1 — read.** Connection type + URI/driver detection; the required
  `Dialect` surface (backtick-or-doublequote identifier quoting, literal
  escaping shared with the MySQL backslash rule, `LIMIT 0` probe); the
  type reader with wrapper unwrapping (`Nullable`, `LowCardinality`);
  `source_db` and the migrate read side working end to end. Catalog entry
  lands here, per the rule above.
- **Phase 2 — append.** `BulkWriter` over the native batch; the type
  renderer and `create_table` with the engine default; `CreateDDL` executed
  writer-side as on the other backends (minus the transaction it cannot
  have); the differential corpus — the same hostile values MySQL's
  capability was earned with — asserted against all three backends.
- **Phase 3 — the refusals, pinned.** Upsert's named refusal at validation
  time; overwrite via `TRUNCATE` + append with its non-atomicity in the
  docs and a test observing the weaker semantics rather than hiding them;
  every pushdown cell asserted disabled, ADR-023-style.
- **Phase 4 — deferred until wanted.** Pushdown rules with measured `why`
  refusals (byte-wise string comparison actually aligns with the compiler's
  `COLLATE "C"` rule, so filters and grouping are plausible); a
  `dbt-clickhouse` entry in `dbtAdapters`, which ADR-025's read-back rule
  permits once Phase 1 exists.

## Consequences

### Positive

- An analytical sink with bounded memory: the workload ClickHouse is for is
  the one the write path is now best at, and `sinkCanStream` extends to it
  by implementing an interface, exactly as ADR-024 promised.
- The first backend that breaks the row-store assumptions, proving (or
  bending) the abstraction the way ADR-024's "shaped by one backend"
  negative predicted — better to find that now, with the capability matrix
  to absorb it, than with backend number five.
- One catalog entry stops being a lie before it starts, and the
  type-and-capability-together rule raises the bar for the existing five.

### Negative

- **A third backend in every corpus.** The differential discipline is a
  standing tax and this raises it by half again; every future hostile value
  runs against three servers, and CI's test job gets a third container.
- **The atomicity story gets a third case.** Documentation and resume
  semantics that were already two-pronged become three, and "it depends on
  the backend" is real cognitive load for operators.
- **The driver dependency is not small.** `clickhouse-go` brings `ch-go`
  and compression libraries; pure Go, but a real addition to the module
  graph and the binary.

### Deferred

- **Pushdown** — investigated in phase 4 and refused on measurement rather
  than left "plausible". clickhouse-go reports no row count for
  `INSERT ... SELECT` (pinned by `TestClickHouseInsertSelectReportsNoCount`,
  which fails if the driver ever starts reporting one), so a ClickHouse
  segment can never satisfy ADR-023's free-count-source eligibility rule:
  the planner would form segments whose executor must always fail at the
  commit gate. Behind that stand two further blockers, each an ADR-023
  violation of its own -- no transaction, so "inside a segment the commit
  is the artifact" cannot hold; and no lock primitive, so an overwrite
  segment cannot serialize against a concurrent one. The byte-wise
  string-comparison alignment noted below remains true and remains moot
  until the count measurement flips.
- **`ReplacingMergeTree`-aware sink modes** — an explicit, honestly-named
  "eventual dedup append" mode could exist someday; it is not upsert and
  will not be called upsert.
- **DuckDB** — same verdict as Oracle (CGO), recorded here so it is a
  decision rather than an absence; revisit if a pure-Go or wazero-based
  binding matures. Its dbt route is a separate question against ADR-025's
  read-back rule.
- ~~dbt-clickhouse~~ — landed in phase 4: `dbtAdapters` generates a
  `driver: native` profile against the connection's own port, proven end
  to end by `TestDBTRunsOnClickHouse`. (Operational note: dbt-clickhouse
  1.8.9's native client imports `pkg_resources`, removed in setuptools 81,
  so the dbt environment pins `setuptools<81`.)
- **HTTP protocol fallback** — the native protocol is the decision;
  environments that only expose HTTP (port 8123) are out of scope until
  someone real hits it.

## Alternatives considered

- **DuckDB first** — the constraint that deferred Oracle applies verbatim;
  see Deferred.
- **ClickHouse over its MySQL/Postgres wire compatibility** — the compat
  layers are partial, would misreport types, and would route into dialects
  whose semantics (transactions, upsert) ClickHouse does not have. The
  native driver tells the truth.
- **Upsert via `ReplacingMergeTree`** — rejected above; it cannot pass the
  equivalence test the capability requires, which is the system telling us
  it is not the same capability.
- **Waiting for a user to ask** — the catalog already advertises five
  backends nobody can use; the honest version of waiting is removing
  entries, not adding silence.

## Follow-ups

- Tracking issue with the four phases, on the #342 model.
- The catalog-honesty rule applied backwards: a separate issue for the
  advertised-but-dead types (named refusal at connection-test time for all
  of them, or removal) -- including fixing `detectDriver`'s default-to-pgx
  fallthrough, which ADR-024 flagged and which still stands.
- ADR-025 amendment discussion for dbt-duckdb, if wanted — separate from
  this decision.
