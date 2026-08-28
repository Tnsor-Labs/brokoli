# ADR-028: A scheduled run processes an interval, not a moment

**Status:** accepted — direction agreed; implementation phased, unstarted
**Date:** 2026-08-28

## Context

A Brokoli schedule today means "fire at these times." The scheduler parses
the cron expression, the leader's tick calls `RunPipeline`, and the run
knows nothing about *why now* — it carries `Params`, a start timestamp,
and no statement of what slice of the world it is responsible for. Three
consequences follow, each already visible:

- **Backfill does not exist.** "Re-process March" means hand-editing a
  query's WHERE clause and clicking run repeatedly, changing the clause
  each time. Nothing records which slices were done, so nothing can say
  which are missing.
- **Catch-up is a single shot.** `catchUpMissedRuns` fires *one* run "now"
  after downtime, regardless of how many ticks were missed — correct for
  "refresh the dashboard," wrong for "load each day's partition," and the
  scheduler cannot tell which kind of pipeline it is looking at.
- **The SDK refuses `catch_up` at construction.** That refusal was the
  honest choice (ADR-015's runtime-honesty rule: never accept
  configuration that does not run), and it is also a signpost: the
  parameter people reach for exists in the ecosystem's vocabulary and not
  in ours.

The missing abstraction is the one thing Airflow got deeply right, and it
survives into Airflow 3 unchanged while most of that system's architecture
is being rebuilt: **a scheduled run is parameterized by the data interval
it covers**. A daily run at 06:00 is not "the 06:00 run"; it is "the run
responsible for [yesterday 06:00, today 06:00)". Once a run states its
interval, backfill is "create runs for these intervals," catch-up is
"create runs for the missed intervals," idempotent re-runs are "the same
interval again," and completeness is a query.

