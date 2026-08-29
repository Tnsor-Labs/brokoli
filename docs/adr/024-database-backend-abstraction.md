# ADR-024: One place a database backend is described, and capabilities it has to earn

**Status:** accepted — interfaces proven by the Postgres migration (#341); MySQL phases tracked in #342
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

## Update (2026-08-24): what migrating Postgres taught

Phase 1c is done — `pkg/dbdialect` exists and the compiler consumes it
(#341), with the CI harness (#339) running the differential suites against
live servers on every push. The interface that survived contact with the
code differs from the sketch above in ways worth recording, because each
difference is something the sketch got wrong.

### The taxonomy missed a fifth concern: agreement

The decision names four concerns — addressing, syntax, types, capabilities.
The migration surfaced a fifth that is none of these: **how the backend's
SQL is made to agree with the reference engine**. The implemented interface
carries three methods the sketch did not anticipate:

```go
CastToText(expr string) string   // CAST(... AS TEXT)
CastToFloat(expr string) string  // CAST(... AS DOUBLE PRECISION)
ByteOrderedText(expr string) string  // ... COLLATE "C"
```

These are not syntax preferences. `CastToFloat` is **deliberately lossy**:
the Go engine compares numbers through `strconv.ParseFloat`, so the SQL
side must lose the same precision — a backend whose cast is *more* accurate
than the engine's comparison diverges from it, and `NUMERIC` would be a
bug. `ByteOrderedText` exists because Go compares bytes and no backend's
default collation does. A backend author filling in these methods is not
answering "how do I spell a cast" but "what makes my comparisons agree with
the engine's" — which is exactly the question the differential corpus then
verifies. The interface turned out to be the corpus's questions, written as
methods.

### "Addressing" overpromised, and the sketch's `Driver()` does not exist

The decision assigned the dialect "the `database/sql` driver name, the DSN
built from a connection's fields, and whether two connections address the
same server". Only the third moved. Driver detection stays in
`engine/database.go` and DSN construction stays on `models.Connection`,
because both are on the connection-opening path and moving them is not
behaviour-preserving in a way the suite can witness. The required surface
as implemented has no `Driver()` method; same-server identity is the
optional `Addresser`, exactly as sketched. The rest of the addressing
concern migrates when it can carry its own oracle.

Related, still true after this phase: `detectDriver`'s `default` branch
still answers `pgx`, so an unrecognised URI is still handed to Postgres.
`DetectDriver` now refuses schemes whose driver is not compiled in, with an
error naming the scheme — but an unknown scheme is not one of those.
Refusing it is a behaviour change (URIs that "work" today by accident would
start erroring) and needs its own change with its own justification, not a
line in a refactor.

### "One place" is, for now, two places

`engine/sqlgen.go` keeps its own `dialect` record — quoting characters,
type maps, timestamp layouts, boolean literals — for statement
*generation*, while `pkg/dbdialect` serves statement *compilation*. This is
deliberate, not an oversight: the generator's record is on the write path,
and folding it in would be a refactor whose mistakes corrupt destination
tables rather than failing tests. The merge is the obvious next step and
waits until the write path has the same differential coverage the compiler
has. Until then, a backend author touches two records, and this section is
the notice the decision section otherwise fails to give.

### The dialect travels on the reference

ADR-023's `TableRef` gained a field: `Dialect string`, the backend the
carried query is written for. This is how the compiler receives the real
dialect instead of the literal `"postgres"` the sprawl section describes —
the producer that builds the ref knows the connection, records the dialect,
and every consumer emits in it rather than re-deriving it and possibly
disagreeing. A reference that crosses a segment now names the SQL it is
written in, which ADR-023 should have required from the start.

### Absent capabilities are pinned, not just absent

The "asserted disabled" half of the registry discipline is implemented
literally: `engine/table_ref_test.go` asserts that an unknown dialect never
pushes down and that MySQL — which has no `Addresser` yet — never pushes
down. When Phase 4 gives MySQL a same-server rule, that test fails, which
is the point: a backend cannot gain a capability by omission, only by a
change that trips a pinned expectation and replaces it with a differential
proof.

### One negative retired

The first negative above — "the live tests that would catch a change have
only just started running anywhere but one machine" — is retired. #339 put
Postgres and MySQL service containers in CI, and the run logs show the
suites executing there: the value-fidelity corpus against both backends,
and the pushdown differential tests against live Postgres. The claim
"nothing changed" in #341 is as good as those tests, and those tests now
run on every push.

## Update (2026-08-25): the second backend, and what it cost the interface

MySQL is now a first-class backend: correctness (#344), bulk write (#345),
and pushdown (this update's change). The ADR predicted "MySQL will bend the
interface, and that is an acceptable second step". It bent it in one place
and confirmed it in another, and both are worth recording.

### The interface held; one method's contract got sharper

No method was added, removed, or re-signed to admit MySQL. The nine-method
`Dialect` plus the optional `Addresser` was enough, which is the strongest
evidence available that deriving the surface from what the compiler
actually calls — rather than from what a database might need — was right.

What changed is `ByteOrderedText`'s contract. On Postgres it is
`COLLATE "C"` and reads as a formality: text equality under a deterministic
collation is already byte equality, so the method looked like it existed
only for ordering. MySQL proved otherwise. Its default collations are
case- and accent-insensitive, so `'Lisbon'` and `'lisbon'` are *equal*
there — and the aggregate compiler was emitting a bare `GROUP BY` on the
raw identifier, which collapsed them into one group where Go keeps two.

The differential corpus caught it on its first MySQL run: seven groups in
SQL, eight in Go. The fix is that grouping now goes through
`ByteOrderedText` like every other text comparison, which is what the
method meant all along. On Postgres the emitted SQL changed and the result
did not — the corpus proves that too.

That is the argument for a second backend stated concretely: a rule that is
a no-op on the reference implementation is indistinguishable from a rule
that is wrong, until something exercises it.

### One documented divergence between backends

`filter_rows` on a boolean column compiles on MySQL and is refused on
Postgres. This is not a gap to close; it is the two backends genuinely
differing:

- Postgres has a real `BOOLEAN`, pgx hands Go a `bool`, and `%v` renders
  `"true"`. Matching that in SQL needs a cast whose spelling has not been
  shown to agree, so it stays refused.
- MySQL's `BOOLEAN` is `TINYINT(1)`, and the driver's text protocol hands
  Go the server's own rendering — `"1"` — so the column is numeric on both
  sides and the existing numeric forms already agree.

The corpus expresses this as a per-dialect override on one shared case,
with the reason attached, rather than as two corpora. The distinction
matters: one corpus with recorded differences is a comparison; two corpora
are two claims that never meet. Every other case in the corpus carries a
single shared expectation.

### What MySQL refuses, and why the list is not shorter

Same refusals as Postgres, with the same reasons, all still holding on a
live server: `add_column` (no shared expression AST, #213), `sum`/`avg`
(float addition is not associative and the database chooses the order,
#325), `apply_function` and `replace_values` (no proven agreement on
rendering a non-text column), `in` (set membership over rendered text),
grouping by a numeric column, and text columns compared against
numeric-looking targets.

MySQL adds one refusal of its own, in the type classifier rather than the
rule compiler: `BINARY`, `VARBINARY` and the `BLOB` family are
unclassified. Their bytes are the column's raw value, but a literal in a
generated statement is in the connection character set, and equality
between the two is not the byte equality Go performs. `DATE`, `DATETIME`,
`TIMESTAMP`, `TIME` and `YEAR` are unclassified on both backends.

### Same-server identity: the parser is the specification

`SameServer` for MySQL uses the driver's own `ParseDSN` rather than a
second parser of the same format. #338 exists precisely because a second
parser drifted from the first — it percent-encoded credentials the driver
reads verbatim — so re-deriving DSN semantics by hand was the one approach
ruled out in advance.

The strictness matches the Postgres rule: TCP only (a socket is a
different addressing scheme with its own argument), address as written
after default-port normalisation, no DNS resolution, and user, password
and database compared exactly. DSN parameters disqualify unless they are on
a client-only allowlist (`parseTime`, `loc`, the timeouts, `tls`), because
those affect this client's session rather than which server the composed
statement runs on — and the statement runs entirely server-side.

One implementation note that is load-bearing: the parameter keys are read
from the raw DSN string, not from the parsed `Config`. The driver stores
`charset` in an unexported field, so a check that inspected only the
exported struct would have silently permitted the single parameter with
the clearest effect on comparison semantics.

### The pins tripped, twice

ADR-024 claimed absent capabilities are pinned rather than merely absent.
Both pins fired on the way in, exactly as designed: `sameServer("mysql",
…)` asserting no pushdown (#341's addition) and `TestSinkCanStreamScope`
asserting MySQL sinks do not stream (#345's). Neither could be satisfied by
adding a capability quietly — each had to be replaced, in the same change
that carries the differential proof, with the opposite assertion. The
vacated slots were refilled with backends that still lack the capability
(sqlite), so the pins keep meaning something.

## Update (2026-08-29): the fold finished, and what stayed behind (#361)

#401 moved the write vocabulary; this update records the rest of the
fold. `detectDriver` and `dialectForURI` are now adapters over the
registry's own **URI claims** (`pkg/dbdialect/uriclaim.go`): each
backend states the schemes it owns, the `database/sql` driver that opens
them, and the DSN shape that driver wants, next to the rest of its
vocabulary. Adding a backend's scheme means implementing `URIClaims`,
not editing the engine's table. The unknown-scheme refusal (#395) now
lists the claimed schemes from the registry, so the message cannot drift
from the claims.

Three things deliberately did not fold, each recorded where it lives:

- **snowflake://** maps to a driver with no dialect owner in the
  registry; it stays in the engine adapter until snowflake earns a
  registration tier with that tier's proof.
- **The `.db`/`.sqlite` filename suffixes and the schemeless-string
  Postgres default** are heuristics about strings with no scheme to
  claim; they stay in the adapter, pinned by its mapping tests.
- **`models.Connection.BuildURI` and the API connection catalog** span
  connectors that are not database dialects (HTTP, S3, SFTP, ...), so a
  database-dialect registry cannot own them without claiming things it
  has no vocabulary for. They remain the per-connector tables they are.

`bulkWriterFor` keeps its writers in the engine -- they are engine code,
COPY/LOAD DATA plumbing over engine types -- but its dispatch key is the
registry dialect name, so it names no backend the registry does not.

The acceptance bar was this ADR's own: the pre-existing mapping tests
(`TestDetectDriverSchemeMapping`, `TestDialectForURI`) pass unchanged,
and the claims' exact coverage is pinned by name in
`TestAllURIClaimsCoverage`.
