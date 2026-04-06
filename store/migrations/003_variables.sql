CREATE TABLE IF NOT EXISTS variables (
    key TEXT PRIMARY KEY,
    value TEXT NOT NULL DEFAULT '',
    type TEXT NOT NULL DEFAULT 'string',
    description TEXT NOT NULL DEFAULT '',
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
);
