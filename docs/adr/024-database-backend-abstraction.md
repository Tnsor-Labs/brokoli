# ADR-024: One place a database backend is described, and capabilities it has to earn

**Status:** proposed
**Date:** 2026-08-24

## Context

Postgres has had a great deal of work: streaming reads and writes, a `COPY`
fast path, a SQL pushdown compiler with differential tests (ADR-023, #331),
connection-identity rules. None of it generalises. Each capability arrived as
another Postgres-only branch, and the catalog advertises **nine** database
types while three drivers are compiled in.

A survey of the tree found **about thirty-five distinct sites** a new backend
must touch, spread across `models/`, `engine/`, `api/`, and `ui/`. The shape
of the sprawl matters more than the count:

- **Five quoting helpers.** Three are hardcoded Postgres with `PG` in the
  name and no dialect parameter — `quoteIdentPG`, `quoteQualifiedIdentPG`,
  `quoteLiteralPG` — and two more live on the `dialect` struct in
  `engine/sqlgen.go`.
- **A parameter that is already dead.** The pushdown compiler threads a
  `dialect string` through three functions, and the only production caller
  passes the literal `"postgres"` (`engine/pushdown.go`). The
  parameterisation exists and means nothing.
- **A capability silently scoped to one backend.** `sinkCanStream` resolves
  to `canCopyInsert`, which is `cfg.Dialect != "postgres" → false`. So
  ADR-019's bounded-memory property holds for *reads* on any compiled-in
  driver and for *writes* only on Postgres. Nothing says so; a MySQL sink
  just materialises.
- **Type classification written in one driver's vocabulary.**
  `classifyDatabaseType` lists pgx spellings — `BPCHAR`, `INT4`, `FLOAT8` —
  so a MySQL column classifies as unclassified even where the gate would
  otherwise allow it.
- **A default that guesses.** `detectDriver` returns `pgx` for an
  unrecognised URI. `dialectForURI` then reports `postgres`, `canCopyInsert`
  agrees, and the value is routed into the Postgres `COPY` path.

### This is not a hypothetical cost

Two defects on the MySQL write path were found while doing this survey, and
both are the sprawl expressing itself:

`dialect.quoteString` doubled the quote character and did nothing about
backslash. MySQL escapes with backslash unless `NO_BACKSLASH_ESCAPES` is set,
which is not the default, so an ordinary Windows path lost its separators and
a value ending in a backslash escaped the quote meant to close the literal —
after which MySQL parsed the following text as SQL. Postgres was unaffected
because `standard_conforming_strings` makes a backslash literal there. One
escaping rule, written once, correct for the backend it was written against.

`BuildURI` assembled the MySQL DSN with `net/url`, which percent-encodes
userinfo, while go-sql-driver reads credentials verbatim. A password
containing `@` was rejected with `Access denied` and nothing pointing at the
cause. One URI builder, correct for the backend it was written against.

### What happens if this is left alone

`store/` is the same story a few years further on. `store/postgres.go` and
`store/sqlite.go` are two complete parallel implementations of the same
interface, and this codebase already needed a migration-parity test to catch
them drifting. The engine is on that path: every capability added to Postgres
widens the gap that a second backend has to close by hand.

The same shape has bitten three times this week alone — two URI builders with
one dead and both diverging; `csvRecordFor` patched for a number-rendering
bug while `copyEscape` was not; and the escaping defect above.

## Decision

**A backend is described in one place, and a capability is something it has to
earn.**

### One place

A new package (`pkg/dbdialect`) owns the four concerns that currently sprawl:

- **Addressing** — the `database/sql` driver name, the DSN built from a
  connection's fields, and whether two connections address the same server.
- **Syntax** — identifier quoting, literal quoting *and escaping*, the
  clear-table form, the upsert form, and the shape of a zero-row probe
  (`LIMIT 0`, `TOP 0`, `FETCH FIRST`).
- **Types** — classifying a driver's `DatabaseTypeName` into the kinds the
  compiler reasons about, and rendering an inferred type as DDL.
- **Capabilities** — bulk write, pushdown, upsert: what this backend can do
  beyond the baseline.

The dialect takes a plain address struct rather than `models.Connection`, so
the dependency runs one way and `models` keeps no knowledge of SQL.

### Required, then optional

Following the convention `store/store.go` states — "a new capability is added
as its own interface and embedded here, instead of appending to a single
hundred-method declaration" — the required surface is small and the rest is
optional and discovered by type assertion, the idiom
`engine/artifact_store.go` calls "every other optional capability in this
codebase":

```go
// Every backend answers all of this.
type Dialect interface {
    Name() string
    Driver() string
    QuoteIdent(name string) string
    QuoteLiteral(s string) string
    ClassifyType(dbTypeName string) ColumnKind
    ProbeColumnsSQL(query string) string
}

// Optional. Absence means today's behaviour, never an error.
type BulkWriter interface { ... }   // COPY, LOAD DATA LOCAL INFILE
type Addresser  interface { SameServer(a, b string) bool }
type Upserter   interface { ... }
```

**An absent capability degrades, it does not fail.** A backend with no
`BulkWriter` writes generated statements exactly as it does today. This is the
rule already written into `store/store.go`: a capability lets an
implementation "opt into the capability when it's ready rather than being
force-broken".

**A predicate answers `(value, ok)` and never errors for "not supported"**,
and its doc comment states what would have to be *proven* to flip it —
`pkg/loaders/stream.go`'s `SupportsStreaming` is the model, and it explains
why JSON cannot stream rather than merely returning false.

### Earned, not declared

This is ADR-023's capability registry applied to backends rather than
reference kinds:

> every enabled cell carries a differential test against the reference path
> over a shared fixture corpus, and every disabled cell is asserted disabled

**A backend may claim a capability only when a differential test proves it
against the reference path.** Not "the code looks equivalent" — a passing
comparison against a live server, over a corpus shared with the other
backends rather than one that suits the backend under test.

That last clause is deliberate. The escaping fix ships with a corpus of
awkward values asserted against *both* MySQL and Postgres, because a value's
journey must not depend on where it lands — and because the Postgres run is
what stops the MySQL fix being applied everywhere "to be safe".

The corollary is that **refusals carry reasons**. `sum` is not pushed down
because float addition is not associative and the same four values total `1`
or `0` depending on aggregation order; that reason is a test
(`TestSumIsOrderSensitive`) which fails if it ever stops being true. A backend
that cannot do something says why, in a form that gets re-examined when the
world changes.

## Consequences

### Positive

- Adding a backend becomes implementing an interface rather than finding
  thirty-five branch points, and the compiler names what is missing.
- The dead `dialect string` parameter becomes real: the pushdown compiler can
  be handed the connection's actual dialect instead of a literal.
- Capabilities stop being silently Postgres-shaped. `sinkCanStream` asks the
  dialect whether it can bulk-write, so a MySQL sink gains bounded memory by
  implementing an interface rather than by someone noticing.
- Escaping, quoting, and type classification each have one home, so the next
  bug of the kind fixed this week has one place to be fixed rather than five.
- The refusal discipline generalises. Today it guards which transform rules
  compile; under this it guards which backends may claim which capability.

### Negative

- **A large behaviour-preserving refactor.** The claim "nothing changed" is
  only as good as the tests, and the live tests that would catch a change
  have only just started running anywhere but one machine.
- **An abstraction shaped by one backend.** Postgres is the only complete
  implementation, so the first cut will encode Postgres assumptions and MySQL
  will bend it. That is the argument for migrating Postgres first and
  accepting churn when the second backend arrives, rather than designing for
  imagined backends.
- **The corpus discipline is a standing tax.** Every capability on every
  backend needs a passing comparison before it turns on. That is the point,
  and it is real work that does not look like progress.
- **Not every concern moves cleanly.** Connection testing, the type catalog,
  and the UI's dialect dropdowns are dialect-aware in their own ways and are
  not obviously the dialect package's business.

### Deferred

- **Oracle.** No driver exists and no code path mentions it beyond a catalog
  card and an icon. The binding constraint is the build: the release is
  `CGO_ENABLED=0` cross-compiled to six targets, and `modernc.org/sqlite` was
  chosen over the CGO SQLite driver for that reason — so `godror`, which
  needs CGO and Oracle Instant Client, cannot be used without giving up the
  single static binary. Pure-Go `go-ora` is the only candidate that fits.
  Recorded so the constraint is not rediscovered.
- **The control plane.** Brokoli's own metadata store is a separate axis with
  its own two parallel implementations. Nothing here touches it.
- **SQL Server, Snowflake, BigQuery, Databricks.** Advertised in the catalog,
  no driver compiled in. This ADR makes adding them tractable; it does not
  add them.
- **Parameter binding.** Generated SQL interpolates literals rather than
  binding parameters, which is why escaping carries the whole risk. Moving to
  bound parameters would remove that class of defect entirely and is a larger
  change than this one.

## Alternatives considered

- **Keep adding branches, extract later when the shape is obvious.** This is
  how the current thirty-five sites happened, and `store/` shows where it
  ends: two full implementations and a parity test to catch drift.
- **One large `Backend` interface.** Rejected against the codebase's own
  convention, stated in `store/store.go`, and because it forces every backend
  to implement things it cannot do — turning "not supported" into a runtime
  error rather than a capability it simply lacks.
- **A registry with `init()` self-registration**, as some plugin systems do.
  Rejected: nothing here registers that way. `store/factory.go` switches on a
  URI prefix, `pkg/secrets` indexes resolvers by their own `Scheme()`, and
  `extensions.Registry` is a plain struct of fields. A new mechanism would be
  a fourth pattern for readers to learn.
- **Design the interface against MySQL and Postgres together**, so it is not
  Postgres-shaped. Genuinely tempting, and rejected only on risk: a refactor
  that also changes behaviour cannot use the existing suite as its oracle.
  MySQL will bend the interface, and that is an acceptable second step.

## Follow-ups

- #327 — `sink_api` streaming, which this makes reachable for MySQL sinks.
- #329 — the engine test package's timeout headroom, which service containers
  in CI will make tighter.
- #331 — the pushdown tracking issue; the compiler is the first consumer of
  the dialect abstraction.
- A follow-up issue for the MySQL correctness items this survey turned up and
  did not fix: `TRUNCATE` and `CREATE TABLE` implicitly commit, so the
  clear-then-insert atomicity `ExecuteSQL` assumes does not hold there;
  `key_columns` is silently ignored by MySQL upsert; and MySQL 8.0.20
  deprecated the `VALUES()` form the generator emits.
