package store

import (
	"testing"
	"time"

	"github.com/Tnsor-Labs/brokoli/models"
)

// createRunAt creates the pipeline row (if not already created in this
// store) and a run under it with the given started_at, satisfying the
// runs.pipeline_id foreign key. orgID is applied via a raw UPDATE after
// creation because models.Run has no OrgID field and CreateRun's INSERT
// never sets one — every run gets runs.org_id's DEFAULT ('default') today,
// regardless of which org actually owns it. That's a real, separate gap
// (tracked in a follow-up issue) from what this file tests: these tests
// verify ListRunIDsOlderThanByOrg's/PurgeRunsOlderThanByOrg's WHERE clause
// is correct GIVEN a populated org_id, not that org_id gets populated
// correctly in practice today.
func createRunAt(t *testing.T, s *SQLiteStore, id, pipelineID, orgID string, startedAt time.Time) {
	t.Helper()
	if _, err := s.GetPipeline(pipelineID); err != nil {
		now := time.Now().UTC().Truncate(time.Millisecond)
		if err := s.CreatePipeline(&models.Pipeline{ID: pipelineID, Name: "Purge Test Pipeline", CreatedAt: now, UpdatedAt: now}); err != nil {
			t.Fatalf("create pipeline %s: %v", pipelineID, err)
		}
	}
	if err := s.CreateRun(&models.Run{ID: id, PipelineID: pipelineID, Status: models.RunStatusSuccess, StartedAt: &startedAt}); err != nil {
		t.Fatalf("create run %s: %v", id, err)
	}
	if orgID != "" {
		if _, err := s.db.Exec(`UPDATE runs SET org_id = ? WHERE id = ?`, orgID, id); err != nil {
			t.Fatalf("set org_id for run %s: %v", id, err)
		}
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

	createRunAt(t, s, "run-org-a", "p1", "org-a", old)
	createRunAt(t, s, "run-org-b", "p1", "org-b", old)

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
