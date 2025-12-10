CREATE TABLE IF NOT EXISTS pipelines (
    id TEXT PRIMARY KEY,
    name TEXT NOT NULL,
    description TEXT NOT NULL DEFAULT '',
    nodes TEXT NOT NULL DEFAULT '[]',     -- JSON array of nodes
    edges TEXT NOT NULL DEFAULT '[]',     -- JSON array of edges
    schedule TEXT NOT NULL DEFAULT '',
    webhook_url TEXT NOT NULL DEFAULT '',
    params TEXT NOT NULL DEFAULT '{}',
    enabled INTEGER NOT NULL DEFAULT 1,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS runs (
    id TEXT PRIMARY KEY,
    pipeline_id TEXT NOT NULL,
    status TEXT NOT NULL DEFAULT 'pending',
    started_at TEXT,
    finished_at TEXT,
    FOREIGN KEY (pipeline_id) REFERENCES pipelines(id) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS idx_runs_pipeline ON runs(pipeline_id, started_at DESC);

CREATE TABLE IF NOT EXISTS node_runs (
    id TEXT PRIMARY KEY,
    run_id TEXT NOT NULL,
    node_id TEXT NOT NULL,
    status TEXT NOT NULL DEFAULT 'pending',
    row_count INTEGER NOT NULL DEFAULT 0,
    started_at TEXT,
    duration_ms INTEGER NOT NULL DEFAULT 0,
    error TEXT NOT NULL DEFAULT '',
    FOREIGN KEY (run_id) REFERENCES runs(id) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS idx_node_runs_run ON node_runs(run_id);

CREATE TABLE IF NOT EXISTS logs (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    run_id TEXT NOT NULL,
    node_id TEXT NOT NULL DEFAULT '',
    level TEXT NOT NULL DEFAULT 'info',
    message TEXT NOT NULL,
    timestamp TEXT NOT NULL,
    FOREIGN KEY (run_id) REFERENCES runs(id) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS idx_logs_run ON logs(run_id, timestamp);

CREATE TABLE IF NOT EXISTS pipeline_versions (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    pipeline_id TEXT NOT NULL,
    version INTEGER NOT NULL,
    snapshot TEXT NOT NULL,           -- full pipeline JSON at this version
    message TEXT NOT NULL DEFAULT '', -- optional commit message
    created_at TEXT NOT NULL,
    FOREIGN KEY (pipeline_id) REFERENCES pipelines(id) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS idx_pipeline_versions ON pipeline_versions(pipeline_id, version DESC);

CREATE TABLE IF NOT EXISTS node_previews (
    run_id TEXT NOT NULL,
    node_id TEXT NOT NULL,
    columns TEXT NOT NULL DEFAULT '[]',    -- JSON array of column names
    rows TEXT NOT NULL DEFAULT '[]',       -- JSON array of row objects (max 50)
    PRIMARY KEY (run_id, node_id),
    FOREIGN KEY (run_id) REFERENCES runs(id) ON DELETE CASCADE
);
