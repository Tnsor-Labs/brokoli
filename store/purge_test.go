package store

import (
	"testing"
	"time"

	"github.com/Tnsor-Labs/brokoli/models"
)

// createRunAt creates the pipeline row (if not already created in this
// store) and a run under it with the given started_at and org, satisfying
// the runs.pipeline_id foreign key. orgID is set directly via
// models.Run.OrgID — CreateRun persists it for real (Tnsor-Labs/brokoli#50)
// — so these tests exercise the actual write path, not a raw-SQL stand-in
// for a field that didn't exist yet.
func createRunAt(t *testing.T, s *SQLiteStore, id, pipelineID, orgID string, startedAt time.Time) {
	t.Helper()
	if _, err := s.GetPipeline(pipelineID); err != nil {
		now := time.Now().UTC().Truncate(time.Millisecond)
		if err := s.CreatePipeline(&models.Pipeline{ID: pipelineID, Name: "Purge Test Pipeline", OrgID: orgID, CreatedAt: now, UpdatedAt: now}); err != nil {
			t.Fatalf("create pipeline %s: %v", pipelineID, err)
		}
	}
	if err := s.CreateRun(&models.Run{ID: id, PipelineID: pipelineID, Status: models.RunStatusSuccess, StartedAt: &startedAt, OrgID: orgID}); err != nil {
		t.Fatalf("create run %s: %v", id, err)
	}
}

func TestListRunIDsOlderThan_MatchesPurgeRunsOlderThan(t *testing.T) {
	s := newTestStore(t)
	old := time.Now().AddDate(0, 0, -60)
	recent := time.Now().AddDate(0, 0, -1)

	createRunAt(t, s, "run-old-1", "p1", "", old)
	createRunAt(t, s, "run-old-2", "p1", "", old)
	createRunAt(t, s, "run-recent", "p1", "", recent)

	ids, err := s.ListRunIDsOlderThan(30)
	if err != nil {
		t.Fatalf("ListRunIDsOlderThan: %v", err)
	}
	if len(ids) != 2 {
		t.Fatalf("got %d run IDs, want 2: %v", len(ids), ids)
	}
	got := map[string]bool{}
	for _, id := range ids {
		got[id] = true
	}
	if !got["run-old-1"] || !got["run-old-2"] {
		t.Errorf("expected run-old-1 and run-old-2, got %v", ids)
	}
	if got["run-recent"] {
		t.Errorf("run-recent should not be listed, got %v", ids)
	}

	// The whole point of this method: it must agree with what Purge
	// actually deletes, since systemPurge calls List before Purge and
	// trusts the IDs it got back.
	deleted, err := s.PurgeRunsOlderThan(30)
	if err != nil {
		t.Fatalf("PurgeRunsOlderThan: %v", err)
	}
	if int(deleted) != len(ids) {
		t.Errorf("PurgeRunsOlderThan deleted %d rows, ListRunIDsOlderThan found %d — they must agree", deleted, len(ids))
	}
}

func TestListRunIDsOlderThanByOrg_ScopesByOrg(t *testing.T) {
	s := newTestStore(t)
	old := time.Now().AddDate(0, 0, -60)

	createRunAt(t, s, "run-org-a", "p-org-a", "org-a", old)
	createRunAt(t, s, "run-org-b", "p-org-b", "org-b", old)

	ids, err := s.ListRunIDsOlderThanByOrg(30, "org-a")
	if err != nil {
		t.Fatalf("ListRunIDsOlderThanByOrg: %v", err)
	}
	if len(ids) != 1 || ids[0] != "run-org-a" {
		t.Fatalf("got %v, want exactly [run-org-a]", ids)
	}

	deleted, err := s.PurgeRunsOlderThanByOrg(30, "org-a")
	if err != nil {
		t.Fatalf("PurgeRunsOlderThanByOrg: %v", err)
	}
	if deleted != 1 {
		t.Errorf("PurgeRunsOlderThanByOrg deleted %d rows, want 1", deleted)
	}

	// org-b's run must be untouched by either call.
	if _, err := s.GetRun("run-org-b"); err != nil {
		t.Errorf("run-org-b should still exist: %v", err)
	}
}

func TestListRunIDsOlderThan_NoMatches(t *testing.T) {
	s := newTestStore(t)
	createRunAt(t, s, "run-recent", "p1", "", time.Now())

	ids, err := s.ListRunIDsOlderThan(30)
	if err != nil {
		t.Fatalf("ListRunIDsOlderThan: %v", err)
	}
	if len(ids) != 0 {
		t.Errorf("got %v, want no results", ids)
	}
}

