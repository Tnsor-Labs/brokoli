-- scheduler_leader: single-row lease + fencing-generation table backing
-- Postgres scheduler leader election (Tnsor-Labs/brokoli#10). Exactly one
-- row exists, keyed by the fixed id 'scheduler'. Whichever instance holds
-- an unexpired lease (lease_expires_at in the future) is the leader
-- allowed to dispatch scheduled pipeline runs; every other instance stays
-- a standby that loads schedules and serves reads but never fires
-- RunPipeline. fencing_generation increments each time leadership changes
-- hands (not on every renewal), so a downstream consumer can detect and
-- reject a write from a since-superseded leader — the same fencing
-- contract models.ExecutionAttempt.FencingGeneration already establishes
-- for execution-attempt claims (Tnsor-Labs/brokoli#7).
--
-- See store/postgres_leader.go's design note for why this is a leased row
-- rather than a session-scoped pg_advisory_lock: it needs to work
-- correctly through database/sql's pooled *sql.DB (store/postgres.go's
-- sql.Open("pgx", uri)) without pinning a dedicated connection for the
-- duration of leadership.
--
-- NOTE: PostgresStore.migrate() (store/postgres.go) only embeds and
-- executes 001_initial_pg.sql; this table, like run_events and
-- execution_attempts before it, is applied via idempotent inline DDL in Go
-- (CREATE TABLE IF NOT EXISTS). This file documents the schema for parity
-- but is not itself read at runtime. See 009_run_events_pg.sql for the
-- same convention.
CREATE TABLE IF NOT EXISTS scheduler_leader (
    id TEXT PRIMARY KEY,                 -- always the literal 'scheduler'
    holder TEXT NOT NULL DEFAULT '',     -- opaque instance identity, see DefaultHolderID
    fencing_generation BIGINT NOT NULL DEFAULT 0,
    lease_expires_at TIMESTAMPTZ NOT NULL DEFAULT 'epoch',
    acquired_at TIMESTAMPTZ,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
