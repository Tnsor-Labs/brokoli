package store

import (
	"database/sql"
	"fmt"
	"log"
)

// sqliteTimestampColumns is every timestamp column the store owns on
// SQLite. Tables the api package creates (users, agent_tokens, oauth_*,
// change_requests) are not here: their schema is not under versioned
// migrations.
var sqliteTimestampColumns = []struct {
	table   string
	columns []string
}{
	{"alert_reads", []string{"read_at"}},
	{"alerts", []string{"created_at", "read_at", "dismissed_at", "acknowledged_at", "resolved_at"}},
	{"api_tokens", []string{"expires_at", "created_at", "last_used_at"}},
	{"connections", []string{"created_at", "updated_at"}},
	{"dead_letter_queue", []string{"created_at", "resolved_at"}},
	{"execution_attempts", []string{"lease_expires_at", "created_at", "updated_at"}},
	{"expansion_instances", []string{"started_at"}},
	{"login_attempts", []string{"attempted_at"}},
	{"logs", []string{"timestamp"}},
	{"node_profiles", []string{"created_at"}},
	{"node_runs", []string{"started_at", "ready_at"}},
	{"parked_waits", []string{"next_poll_at", "expires_at", "created_at"}},
	{"physical_instances", []string{"started_at"}},
	{"pipeline_templates", []string{"created_at", "updated_at"}},
	{"pipeline_versions", []string{"created_at"}},
	{"pipelines", []string{"created_at", "updated_at"}},
	{"resolved_execution_records", []string{"created_at"}},
	{"roles", []string{"created_at"}},
	{"run_events", []string{"created_at"}},
	{"run_physical_plans", []string{"created_at"}},
	{"run_provenance", []string{"recorded_at"}},
	{"runs", []string{"started_at", "finished_at", "data_interval_start", "data_interval_end"}},
	{"schema_migrations", []string{"applied_at"}},
	{"secret_stores", []string{"created_at", "updated_at"}},
	{"task_bundles", []string{"created_at"}},
	{"task_bundles_v2", []string{"created_at"}},
	{"variables", []string{"created_at", "updated_at"}},
	{"workspace_members", []string{"joined_at"}},
	{"workspaces", []string{"created_at", "updated_at"}},
}

// canonicalTimeGlob matches exactly what formatTime writes.
const canonicalTimeGlob = "[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]T[0-9][0-9]:[0-9][0-9]:[0-9][0-9].[0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9]Z"

// rewriteBatch bounds how many rows are held in memory at once; logs can
// run to millions of rows. A variable so a test can make batches small.
var rewriteBatch = 5000

// rewriteTimestamps is SQLite migration 2: every stored timestamp is
// rewritten in formatTime's fixed-width shape, so text comparison and
// ORDER BY match time order. A value no layout recognises is left as it
// is and counted; refusing to start over one bad row would be worse than
// the row.
func rewriteTimestamps(tx *sql.Tx) error {
	for _, tc := range sqliteTimestampColumns {
		var n int
		if err := tx.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = ?`, tc.table).Scan(&n); err != nil {
			return err
		}
		if n == 0 {
			continue // created lazily, and not yet
		}
		for _, col := range tc.columns {
			rewritten, unreadable, err := rewriteTimestampColumn(tx, tc.table, col)
			if err != nil {
				return fmt.Errorf("%s.%s: %w", tc.table, col, err)
			}
			if rewritten > 0 {
				log.Printf("store: rewrote %d timestamps in %s.%s", rewritten, tc.table, col)
			}
			if unreadable > 0 {
				log.Printf("store: left %d unreadable timestamps in %s.%s as they were", unreadable, tc.table, col)
			}
		}
	}
	return nil
}

func rewriteTimestampColumn(tx *sql.Tx, table, col string) (rewritten, unreadable int, err error) {
	// Identifiers come from sqliteTimestampColumns, never from input.
	sel := fmt.Sprintf(`SELECT rowid, %s FROM %s
		WHERE rowid > ? AND %s IS NOT NULL AND %s != '' AND NOT %s GLOB '%s'
		ORDER BY rowid LIMIT %d`, col, table, col, col, col, canonicalTimeGlob, rewriteBatch)
	upd := fmt.Sprintf(`UPDATE %s SET %s = ? WHERE rowid = ?`, table, col)

	type change struct {
		rowid int64
		value string
	}
	var after int64
	for {
		rows, err := tx.Query(sel, after)
		if err != nil {
			return rewritten, unreadable, err
		}
		var batch []change
		var seen int
		for rows.Next() {
			var rowid int64
			var v string
			if err := rows.Scan(&rowid, &v); err != nil {
				rows.Close()
				return rewritten, unreadable, err
			}
			seen++
			after = rowid
			t, perr := parseTime(v)
			if perr != nil {
				unreadable++
				continue
			}
			batch = append(batch, change{rowid, formatTime(t)})
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return rewritten, unreadable, err
		}
		rows.Close()
		for _, c := range batch {
			if _, err := tx.Exec(upd, c.value, c.rowid); err != nil {
				return rewritten, unreadable, err
			}
		}
		rewritten += len(batch)
		if seen < rewriteBatch {
			return rewritten, unreadable, nil
		}
	}
}