// TestCreateRun_PersistsOrgID is the core regression test for
// Tnsor-Labs/brokoli#50: CreateRun must actually write org_id, and GetRun
// must read it back — before this fix, models.Run had no OrgID field at
// all, so every run silently got the schema default regardless of which
// org it belonged to.
func TestCreateRun_PersistsOrgID(t *testing.T) {
	s := newTestStore(t)
	now := time.Now().UTC()
	if err := s.CreatePipeline(&models.Pipeline{ID: "p-acme", Name: "Acme Pipeline", OrgID: "acme-corp", CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatalf("create pipeline: %v", err)
	}
	if err := s.CreateRun(&models.Run{ID: "run-1", PipelineID: "p-acme", Status: models.RunStatusSuccess, StartedAt: &now, OrgID: "acme-corp"}); err != nil {
		t.Fatalf("create run: %v", err)
	}

	got, err := s.GetRun("run-1")
	if err != nil {
		t.Fatalf("get run: %v", err)
	}
	if got.OrgID != "acme-corp" {
		t.Errorf("GetRun(run-1).OrgID = %q, want %q", got.OrgID, "acme-corp")
	}
}

// TestCreateRun_EmptyOrgIDPersistsAsEmpty covers the community/single-tenant
// case: a pipeline with no org (OrgID == "", the convention this codebase
// already uses elsewhere for "no org" — see requirePipelineOrg in
// api/handlers_pipeline.go) must produce a run with org_id == "", not the
// schema's 'default' placeholder. Community-edition purge
// (systemPurge/PurgeRunsOlderThan) never filters by org_id at all, so this
// doesn't affect that path — but it keeps runs.org_id's real-world values
// consistent with pipelines.org_id's own convention instead of introducing
// a second, mismatched sentinel.
func TestCreateRun_EmptyOrgIDPersistsAsEmpty(t *testing.T) {
	s := newTestStore(t)
	now := time.Now().UTC()
	if err := s.CreatePipeline(&models.Pipeline{ID: "p-community", Name: "Community Pipeline", CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatalf("create pipeline: %v", err)
	}
	if err := s.CreateRun(&models.Run{ID: "run-1", PipelineID: "p-community", Status: models.RunStatusSuccess, StartedAt: &now}); err != nil {
		t.Fatalf("create run: %v", err)
	}

	got, err := s.GetRun("run-1")
	if err != nil {
		t.Fatalf("get run: %v", err)
	}
	if got.OrgID != "" {
		t.Errorf("GetRun(run-1).OrgID = %q, want empty (community/no-org convention)", got.OrgID)
	}
}

// TestMigration_BackfillsRunOrgIDFromPipeline simulates a run created
// before this fix (org_id still the schema default 'default', bypassing
// CreateRun entirely — the same state every pre-fix row is actually in)
// and confirms re-running migrate() (which happens on every store open)
// backfills it from the owning pipeline's real org_id.
//
// The backfill's COALESCE-to-existing-value fallback for a run whose
// pipeline no longer exists isn't exercised here: runs.pipeline_id has
// ON DELETE CASCADE, so a genuinely orphaned run (pipeline gone, run
// still present) can't actually occur through normal deletion in this
// schema — the fallback is defensive, not a reachable real-world case.
func TestMigration_BackfillsRunOrgIDFromPipeline(t *testing.T) {
	dir := t.TempDir()
	dbPath := dir + "/backfill.db"
	s, err := NewSQLiteStore(dbPath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}

	now := time.Now().UTC()
	if err := s.CreatePipeline(&models.Pipeline{ID: "p-acme", Name: "Acme Pipeline", OrgID: "acme-corp", CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatalf("create pipeline: %v", err)
	}
	// Simulate a legacy row: insert directly, bypassing CreateRun, so
	// org_id lands on the bare schema default exactly like every run
	// created before this fix did.
	if _, err := s.db.Exec(
		`INSERT INTO runs (id, pipeline_id, status, started_at, finished_at, trace_id, error, params, pipeline_version, resumed_from_run_id) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		"run-legacy", "p-acme", string(models.RunStatusSuccess), now.Format(timeFormat), "", "", "", "{}", 0, "",
	); err != nil {
		t.Fatalf("insert legacy run: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}

	// Reopen — migrate() runs again on every open, which is where the
	// backfill lives.
	s2, err := NewSQLiteStore(dbPath)
	if err != nil {
		t.Fatalf("reopen store: %v", err)
	}
	defer s2.Close()

	got, err := s2.GetRun("run-legacy")
	if err != nil {
		t.Fatalf("get legacy run: %v", err)
	}
	if got.OrgID != "acme-corp" {
		t.Errorf("run-legacy.OrgID = %q after backfill, want %q (from its pipeline)", got.OrgID, "acme-corp")
	}
}

// TestGetRunCalendarByOrg_MatchesRealOrg is the third of the three methods
// issue #50 named as broken (alongside PurgeRunsOlderThanByOrg and
// ListRunIDsOlderThanByOrg): before this fix, no run ever had a real
// org_id, so this always returned an empty calendar for any org that
// wasn't the literal string 'default'.
func TestGetRunCalendarByOrg_MatchesRealOrg(t *testing.T) {
	s := newTestStore(t)
	now := time.Now()

	createRunAt(t, s, "run-org-a", "p-org-a", "org-a", now)
	createRunAt(t, s, "run-org-b", "p-org-b", "org-b", now)

	days, err := s.GetRunCalendarByOrg(7, "org-a")
	if err != nil {
		t.Fatalf("GetRunCalendarByOrg: %v", err)
	}
	var total int
	for _, d := range days {
		total += d.Total
	}
	if total != 1 {
		t.Errorf("GetRunCalendarByOrg(org-a) total = %d, want 1 (org-b's run must not count)", total)
	}
}
