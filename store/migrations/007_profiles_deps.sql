-- Node profiling: auto-computed statistics per node per run
CREATE TABLE IF NOT EXISTS node_profiles (
    run_id TEXT NOT NULL,
    node_id TEXT NOT NULL,
    profile TEXT NOT NULL DEFAULT '{}',       -- JSON DataProfile
    schema_snapshot TEXT NOT NULL DEFAULT '{}', -- JSON SchemaSnapshot
    drift_alerts TEXT NOT NULL DEFAULT '[]',   -- JSON []DriftAlert
    created_at TEXT NOT NULL,
    PRIMARY KEY (run_id, node_id),
    FOREIGN KEY (run_id) REFERENCES runs(id) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS idx_node_profiles ON node_profiles(run_id);
