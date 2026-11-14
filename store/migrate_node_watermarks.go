package store

import "database/sql"

// node_watermarks holds the high-water mark a migrate node has read its
// source up to, so the next incremental run reads only the rows past it.
// Keyed by the pipeline and node, it outlives any one run.
//
// This is SQLite migration 4 and Postgres migration 3. The baseline is
// frozen (see TestBaselineIsFrozen), so the table arrives as its own step
// rather than being added to the baseline schema. The column types differ
// per dialect, so each store has its own statement.

const sqliteNodeWatermarks = `CREATE TABLE IF NOT EXISTS node_watermarks (
	pipeline_id TEXT NOT NULL,
	node_id TEXT NOT NULL,
	value TEXT NOT NULL,
	updated_at TEXT NOT NULL,
	PRIMARY KEY (pipeline_id, node_id))`

const postgresNodeWatermarks = `CREATE TABLE IF NOT EXISTS node_watermarks (
	pipeline_id TEXT NOT NULL,
	node_id TEXT NOT NULL,
	value TEXT NOT NULL,
	updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
	PRIMARY KEY (pipeline_id, node_id))`

func addNodeWatermarksSQLite(tx *sql.Tx) error {
	_, err := tx.Exec(sqliteNodeWatermarks)
	return err
}

func addNodeWatermarksPostgres(tx *sql.Tx) error {
	_, err := tx.Exec(postgresNodeWatermarks)
	return err
}
