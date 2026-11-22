package store

import "database/sql"

// max_instances is a pipeline's run-wide budget on mapped instances across
// every level of group nesting (ADR-050 phase 2c, IR 2.3). 0 means the
// server's default budget.
//
// This is SQLite migration 5 and Postgres migration 4. The baseline is
// frozen (see TestBaselineIsFrozen), so the column arrives as its own step
// rather than being added to the baseline schema.
//
// Both steps are idempotent, as every migration must be (TestMigration-
// IsIdempotent): Postgres uses ADD COLUMN IF NOT EXISTS; SQLite has no such
// clause, so the step checks for the column first and does nothing when it
// is already there.

const postgresPipelineMaxInstances = `ALTER TABLE pipelines ADD COLUMN IF NOT EXISTS max_instances INTEGER NOT NULL DEFAULT 0`

func addPipelineMaxInstancesSQLite(tx *sql.Tx) error {
	rows, err := tx.Query("PRAGMA table_info(pipelines)")
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var (
			cid        int
			name       string
			ctype      string
			notNull    int
			defaultVal sql.NullString
			pk         int
		)
		if err := rows.Scan(&cid, &name, &ctype, &notNull, &defaultVal, &pk); err != nil {
			return err
		}
		if name == "max_instances" {
			return rows.Close()
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	_, err = tx.Exec("ALTER TABLE pipelines ADD COLUMN max_instances INTEGER NOT NULL DEFAULT 0")
	return err
}

func addPipelineMaxInstancesPostgres(tx *sql.Tx) error {
	_, err := tx.Exec(postgresPipelineMaxInstances)
	return err
}