The moment is right for a Brokoli-specific reason: the recent write-path
arc made interval-idempotent pipelines cheap. An interval-scoped query
feeding an `overwrite` sink — or an `upsert` keyed on the interval's rows
(#377) — is exactly re-runnable, on every backend including ClickHouse
where the docs already tell resumed appends to "use overwrite for
idempotent re-runs." Intervals are the missing half of that sentence: the
thing that makes a re-run *mean* something.

### What this deliberately does not copy

Airflow's `execution_date`. Its interval semantics were right and its
naming was a decade-long support burden — a "date" that was neither the
run time nor the interval end, renamed twice, still confusing users in
2025. This ADR adopts the interval and refuses the ambiguity: there are
exactly two new values, `data_interval_start` and `data_interval_end`,
half-open `[start, end)`, and no third alias for either.

## Decision

**A run may carry a data interval — two timestamps, half-open — stamped by
whatever created it. The scheduler stamps the interval a tick covers;
backfill creates one run per interval in a range; catch-up, when a
pipeline opts in, creates one run per missed interval. Pipelines consume
the interval as variables. A run with no interval (manual, webhook, dbt,
today's entire population) behaves exactly as today.**

### The interval

For a schedule firing at tick T, the interval is `[previous tick, T)` in
the schedule's timezone, stored UTC. The run fires at the interval's *end*
— data for a window is complete when the window closes, which is the
convention every interval-aware system converges on. Half-open, so
consecutive intervals partition time with no double-counted boundary.

### Where it rides

- `models.Run` gains `DataIntervalStart`/`DataIntervalEnd` (`*time.Time`,
  nullable, serialized). Both stores gain the columns via the usual
  idempotent migration. Nullable is the compatibility story: every
  existing run, and every manual run forever, simply has none.
- **Variables are the pipeline-facing surface**: `${interval.start}` and
  `${interval.end}` resolve to RFC3339 UTC in any node config string, the
  same mechanism `${params.*}` uses today, so an interval-scoped source is

      SELECT ... WHERE ts >= '${interval.start}' AND ts < '${interval.end}'

  with no new node types and no engine changes beyond the resolver. In a
  run with no interval the variables resolve to nothing and validation
  warns — a pipeline that references `${interval.*}` is declaring it wants
  scheduled or backfill triggers.
- The run record answers "which slices exist": completeness and gap
  queries are `GROUP BY data_interval_start` over run history, which the
  existing run-history indexes already serve.

### Dispatch idempotency, scoped tightly

Scheduler-originated runs become unique on
`(pipeline_id, data_interval_start)`, enforced by the store the same way
`SavePipelineVersion`'s unique index closed its compute-then-insert race.
This is the leader-failover guard: two instances believing themselves
leader across a lease boundary can both fire the same tick today; with the
constraint, the second insert loses and no duplicate run exists.

Deliberately narrow: the constraint binds *scheduled* dispatch only.
Manual runs never carry an interval, and a backfill re-running an interval
creates a new run on purpose — runs are immutable history here, and
"re-do March 3rd" must append a new attempt at that slice, not be refused
because the slice was attempted before. (Airflow instead clears and
mutates the old run; our model keeps the failed attempt visible, which is
the same choice the execution-attempt store already made.)

### Catch-up becomes per-interval, opt-in

A pipeline gains `catchup: true|false` (default **false**). On leader
startup, an opted-in pipeline gets one run per missed interval, oldest
first, bounded by a per-pipeline cap (default 50) so a pipeline paused for
a year does not detonate the queue on unpause — the cap is logged when it
truncates, per the no-silent-caps rule. The default keeps today's exact
behaviour: one run, now, latest interval only. This is what finally lets
the SDK's `catch_up` parameter stop raising: the server advertises
`data_intervals` in `supported_execution_features`, and the SDK gates on
it per its existing capability protocol — the honesty rule is satisfied
because the configuration now *runs*.

### Backfill

`POST /api/pipelines/{id}/backfill {"start": ..., "end": ...}` enumerates
the schedule's intervals in the range and creates the runs, marked as
backfill-triggered, dispatched oldest-first at bounded concurrency
(initially 1 — sequential — until pools exist; the bound is stated in the
response rather than implied). The UI's pipeline page gets the button; the
CLI gets the verb. A backfill over a pipeline that never referenced
`${interval.*}` is almost certainly a mistake, and validation says so
before creating fifty identical runs.

## Consequences

### Positive

- Backfill, gap detection, and real catch-up fall out of one recorded
  value, rather than each being its own feature.
- Idempotent re-runs get their missing half: the write-path arc made
  re-running cheap; intervals make re-running *addressable*.
- The SDK's `catch_up` refusal converts to a capability, by the book.
- Nothing changes for any run that exists today: the field is nullable,
  the variables resolve only when stamped, catch-up defaults to today's
  single-shot.

### Negative

- **A `models.Run` schema change**, which crosses the EE seam: the
  APIStore must carry the two columns, so this lands core-first and EE
  follows at the next pin bump — the same sequencing every run-field
  addition has used.
- **Interval computation from cron needs a "previous tick" the cron
  library does not provide** — it must be derived by stepping, and the
  edge cases (DST transitions in zoned schedules, `@daily` vs explicit
  cron) need their own table of tests. This is the fiddly center of the
  implementation.
- A bounded backfill without pools serializes: fifty intervals run one at
  a time. Acceptable first cut; pools (the Airflow idea worth stealing
  next) lift it later.

### Deferred

- **Pools / concurrency budgets** — backfill concurrency above 1 waits on
  them.
- **Event-driven intervals** (a run per arriving partition rather than per
  clock tick) — a different trigger source stamping the same two fields;
  nothing here precludes it.
- **Interval-aware sensors/waits** — the triggerer-shaped idea; separate
  decision.
- **`${interval.*}` date-formatting helpers** (`${interval.start_date}`
  etc.) — added on demand; two RFC3339 values are the contract, formats
  are sugar.

## Alternatives considered

- **Params-only convention** (`params.start`/`params.end` by convention,
  no schema change) — no dispatch idempotency, no gap queries, catch-up
  cannot enumerate what it missed, and every pipeline invents its own
  spelling. The convention is what teams do *before* the abstraction
  exists.
- **Airflow's model wholesale** (logical date as run identity, clearing
  runs to re-run) — imports the naming confusion this ADR exists to
  refuse, and mutating history contradicts the execution-attempt store's
  append-only choice.
- **Wait for event-driven triggers and skip clock intervals** — the two
  are complementary, and the clock version is the one every scheduled
  pipeline can use today.

## Follow-ups

- Tracking issue with phases: (1) model + store + variables + scheduler
  stamping + dispatch uniqueness; (2) opt-in per-interval catch-up;
  (3) backfill API/CLI/UI; (4) `supported_execution_features` flag and the
  SDK's `catch_up` un-refusal, in that order — the SDK gate is last so it
  only stops raising when the whole path runs.
- EE pin-bump note: APIStore columns for the two fields.
- The DST/previous-tick test table, written before the scheduler stamping
  lands, not after.
