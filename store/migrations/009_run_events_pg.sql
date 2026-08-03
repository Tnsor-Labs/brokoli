-- run_events: immutable, append-only log of run/node-attempt lifecycle
-- transitions (Tnsor-Labs/brokoli#6). One row per fact — never updated,
-- never deleted. Rows are ordered by `id` (monotonic); that order is the
-- authoritative replay order for engine.ProjectRun.
--
-- NOTE: PostgresStore.migrate() (store/postgres.go) only embeds and executes
-- 001_initial_pg.sql; everything else, including this table, is applied via
-- idempotent inline DDL in Go (`CREATE TABLE IF NOT EXISTS`). This file
-- documents the schema for parity with the SQLite migration but is not
-- itself read at runtime. See 009_run_events.sql for more detail.
CREATE TABLE IF NOT EXISTS run_events (
    id BIGSERIAL PRIMARY KEY,
    run_id TEXT NOT NULL,
    node_id TEXT,               -- NULL for run-level events
    attempt INTEGER,            -- NULL for run-level events
    event_type TEXT NOT NULL,
    payload JSONB NOT NULL DEFAULT '{}',
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    schema_version INTEGER NOT NULL DEFAULT 1,
    FOREIGN KEY (run_id) REFERENCES runs(id) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS idx_run_events_run ON run_events(run_id);
CREATE INDEX IF NOT EXISTS idx_run_events_run_node ON run_events(run_id, node_id);
