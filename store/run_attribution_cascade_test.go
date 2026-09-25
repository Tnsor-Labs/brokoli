package store

import (
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"github.com/Tnsor-Labs/brokoli/models"
)

// Deleting a run takes its attribution with it.
//
// The first version of this table had no foreign key and documented
// DeleteRunAttribution as the thing that would clear orphans "alongside
// the runs they belong to". Nothing in the product called it, so nothing
// cleared them: every attribution row written since the feature shipped
// outlived its run permanently.
//
// The comment asserting the cleanup was the only place the cleanup
// existed. These tests are here so that stays impossible: they exercise
// deletion rather than reading the schema, because a constraint that is
// declared and not enforced -- foreign keys off, a rebuild that silently
// failed -- looks identical to one that works if you only read the DDL.

func attributionTestStore(t *testing.T) *SQLiteStore {
	t.Helper()
	s, err := NewSQLiteStore(filepath.Join(t.TempDir(), "attrib.db"))
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func seedRunWithAttribution(t *testing.T, s *SQLiteStore, runID, pipelineID string, startedAt time.Time) {
	t.Helper()
	if existing, _ := s.GetPipeline(pipelineID); existing == nil {
		if err := s.CreatePipeline(&models.Pipeline{
			ID: pipelineID, Name: pipelineID,
			CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
		}); err != nil {
			t.Fatalf("create pipeline: %v", err)
		}
	}
	if err := s.CreateRun(&models.Run{
		ID: runID, PipelineID: pipelineID,
		Status: models.RunStatusSuccess, StartedAt: &startedAt,
	}); err != nil {
		t.Fatalf("create run: %v", err)
	}
	if err := s.SetRunAttribution(runID, &models.RunAttribution{
		Kind: models.RunTriggerKindUser, UserID: "u1", UserName: "someone",
	}); err != nil {
		t.Fatalf("set attribution: %v", err)
	}
}

func attributionRowCount(t *testing.T, s *SQLiteStore) int {
	t.Helper()
	db, ok := s.RawDB().(*sql.DB)
	if !ok {
		t.Fatal("RawDB did not return *sql.DB")
	}
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM run_attribution`).Scan(&n); err != nil {
		t.Fatalf("counting attribution rows: %v", err)
	}
	return n
}

func TestPurgingARunRemovesItsAttribution(t *testing.T) {
	s := attributionTestStore(t)

	old := time.Now().UTC().AddDate(0, 0, -90)
	seedRunWithAttribution(t, s, "old-run", "p1", old)
	seedRunWithAttribution(t, s, "new-run", "p1", time.Now().UTC())

	if got := attributionRowCount(t, s); got != 2 {
		t.Fatalf("seeded %d attribution row(s), want 2", got)
	}

	purged, err := s.PurgeRunsOlderThan(30)
	if err != nil {
		t.Fatalf("purge: %v", err)
	}
	if purged != 1 {
		t.Fatalf("purged %d run(s), want 1", purged)
	}

	if got := attributionRowCount(t, s); got != 1 {
		t.Errorf("%d attribution row(s) remain, want 1. The purged run's attribution "+
			"outlived it, which is the orphan leak the foreign key exists to stop.", got)
	}

	// The surviving run keeps its attribution: a cascade that took both
	// would be a different bug with the same row count as no cascade at
	// all on a one-run test.
	attrib, err := s.GetRunAttribution([]string{"new-run"})
	if err != nil {
		t.Fatalf("get attribution: %v", err)
	}
	if _, ok := attrib["new-run"]; !ok {
		t.Error("the surviving run lost its attribution")
	}
}

// The org-scoped purge is a separate query and the one enterprise
// retention actually calls, so it gets its own test rather than being
// assumed to behave like its sibling.
func TestPurgingByOrgRemovesAttribution(t *testing.T) {
	s := attributionTestStore(t)
	db := s.RawDB().(*sql.DB)

	old := time.Now().UTC().AddDate(0, 0, -90)
	seedRunWithAttribution(t, s, "org-run", "p1", old)
	if _, err := db.Exec(`UPDATE runs SET org_id = ? WHERE id = ?`, "org-1", "org-run"); err != nil {
		t.Fatalf("stamping the org: %v", err)
	}

	if _, err := s.PurgeRunsOlderThanByOrg(30, "org-1"); err != nil {
		t.Fatalf("purge by org: %v", err)
	}
	if got := attributionRowCount(t, s); got != 0 {
		t.Errorf("%d attribution row(s) survived the org purge, want 0", got)
	}
}

// A database created before the foreign key existed is migrated, and its
// existing orphans are cleared rather than carried forward.
//
// This is the case that actually matters: every deployment running today
// has the constraint-free table, so a fix that only helps fresh installs
// helps nobody who already has the problem.
func TestAnOldTableIsMigratedAndItsOrphansCleared(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.db")

	// Build the pre-fix table by hand, then put an orphan in it.
	s, err := NewSQLiteStore(path)
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	db := s.RawDB().(*sql.DB)
	for _, stmt := range []string{
		`DROP TABLE run_attribution`,
		`CREATE TABLE run_attribution (
			run_id TEXT PRIMARY KEY, kind TEXT NOT NULL,
			user_id TEXT NOT NULL DEFAULT '', user_name TEXT NOT NULL DEFAULT '',
			token_name TEXT NOT NULL DEFAULT ''
		)`,
		`INSERT INTO run_attribution (run_id, kind) VALUES ('ghost-run', 'user')`,
	} {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatalf("building the legacy table: %v", err)
		}
	}
	// A row whose run does exist, to prove the migration copies rather
	// than just empties.
	seedRunWithAttribution(t, s, "real-run", "p1", time.Now().UTC())
	if got := attributionRowCount(t, s); got != 2 {
		t.Fatalf("legacy table holds %d row(s), want 2", got)
	}
	s.Close()

	// Reopen: the migration runs on boot.
	s2, err := NewSQLiteStore(path)
	if err != nil {
		t.Fatalf("reopening: %v", err)
	}
	defer s2.Close()

	if got := attributionRowCount(t, s2); got != 1 {
		t.Errorf("after migration %d row(s) remain, want 1 (the orphan cleared, the real one kept)", got)
	}
	attrib, err := s2.GetRunAttribution([]string{"real-run"})
	if err != nil {
		t.Fatalf("get attribution: %v", err)
	}
	if _, ok := attrib["real-run"]; !ok {
		t.Error("the migration dropped a row whose run still exists")
	}

	// And the constraint is now real, tested by deleting rather than by
	// reading the schema.
	db2 := s2.RawDB().(*sql.DB)
	if _, err := db2.Exec(`DELETE FROM runs WHERE id = ?`, "real-run"); err != nil {
		t.Fatalf("deleting the run: %v", err)
	}
	if got := attributionRowCount(t, s2); got != 0 {
		t.Errorf("%d row(s) survived deleting their run; the migrated table has no working cascade", got)
	}
}

// Running the migration twice must not rebuild the table again or lose
// anything. Boot happens more than once.
func TestTheMigrationIsIdempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "twice.db")
	s, err := NewSQLiteStore(path)
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	seedRunWithAttribution(t, s, "r1", "p1", time.Now().UTC())
	s.Close()

	for i := 0; i < 3; i++ {
		s2, err := NewSQLiteStore(path)
		if err != nil {
			t.Fatalf("reopen %d: %v", i, err)
		}
		if got := attributionRowCount(t, s2); got != 1 {
			t.Fatalf("reopen %d: %d row(s), want 1", i, got)
		}
		s2.Close()
	}
}
