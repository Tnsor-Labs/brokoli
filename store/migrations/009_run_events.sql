-- run_events: immutable, append-only log of run/node-attempt lifecycle
-- transitions (Tnsor-Labs/brokoli#6). One row per fact — never updated,
-- never deleted. Rows are ordered by `id` (monotonic); that order is the
-- authoritative replay order for engine.ProjectRun.
--
-- NOTE: like 004_tags.sql/006_sla.sql/007_profiles_deps.sql/008_credential_refs.sql,
-- this file documents the schema but is NOT part of the hardcoded list of
-- migrations executed by SQLiteStore.migrate() (store/sqlite.go). The actual
-- table is created via an idempotent `CREATE TABLE IF NOT EXISTS` in Go
-- alongside the rest of that function, so it applies on every startup
-- regardless of which migration files are wired up. Formalizing the
-- migration runner (so numbered files on disk are the single source of
-- truth) is tracked separately and deliberately out of scope here.
CREATE TABLE IF NOT EXISTS run_events (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    run_id TEXT NOT NULL,
    node_id TEXT,               -- NULL for run-level events
    attempt INTEGER,            -- NULL for run-level events
    event_type TEXT NOT NULL,
    payload TEXT NOT NULL DEFAULT '{}', -- JSON
    created_at TEXT NOT NULL,
    schema_version INTEGER NOT NULL DEFAULT 1,
    FOREIGN KEY (run_id) REFERENCES runs(id) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS idx_run_events_run ON run_events(run_id);
CREATE INDEX IF NOT EXISTS idx_run_events_run_node ON run_events(run_id, node_id);
