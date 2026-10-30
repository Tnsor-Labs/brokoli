package store

import (
	"database/sql"
	"fmt"

	"github.com/Tnsor-Labs/brokoli/models"
)

// appendLogsTx inserts entries in order in one transaction, through one
// prepared statement: all of them or, on any error, none.
func appendLogsTx(db *sql.DB, insert string, entries []*models.LogEntry, argsOf func(*models.LogEntry) ([]interface{}, error)) (err error) {
	if len(entries) == 0 {
		return nil
	}
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()
	stmt, err := tx.Prepare(insert)
	if err != nil {
		return err
	}
	defer stmt.Close()
	for i, entry := range entries {
		args, err := argsOf(entry)
		if err != nil {
			return fmt.Errorf("log entry %d: %w", i, err)
		}
		if _, err := stmt.Exec(args...); err != nil {
			return fmt.Errorf("log entry %d: %w", i, err)
		}
	}
	return tx.Commit()
}
