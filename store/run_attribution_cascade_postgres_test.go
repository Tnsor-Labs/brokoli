package store

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/Tnsor-Labs/brokoli/models"
)

// Live-Postgres coverage of the attribution cascade, same
// skip-if-unreachable discipline as the other Postgres tests here.
//
// The Postgres branch of the migration is a different mechanism from
// SQLite's -- ALTER TABLE ADD CONSTRAINT rather than a table rebuild --
// and neither exercises the other. A dialect covered only by its
// sibling's test is the shape that broke the enterprise invite feature:
// the SQLite path worked, the Postgres path had never run, and nobody
// found out until a deployment did.
func openAttributionTestPostgresStore(t *testing.T) *PostgresStore {
	t.Helper()
	dsn := os.Getenv("BROKOLI_TEST_POSTGRES_URL")
	if dsn == "" {
		dsn = "postgres://postgres@localhost:5432/brokoli_leader_test?sslmode=disable"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	s, err := NewPostgresStore(dsn)
	if err != nil {
		t.Skipf("skipping live-Postgres attribution cascade test: %v", err)
	}
	if err := s.db.PingContext(ctx); err != nil {
		s.Close()
		t.Skipf("skipping live-Postgres attribution cascade test: no reachable Postgres at %s", dsn)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestPostgresPurgeRemovesAttribution(t *testing.T) {
	s := openAttributionTestPostgresStore(t)

	suffix := time.Now().UnixNano()
	pipelineID := fmt.Sprintf("pg-attrib-p-%d", suffix)
	oldRun := fmt.Sprintf("pg-attrib-old-%d", suffix)
	newRun := fmt.Sprintf("pg-attrib-new-%d", suffix)
	orgID := fmt.Sprintf("pg-attrib-org-%d", suffix)

	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, _ = s.db.ExecContext(ctx, `DELETE FROM runs WHERE pipeline_id = $1`, pipelineID)
		_, _ = s.db.ExecContext(ctx, `DELETE FROM pipelines WHERE id = $1`, pipelineID)
	})

	if err := s.CreatePipeline(&models.Pipeline{
		ID: pipelineID, Name: pipelineID,
		CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("create pipeline: %v", err)
	}

	old := time.Now().UTC().AddDate(0, 0, -90)
	now := time.Now().UTC()
	for id, started := range map[string]*time.Time{oldRun: &old, newRun: &now} {
		if err := s.CreateRun(&models.Run{
			ID: id, PipelineID: pipelineID,
			Status: models.RunStatusSuccess, StartedAt: started,
		}); err != nil {
			t.Fatalf("create run %s: %v", id, err)
		}
		if _, err := s.db.Exec(`UPDATE runs SET org_id = $1 WHERE id = $2`, orgID, id); err != nil {
			t.Fatalf("stamping the org: %v", err)
		}
		if err := s.SetRunAttribution(id, &models.RunAttribution{
			Kind: models.RunTriggerKindUser, UserID: "u1", UserName: "someone",
		}); err != nil {
			t.Fatalf("set attribution for %s: %v", id, err)
		}
	}

	count := func() int {
		t.Helper()
		var n int
		if err := s.db.QueryRow(
			`SELECT COUNT(*) FROM run_attribution WHERE run_id IN ($1, $2)`, oldRun, newRun).Scan(&n); err != nil {
			t.Fatalf("counting: %v", err)
		}
		return n
	}
	if got := count(); got != 2 {
		t.Fatalf("seeded %d attribution row(s), want 2", got)
	}

	if _, err := s.PurgeRunsOlderThanByOrg(30, orgID); err != nil {
		t.Fatalf("purge: %v", err)
	}

	if got := count(); got != 1 {
		t.Errorf("%d attribution row(s) remain, want 1. On Postgres the constraint is added by "+
			"ALTER TABLE, which silently does nothing if it failed; this is what proves it did not.", got)
	}

	attrib, err := s.GetRunAttribution([]string{newRun})
	if err != nil {
		t.Fatalf("get attribution: %v", err)
	}
	if _, ok := attrib[newRun]; !ok {
		t.Error("the surviving run lost its attribution")
	}
}

// The constraint is present after migration, checked through the
// catalogue as well as through behaviour.
//
// Behaviour is the real test above; this one names the constraint, so a
// failure says "the ALTER TABLE did not apply" rather than leaving
// somebody to work out why rows vanished or did not.
func TestPostgresAttributionHasItsForeignKey(t *testing.T) {
	s := openAttributionTestPostgresStore(t)

	var n int
	if err := s.db.QueryRow(`
		SELECT COUNT(*) FROM information_schema.table_constraints
		WHERE table_name = 'run_attribution' AND constraint_type = 'FOREIGN KEY'`).Scan(&n); err != nil {
		t.Fatalf("reading the catalogue: %v", err)
	}
	if n == 0 {
		t.Error("run_attribution has no foreign key on Postgres; the ALTER TABLE in " +
			"addRunAttributionForeignKey did not apply, and rows will outlive their runs")
	}
}

// The migration branch, not the fresh-create branch.
//
// Every test above passes on a database where CREATE TABLE built the
// constraint in the first place, which proves nothing about the
// deployments that matter: they all have the constraint-free table
// already. So this puts one back and re-runs the migration.
func TestPostgresMigratesAPreExistingTable(t *testing.T) {
	s := openAttributionTestPostgresStore(t)

	orphan := fmt.Sprintf("pg-ghost-%d", time.Now().UnixNano())
	t.Cleanup(func() {
		_, _ = s.db.Exec(`DELETE FROM run_attribution WHERE run_id = $1`, orphan)
		// Leave the constraint as the product expects to find it,
		// whatever this test did to it.
		addRunAttributionForeignKey(s.db, "postgres")
	})

	// Put the database back into its pre-fix shape: no constraint, and an
	// orphan of the kind that accumulated while nothing cleared them.
	if _, err := s.db.Exec(`ALTER TABLE run_attribution DROP CONSTRAINT IF EXISTS run_attribution_run_id_fkey`); err != nil {
		t.Fatalf("dropping the constraint: %v", err)
	}
	if _, err := s.db.Exec(
		`INSERT INTO run_attribution (run_id, kind) VALUES ($1, 'user') ON CONFLICT DO NOTHING`, orphan); err != nil {
		t.Fatalf("inserting the orphan: %v", err)
	}

	constraints := func() int {
		t.Helper()
		var n int
		if err := s.db.QueryRow(`
			SELECT COUNT(*) FROM information_schema.table_constraints
			WHERE table_name = 'run_attribution' AND constraint_type = 'FOREIGN KEY'`).Scan(&n); err != nil {
			t.Fatalf("reading the catalogue: %v", err)
		}
		return n
	}
	if got := constraints(); got != 0 {
		t.Fatalf("the pre-fix state has %d constraint(s); this test is not testing the migration", got)
	}

	// What boot does.
	addRunAttributionForeignKey(s.db, "postgres")

	if got := constraints(); got != 1 {
		t.Errorf("after migration there are %d foreign key(s), want 1", got)
	}
	var ghosts int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM run_attribution WHERE run_id = $1`, orphan).Scan(&ghosts); err != nil {
		t.Fatalf("counting orphans: %v", err)
	}
	if ghosts != 0 {
		t.Errorf("the orphan survived the migration; ADD CONSTRAINT would have failed on it")
	}

	// Idempotent: boot happens more than once.
	addRunAttributionForeignKey(s.db, "postgres")
	if got := constraints(); got != 1 {
		t.Errorf("running the migration twice left %d constraint(s), want 1", got)
	}
}
