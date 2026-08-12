package store

import (
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"github.com/Tnsor-Labs/brokoli/models"
)

// TestExecutionAttemptsPrimaryKeyWidenPreservesExistingRows proves
// widenExecutionAttemptsPrimaryKey (ADR-017) against a database shaped
// exactly like one written by pre-ADR-017 code: execution_attempts still
// has the narrower (run_id, node_id, attempt) primary key, with a real row
// in it. Opening that database with the current code must migrate it
// in place — preserving the existing row's data exactly — and the
// resulting schema must actually accept a second, distinct instance_key
// row for the same (run_id, node_id, attempt), which is the entire point
// of the migration (the old primary key made that a collision).
func TestExecutionAttemptsPrimaryKeyWidenPreservesExistingRows(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "pre-adr017.db")

	// Get a real, fully-migrated database first (every table this test
	// doesn't care about — runs, pipelines, etc. — ends up in its current,
	// correct shape), then downgrade only execution_attempts to the old
	// pre-ADR-017 shape it would have had, so the migration under test has
	// exactly one real thing to do.
	s := openTestStoreAt(t, dbPath)
	seedRunForAttempts(t, s, "pipe-migrate", "run-migrate")
	oldCreated := time.Now().UTC().Truncate(time.Second).Add(-time.Hour)
	if _, err := s.raw().Exec(`DROP TABLE execution_attempts`); err != nil {
		t.Fatalf("drop execution_attempts to simulate pre-migration schema: %v", err)
	}
	if _, err := s.raw().Exec(`CREATE TABLE execution_attempts (
		run_id TEXT NOT NULL,
		node_id TEXT NOT NULL DEFAULT '',
		attempt INTEGER NOT NULL DEFAULT 0,
		status TEXT NOT NULL DEFAULT 'queued',
		claimed_by TEXT NOT NULL DEFAULT '',
		lease_expires_at TEXT,
		fencing_generation INTEGER NOT NULL DEFAULT 0,
		idempotency_key TEXT NOT NULL DEFAULT '',
		error TEXT NOT NULL DEFAULT '',
		created_at TEXT NOT NULL,
		updated_at TEXT NOT NULL,
		PRIMARY KEY (run_id, node_id, attempt),
		FOREIGN KEY (run_id) REFERENCES runs(id) ON DELETE CASCADE)`); err != nil {
		t.Fatalf("recreate old-schema execution_attempts: %v", err)
	}
	if _, err := s.raw().Exec(
		`INSERT INTO execution_attempts (run_id, node_id, attempt, status, claimed_by, lease_expires_at, fencing_generation, idempotency_key, error, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		"run-migrate", "old-node", 0, string(models.AttemptStatusClaimed), "pre-existing-worker",
		oldCreated.Format(timeFormat), int64(3), "run-migrate:old-node", "",
		oldCreated.Format(timeFormat), oldCreated.Format(timeFormat),
	); err != nil {
		t.Fatalf("insert pre-migration row: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("close pre-migration store: %v", err)
	}

	// Reopening runs migrate() again, which must detect the missing
	// instance_key column and rebuild the table.
	migrated, err := NewSQLiteStore(dbPath)
	if err != nil {
		t.Fatalf("reopen after downgrading execution_attempts: %v", err)
	}
	t.Cleanup(func() { migrated.Close() })

	got, err := migrated.GetExecutionAttempt("run-migrate", "old-node", "", 0)
	if err != nil {
		t.Fatalf("GetExecutionAttempt for pre-migration row: %v", err)
	}
	if got.Status != models.AttemptStatusClaimed || got.ClaimedBy != "pre-existing-worker" || got.FencingGeneration != 3 || got.InstanceKey != "" {
		t.Fatalf("pre-migration row after widen = %+v, want status=claimed claimed_by=pre-existing-worker fencing_generation=3 instance_key=\"\" preserved exactly", got)
	}
	if got.IdempotencyKey != "run-migrate:old-node" {
		t.Errorf("idempotency_key = %q, want preserved value", got.IdempotencyKey)
	}

	// The actual point of the migration: a second, distinct instance_key
	// row for the SAME (run_id, node_id, attempt) must now be insertable —
	// the old primary key made this a collision.
	if err := migrated.WithTx(func(tx *sql.Tx) error {
		return migrated.CreateExecutionAttemptTx(tx, &models.ExecutionAttempt{
			RunID: "run-migrate", NodeID: "old-node", InstanceKey: "idx:0", Attempt: 0,
			Status: models.AttemptStatusQueued, IdempotencyKey: "run-migrate:old-node:idx:0",
		})
	}); err != nil {
		t.Fatalf("create instance-keyed row after widen (this is the entire point of the migration): %v", err)
	}
	instanceRow, err := migrated.GetExecutionAttempt("run-migrate", "old-node", "idx:0", 0)
	if err != nil {
		t.Fatalf("GetExecutionAttempt for new instance-keyed row: %v", err)
	}
	if instanceRow.Status != models.AttemptStatusQueued {
		t.Errorf("new instance row status = %s, want queued", instanceRow.Status)
	}
	// The pre-migration row must still be exactly as it was — proving the
	// new row is genuinely independent, not a collision that silently
	// overwrote it.
	stillThere, err := migrated.GetExecutionAttempt("run-migrate", "old-node", "", 0)
	if err != nil {
		t.Fatalf("GetExecutionAttempt for pre-migration row after inserting the instance row: %v", err)
	}
	if stillThere.ClaimedBy != "pre-existing-worker" {
		t.Errorf("pre-migration row was disturbed by inserting a sibling instance row: %+v", stillThere)
	}

	// Idempotency: reopening a database that already has instance_key must
	// be a pure no-op — no error, no data loss, no second rebuild attempt.
	if err := migrated.Close(); err != nil {
		t.Fatalf("close migrated store: %v", err)
	}
	reopened, err := NewSQLiteStore(dbPath)
	if err != nil {
		t.Fatalf("reopen already-migrated database: %v", err)
	}
	defer reopened.Close()
	again, err := reopened.GetExecutionAttempt("run-migrate", "old-node", "", 0)
	if err != nil {
		t.Fatalf("GetExecutionAttempt after second reopen: %v", err)
	}
	if again.ClaimedBy != "pre-existing-worker" {
		t.Errorf("row changed across a no-op reopen: %+v", again)
	}
}

// openTestStoreAt is newTestStore but at a caller-chosen path, so this file
// can reopen the same on-disk database after mutating it directly.
func openTestStoreAt(t *testing.T, dbPath string) *SQLiteStore {
	t.Helper()
	s, err := NewSQLiteStore(dbPath)
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}
	return s
}

// raw exposes the underlying *sql.DB for tests that need to manipulate
// schema directly (simulating a pre-migration database) — package-internal
// only, not part of any exported contract.
func (s *SQLiteStore) raw() *sql.DB { return s.db }
