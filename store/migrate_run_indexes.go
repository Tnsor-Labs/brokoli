package store

import "database/sql"

// runIndexStatements are the same on both dialects.
var runIndexStatements = []string{
	// Purge, the retention listing, the run calendar and the dashboard
	// aggregates filter runs on started_at alone, and no index led with
	// it, so they scanned an index on pipeline_id end to end. Measured on
	// SQLite with a million runs: the 365-day calendar went from 4.2 s to
	// 1.2 s, the purge listing from 75 ms to 44 ms. A covering index on
	// (started_at, status) was only 8-15% faster again, and bigger.
	`CREATE INDEX idx_runs_started_at ON runs (started_at)`,
	// Each of these is the leading column of another index on the same
	// table, (run_id, status) and (run_id, node_id), which serves every
	// lookup it did. Each cost a write on every insert.
	`DROP INDEX idx_node_runs_run`,
	`DROP INDEX idx_run_events_run`,
}

// addRunIndexes is SQLite migration 3 and Postgres migration 2. On
// Postgres the index is built inside the migration's transaction, so
// writes to runs wait for the build to finish; they are not refused.
func addRunIndexes(tx *sql.Tx) error {
	for _, stmt := range runIndexStatements {
		if _, err := tx.Exec(stmt); err != nil {
			return err
		}
	}
	return nil
}
