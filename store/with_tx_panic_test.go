package store

import (
	"database/sql"
	"path/filepath"
	"testing"
)

// WithTx rolled back only when fn returned an error. If fn panicked, the
// transaction was left open: on SQLite it kept the write lock, so every
// later write failed with "database is locked" once the busy timeout ran
// out. A panic now rolls the transaction back on its way out.
func TestWithTxRollsBackWhenFnPanics(t *testing.T) {
	s, err := NewSQLiteStore(filepath.Join(t.TempDir(), "tx.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	if _, err := s.db.Exec(`CREATE TABLE tx_probe (x INTEGER)`); err != nil {
		t.Fatal(err)
	}

	func() {
		defer func() {
			if recover() == nil {
				t.Fatal("fn's panic did not reach the caller")
			}
		}()
		_ = s.WithTx(func(tx *sql.Tx) error {
			if _, err := tx.Exec(`INSERT INTO tx_probe VALUES (1)`); err != nil {
				t.Fatal(err)
			}
			panic("boom")
		})
	}()

	if _, err := s.db.Exec(`INSERT INTO tx_probe VALUES (2)`); err != nil {
		t.Fatalf("a write after the panicking transaction failed: %v", err)
	}
	var n int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM tx_probe`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("tx_probe has %d rows, want 1 (the panicking transaction's row rolled back)", n)
	}
}
