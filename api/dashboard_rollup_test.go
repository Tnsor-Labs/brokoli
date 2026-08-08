package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/Tnsor-Labs/brokoli/models"
	"github.com/Tnsor-Labs/brokoli/store"
)

func newDashboardTestStore(t *testing.T) store.Store {
	t.Helper()
	s, err := store.NewSQLiteStore(filepath.Join(t.TempDir(), "dashboard.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

// seedRuns creates a pipeline and n runs for it, all started `age` ago.
func seedRuns(t *testing.T, s store.Store, pipelineID, name string, n int, status models.RunStatus, age time.Duration) {
	t.Helper()
	if err := s.CreatePipeline(&models.Pipeline{
		ID: pipelineID, Name: name, Enabled: true,
		WorkspaceID: models.DefaultWorkspaceID,
		Nodes:       []models.Node{{ID: "s1", Type: models.NodeTypeSourceFile, Name: "src"}},
		CreatedAt:   time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("create pipeline: %v", err)
	}
	started := time.Now().UTC().Add(-age)
	for i := 0; i < n; i++ {
		finished := started.Add(time.Second)
		if err := s.CreateRun(&models.Run{
			ID:         pipelineID + "-run-" + string(rune('a'+i)),
			PipelineID: pipelineID,
			Status:     status,
			StartedAt:  &started,
			FinishedAt: &finished,
		}); err != nil {
			t.Fatalf("create run: %v", err)
		}
	}
}

func getDashboard(t *testing.T, s store.Store) map[string]interface{} {
	t.Helper()
	rec := httptest.NewRecorder()
	dashboardHandler(s)(rec, httptest.NewRequest(http.MethodGet, "/dashboard", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body: %s", rec.Code, rec.Body.String())
	}
	var out map[string]interface{}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return out
}

// The counterpart to pkg/sodp's eviction test: runs an hour old are long
// past the live-state TTL, but /api/dashboard reads the database, so they
// still count. This is why the dashboard reads its figures from here.
func TestDashboardHandler_CountsRunsOlderThanTheEvictionTTL(t *testing.T) {
	s := newDashboardTestStore(t)
	seedRuns(t, s, "pipe-1", "hourly-sync", 3, models.RunStatusSuccess, time.Hour)

	d := getDashboard(t, s)
	if got := d["runs_24h_total"]; got != float64(3) {
		t.Errorf("runs_24h_total = %v, want 3 — hour-old runs are well inside 24h", got)
	}
	if got := d["runs_today"]; got != float64(3) {
		t.Errorf("runs_today = %v, want 3", got)
	}
}

// A grouped runs list needs the true per-pipeline count. Counting entries in
// the recent_runs sample would report the sample size (capped at 50), so a
// pipeline run far more than that would be badly understated.
func TestDashboardHandler_PipelineRollupsExceedTheRecentRunsSample(t *testing.T) {
	s := newDashboardTestStore(t)
	const runCount = 80 // deliberately larger than the 50-entry sample
	seedRuns(t, s, "pipe-1", "chatty", runCount, models.RunStatusSuccess, time.Minute)

	d := getDashboard(t, s)

	sample, _ := d["recent_runs"].([]interface{})
	if len(sample) > 50 {
		t.Fatalf("recent_runs = %d, expected it to stay a bounded sample", len(sample))
	}

	rollups, _ := d["pipeline_rollups"].([]interface{})
	if len(rollups) != 1 {
		t.Fatalf("pipeline_rollups = %d entries, want 1", len(rollups))
	}
	ru, _ := rollups[0].(map[string]interface{})
	if ru["name"] != "chatty" {
		t.Errorf("name = %v, want chatty", ru["name"])
	}
	if ru["total"] != float64(runCount) {
		t.Errorf("total = %v, want %d — the rollup must count every run, not the sample", ru["total"], runCount)
	}
	if ru["success"] != float64(runCount) {
		t.Errorf("success = %v, want %d", ru["success"], runCount)
	}
	// The whole point: the true count exceeds what the sample could show.
	if ru["total"].(float64) <= float64(len(sample)) {
		t.Errorf("rollup total (%v) should exceed the sample size (%d)", ru["total"], len(sample))
	}
}

func TestDashboardHandler_PipelineRollupsSplitSuccessAndFailure(t *testing.T) {
	s := newDashboardTestStore(t)
	seedRuns(t, s, "pipe-ok", "steady", 4, models.RunStatusSuccess, time.Minute)
	seedRuns(t, s, "pipe-bad", "flaky", 2, models.RunStatusFailed, time.Minute)

	d := getDashboard(t, s)
	rollups, _ := d["pipeline_rollups"].([]interface{})
	byName := map[string]map[string]interface{}{}
	for _, r := range rollups {
		m, _ := r.(map[string]interface{})
		byName[m["name"].(string)] = m
	}

	if got := byName["steady"]; got == nil || got["success"] != float64(4) || got["failed"] != float64(0) {
		t.Errorf("steady rollup = %+v, want 4 success / 0 failed", got)
	}
	if got := byName["flaky"]; got == nil || got["failed"] != float64(2) || got["success"] != float64(0) {
		t.Errorf("flaky rollup = %+v, want 0 success / 2 failed", got)
	}
	// A grouped row must be able to show its last status without a second
	// request, so the failure is visible before anyone expands the group.
	if got := byName["flaky"]; got != nil && got["last_status"] != "failed" {
		t.Errorf("flaky last_status = %v, want failed", got["last_status"])
	}
}

// Runs outside the 24h window must not inflate the rollup.
func TestDashboardHandler_PipelineRollupsExcludeOlderThan24h(t *testing.T) {
	s := newDashboardTestStore(t)
	seedRuns(t, s, "pipe-1", "ancient", 5, models.RunStatusSuccess, 48*time.Hour)

	d := getDashboard(t, s)
	rollups, _ := d["pipeline_rollups"].([]interface{})
	if len(rollups) != 0 {
		t.Errorf("pipeline_rollups = %+v, want none — every run is two days old", rollups)
	}
}
