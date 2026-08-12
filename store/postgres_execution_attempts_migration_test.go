package store

import (
	"context"
	"database/sql"
	"os"
	"testing"
	"time"

	"github.com/Tnsor-Labs/brokoli/models"
)

// openTestPostgresStore connects to a real local Postgres instance for the
// execution_attempts primary-key-widen migration test (ADR-017) — same
// live-or-skip convention as openTestPostgres (postgres_leader_test.go),
// a dedicated database so this doesn't collide with the leader-election
// tests' own schema/rows.
func openTestPostgresStore(t *testing.T) *PostgresStore {
	t.Helper()
	dsn := os.Getenv("BROKOLI_TEST_POSTGRES_EXECATTEMPT_URL")
	if dsn == "" {
		dsn = "postgres://postgres@localhost:5432/brokoli_execattempt_test?sslmode=disable"
	}
	probe, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Skipf("skipping live-Postgres execution_attempts migration test: sql.Open: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := probe.PingContext(ctx); err != nil {
		probe.Close()
		t.Skipf("skipping live-Postgres execution_attempts migration test: no reachable Postgres at %s: %v", dsn, err)
	}
	probe.Close()

	s, err := NewPostgresStore(dsn)
	if err != nil {
		t.Fatalf("NewPostgresStore: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

// TestPostgresExecutionAttemptsPrimaryKeyWidenPreservesExistingRows is the
// Postgres counterpart to the SQLite version of this test
// (execution_attempts_migration_test.go): proves
// widenExecutionAttemptsPrimaryKey against a database shaped like one
// written by pre-ADR-017 code — execution_attempts still has the narrower
// (run_id, node_id, attempt) primary key (Postgres's default-named
// execution_attempts_pkey constraint) — and that reopening with current
// code migrates it in place without losing the existing row, then actually
// accepts the second, distinct instance_key row the old constraint would
// have collided on.
func TestPostgresExecutionAttemptsPrimaryKeyWidenPreservesExistingRows(t *testing.T) {
	s := openTestPostgresStore(t)

	// Clean slate for this specific run/pipeline pair — cascades to
	// execution_attempts via its FK, so no leftover rows from a prior test
	// invocation against this persistent database confuse the assertions
	// below.
	if _, err := s.db.Exec(`DELETE FROM runs WHERE id = 'run-pg-migrate'`); err != nil {
		t.Fatalf("reset prior test state: %v", err)
	}
	if _, err := s.db.Exec(`DELETE FROM pipelines WHERE id = 'pipe-pg-migrate'`); err != nil {
		t.Fatalf("reset prior test state: %v", err)
	}

	now := time.Now().UTC().Truncate(time.Millisecond)
	if err := s.CreatePipeline(&models.Pipeline{ID: "pipe-pg-migrate", Name: "PG Migrate Test", CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatalf("CreatePipeline: %v", err)
	}
	if err := s.CreateRun(&models.Run{ID: "run-pg-migrate", PipelineID: "pipe-pg-migrate", Status: models.RunStatusRunning, StartedAt: &now}); err != nil {
		t.Fatalf("CreateRun: %v", err)
	}

	oldCreated := now.Add(-time.Hour)
	if _, err := s.db.Exec(`ALTER TABLE execution_attempts DROP CONSTRAINT execution_attempts_pkey`); err != nil {
		t.Fatalf("drop current primary key to simulate pre-migration schema: %v", err)
	}
	if _, err := s.db.Exec(`ALTER TABLE execution_attempts DROP COLUMN IF EXISTS instance_key`); err != nil {
		t.Fatalf("drop instance_key to simulate pre-migration schema: %v", err)
	}
	if _, err := s.db.Exec(`ALTER TABLE execution_attempts ADD PRIMARY KEY (run_id, node_id, attempt)`); err != nil {
		t.Fatalf("add old-shape primary key: %v", err)
	}
	if _, err := s.db.Exec(
		`INSERT INTO execution_attempts (run_id, node_id, attempt, status, claimed_by, lease_expires_at, fencing_generation, idempotency_key, error, created_at, updated_at)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)`,
		"run-pg-migrate", "old-node", 0, string(models.AttemptStatusClaimed), "pre-existing-worker",
		oldCreated, int64(3), "run-pg-migrate:old-node", "", oldCreated, oldCreated,
	); err != nil {
		t.Fatalf("insert pre-migration row: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("close pre-migration store: %v", err)
	}

	dsn := os.Getenv("BROKOLI_TEST_POSTGRES_EXECATTEMPT_URL")
	if dsn == "" {
		dsn = "postgres://postgres@localhost:5432/brokoli_execattempt_test?sslmode=disable"
	}
	migrated, err := NewPostgresStore(dsn)
	if err != nil {
		t.Fatalf("reopen after downgrading execution_attempts: %v", err)
	}
	t.Cleanup(func() { migrated.Close() })

	got, err := migrated.GetExecutionAttempt("run-pg-migrate", "old-node", "", 0)
	if err != nil {
		t.Fatalf("GetExecutionAttempt for pre-migration row: %v", err)
	}
	if got.Status != models.AttemptStatusClaimed || got.ClaimedBy != "pre-existing-worker" || got.FencingGeneration != 3 || got.InstanceKey != "" {
		t.Fatalf("pre-migration row after widen = %+v, want status=claimed claimed_by=pre-existing-worker fencing_generation=3 instance_key=\"\" preserved exactly", got)
	}

	if err := migrated.WithTx(func(tx *sql.Tx) error {
		return migrated.CreateExecutionAttemptTx(tx, &models.ExecutionAttempt{
			RunID: "run-pg-migrate", NodeID: "old-node", InstanceKey: "idx:0", Attempt: 0,
			Status: models.AttemptStatusQueued, IdempotencyKey: "run-pg-migrate:old-node:idx:0",
		})
	}); err != nil {
		t.Fatalf("create instance-keyed row after widen (this is the entire point of the migration): %v", err)
	}
	instanceRow, err := migrated.GetExecutionAttempt("run-pg-migrate", "old-node", "idx:0", 0)
	if err != nil {
		t.Fatalf("GetExecutionAttempt for new instance-keyed row: %v", err)
	}
	if instanceRow.Status != models.AttemptStatusQueued {
		t.Errorf("new instance row status = %s, want queued", instanceRow.Status)
	}
	stillThere, err := migrated.GetExecutionAttempt("run-pg-migrate", "old-node", "", 0)
	if err != nil {
		t.Fatalf("GetExecutionAttempt for pre-migration row after inserting the instance row: %v", err)
	}
	if stillThere.ClaimedBy != "pre-existing-worker" {
		t.Errorf("pre-migration row was disturbed by inserting a sibling instance row: %+v", stillThere)
	}
}
