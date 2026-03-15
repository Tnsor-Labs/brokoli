CREATE TABLE IF NOT EXISTS connections (
    id TEXT PRIMARY KEY,
    conn_id TEXT NOT NULL UNIQUE,
    type TEXT NOT NULL,
    description TEXT NOT NULL DEFAULT '',
    host TEXT NOT NULL DEFAULT '',
    port INTEGER NOT NULL DEFAULT 0,
    schema_name TEXT NOT NULL DEFAULT '',
    login TEXT NOT NULL DEFAULT '',
    password_enc TEXT NOT NULL DEFAULT '',
    extra_enc TEXT NOT NULL DEFAULT '',
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_connections_conn_id ON connections(conn_id);
