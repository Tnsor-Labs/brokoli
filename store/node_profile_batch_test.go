package store

import (
	"testing"
	"time"

	"github.com/Tnsor-Labs/brokoli/models"
)

// TestSQLiteStore_GetLatestNodeProfilesForPipelines is the regression test
// for the N+1 query GetLatestNodeProfile forced on lineage graph
// construction: one batched call must return the latest profile for every
// (pipeline, node) pair across multiple pipelines and multiple runs each,
// matching what calling GetLatestNodeProfile once per node would have
// returned.
func TestSQLiteStore_GetLatestNodeProfilesForPipelines(t *testing.T) {
	s := newTestStore(t)
	now := time.Now().UTC()

	for _, id := range []string{"pipe-a", "pipe-b"} {
		if err := s.CreatePipeline(&models.Pipeline{ID: id, Name: id, CreatedAt: now, UpdatedAt: now}); err != nil {
			t.Fatalf("CreatePipeline(%s): %v", id, err)
		}
	}

	createRun := func(id, pipelineID string) {
		t.Helper()
		if err := s.CreateRun(&models.Run{ID: id, PipelineID: pipelineID, Status: models.RunStatusSuccess, StartedAt: &now}); err != nil {
			t.Fatalf("CreateRun(%s): %v", id, err)
		}
	}
	createRun("run-a1-old", "pipe-a")
	createRun("run-a1-new", "pipe-a")
	createRun("run-a2", "pipe-a")
	createRun("run-b1", "pipe-b")

	// pipe-a/n1 has two profiles across two runs; the newer one (by
	// created_at, not insertion order) must win.
	if err := s.SaveNodeProfile("run-a1-old", "n1", `{"row_count":10}`, `{}`, "[]"); err != nil {
		t.Fatal(err)
	}
	time.Sleep(5 * time.Millisecond) // created_at has second-ish granularity risk; ensure strict ordering
	if err := s.SaveNodeProfile("run-a1-new", "n1", `{"row_count":20}`, `{}`, "[]"); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveNodeProfile("run-a2", "n2", `{"row_count":30}`, `{}`, "[]"); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveNodeProfile("run-b1", "n1", `{"row_count":40}`, `{}`, "[]"); err != nil {
		t.Fatal(err)
	}

	records, err := s.GetLatestNodeProfilesForPipelines([]string{"pipe-a", "pipe-b"})
	if err != nil {
		t.Fatalf("GetLatestNodeProfilesForPipelines: %v", err)
	}
	if len(records) != 3 {
		t.Fatalf("got %d records, want 3 (%+v)", len(records), records)
	}

	rec, ok := records["pipe-a:n1"]
	if !ok {
		t.Fatal("missing pipe-a:n1")
	}
	if rec.ProfileJSON != `{"row_count":20}` {
		t.Errorf("pipe-a:n1 profile = %s, want the newer run's profile (row_count 20), not the older one", rec.ProfileJSON)
	}
	if rec.RunID != "run-a1-new" {
		t.Errorf("pipe-a:n1 run_id = %s, want run-a1-new", rec.RunID)
	}
	if rec.ObservedAt.IsZero() {
		t.Error("pipe-a:n1 ObservedAt is zero, want a real timestamp")
	}

	if got := records["pipe-a:n2"].ProfileJSON; got != `{"row_count":30}` {
		t.Errorf("pipe-a:n2 profile = %s, want row_count 30", got)
	}
	if got := records["pipe-b:n1"].ProfileJSON; got != `{"row_count":40}` {
		t.Errorf("pipe-b:n1 profile = %s, want row_count 40", got)
	}
}

// TestSQLiteStore_GetLatestNodeProfilesForPipelines_Empty proves the
// batched call degrades gracefully instead of erroring or panicking when
// given no pipeline IDs (a lineage graph with zero pipelines) or IDs with
// no profiles at all.
func TestSQLiteStore_GetLatestNodeProfilesForPipelines_Empty(t *testing.T) {
	s := newTestStore(t)

	records, err := s.GetLatestNodeProfilesForPipelines(nil)
	if err != nil || len(records) != 0 {
		t.Fatalf("empty input: records=%v err=%v, want empty map and nil error", records, err)
	}

	if err := s.CreatePipeline(&models.Pipeline{ID: "pipe-empty", Name: "pipe-empty"}); err != nil {
		t.Fatal(err)
	}
	records, err = s.GetLatestNodeProfilesForPipelines([]string{"pipe-empty"})
	if err != nil || len(records) != 0 {
		t.Fatalf("pipeline with no profiles: records=%v err=%v, want empty map and nil error", records, err)
	}
}
