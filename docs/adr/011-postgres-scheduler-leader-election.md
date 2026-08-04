# ADR-011: Postgres scheduler leader election via leased row + fencing generation

**Status:** accepted
**Date:** 2026-08-04

## Context

ADR-009 flagged this gap directly: "scheduling, DAG outputs, retries, and
active-run ownership are process-local," with an explicit follow-up to
"extend the harness to Postgres before claiming equivalent durability
there." `engine.Scheduler` assumed it was a singleton — `Register`'s cron
closure called `engine.RunPipeline` unconditionally on every tick, and
`catchUpMissedRuns` fired missed runs unconditionally on `Start()`, with no
coordination against other instances. `cmd/serve.go` already exposes a
`--mode scheduler` role-split flag for enterprise distributed deployments,
but running more than one `--mode scheduler` replica against the same
Postgres database meant every replica independently registered the same
cron entries and independently dispatched the same pipeline runs — no
dedup, no lease, no fencing of any kind.

Tnsor-Labs/brokoli#10 asks for Postgres leader election with a fencing
generation, gating scheduler dispatch behind it, and explicitly documents
SQLite as single-instance-only rather than pretending to solve that case
too.

## Decision

Leadership is expressed as a single leased row in a `scheduler_leader`
table (Postgres only), not a `pg_try_advisory_lock`/`pg_advisory_unlock`
session lock — see the design note at the top of `store/postgres_leader.go`
for the full reasoning. In short: `store/postgres.go` opens its connection
via `sql.Open("pgx", uri)`, a pooled `*sql.DB` with no dedicated-connection
lifecycle management anywhere else in this codebase. Session-scoped
advisory locks require pinning one physical connection out of that pool for
the entire time leadership is held and very carefully ensuring it's never
silently recycled, closed by health-checking, or dropped by a context
timeout without the application noticing — Postgres releases the advisory
lock the instant that connection closes, with no visible error until the
next call fails. That's exactly the failure mode the issue calls "the
trickiest part."

A leased row sidesteps it entirely: every operation
(`store.PostgresLeaderElector.tryAcquire`/`renew`/`release`) is one
self-contained, idempotent SQL statement — `INSERT ... ON CONFLICT DO
UPDATE ... WHERE ... RETURNING` for acquisition, a fencing-checked `UPDATE
... WHERE holder=$1 AND fencing_generation=$2` for renewal — that works
correctly through the connection pool exactly like every other store
method in this package. It mirrors `ClaimAttempt`/`RenewLease`
(`store/postgres.go`, Tnsor-Labs/brokoli#7's execution-attempt leasing)
almost exactly, reusing a pattern this codebase already trusts rather than
introducing a second coordination primitive with a different lifecycle
model. The tradeoff is a bounded failover latency window (up to
`leaseDuration + renewInterval`, defaults 15s/5s) instead of
near-instant release-on-disconnect — acceptable given brokoli's
cron-scheduled dispatch is minute-granularity at best, not sub-second
coordination.

`fencing_generation` increments only when leadership changes hands (a
successful `tryAcquire`), not on every renewal — a plain renewal keeps the
same generation, so a downstream consumer comparing "did my generation
change" sees a stable value for the whole tenure and only sees it move
when a real handoff happened.

`engine.Scheduler` takes a `store.LeaderElector` and checks
`IsLeader()` before both the `Register()` cron closure's dispatch and
`catchUpMissedRuns()`. It defaults to `store.NewNoopLeaderElector()`
(always leader, zero database contact) when none is given, which is what
SQLite gets — `cmd/serve.go` never constructs a `PostgresLeaderElector`
for a non-Postgres store. This preserves single-binary/SQLite behavior
exactly: existing callers, and every non-HA deployment, see no change.

SQLite is guarded by a startup warning
(`cmd/serve.go`'s `warnIfSQLiteMultiInstanceRisk`), not a hard reject — the
binary cannot know how many *other* replicas are configured (there is no
`--replica-count` flag or equivalent anywhere in this repo), so a runtime
reject would need information we don't have. True enforcement belongs at
the deployment/orchestration layer — out of scope here. (Brokoli Enterprise's
managed deployment tooling handles this today.)

## Consequences

### Positive

- Killing the Postgres leader process lets a standby take over within a
  bounded, configurable window, without any risk of a stuck session lock
  from a connection-pool edge case.
- Every election operation is parameterized SQL through the existing pooled
  connection — no new connection-lifecycle code path to get wrong.
- Read-only endpoints (`schedulerStatusHandler`, run/log listing) never
  consult leadership state at all — standbys serve reads identically to the
  leader. Every instance still registers cron entries and loads schedules;
  only actual dispatch is gated.
- Leadership status, fencing generation, and acquisition/release/failure
  counts are exposed as Prometheus metrics
  (`brokoli_leader_status`/`brokoli_leader_fencing_generation`/
  `brokoli_leader_acquisitions_total`/`brokoli_leader_releases_total`/
  `brokoli_leader_election_failures_total`) and structured log lines on
  every acquire/release/loss.

### Negative

- Failover is not instantaneous — a dead leader's lease has to actually
  expire (default 15s) before a standby can take over, unlike a
  connection-drop-triggered advisory-lock release. This is a deliberate
  tradeoff (see Decision above), not an oversight.
- SQLite's multi-instance guard is a log warning, not an enforced limit.
  Running two SQLite-backed `--mode scheduler` replicas by mistake is still
  possible and will duplicate-dispatch every scheduled run.

### Deferred

- True enforcement of SQLite single-instance-ness (a real replica-count
  signal, or infrastructure-level guarantee) — belongs at the deployment/
  orchestration layer, not this OSS core.
- A full multi-process failover test (kill an actual second `brokoli
  --mode scheduler` OS process against a real Postgres cluster and assert
  exactly one replacement leader with zero duplicate dispatch) — this PR's
  tests exercise the same SQL-level election mechanism with multiple
  `PostgresLeaderElector` instances inside one Go test process against a
  real local Postgres, which is not the same as a true multi-process/
  multi-host scenario. See the PR description for exactly what is and
  isn't covered.

## Alternatives considered

- **`pg_try_advisory_lock`/`pg_advisory_unlock` session lock** — rejected;
  see Decision above. Near-instant lock release on connection loss is
  attractive, but the dedicated-connection lifecycle management it demands
  conflicts with how every other store method in this codebase uses the
  pooled `*sql.DB`, and getting that lifecycle wrong fails silently.
- **External coordination service (etcd/Consul/Zookeeper/redsync)** —
  rejected per the issue's explicit "out of scope": no such dependency
  exists in this codebase today, and Postgres (already a hard dependency
  for HA deployments) is sufficient for the single-region case this
  targets.
- **Hard-reject SQLite `--mode scheduler`/`all` outright** (even for a
  single intended replica) — rejected: SQLite single-instance is the
  default, fully-supported deployment mode: rejecting it unconditionally
  would break every non-HA install. The risk is specifically *multiple*
  SQLite replicas, which this binary cannot detect on its own.

## Follow-ups

- HA deployment topology / production Helm chart tooling — the natural
  home for real replica-count enforcement and multi-process failover
  test infrastructure (docker-compose/testcontainers or equivalent)
  that this OSS-core PR doesn't add. Brokoli Enterprise ships this as
  part of its managed deployment tooling.
- A CI-integrated live-Postgres test service (docker-compose or a
  `services:` block in `.github/workflows/ci.yml`) so
  `store/postgres_leader_test.go`'s live-database tests run in CI instead
  of skipping — see that file's doc comment for the current state.
