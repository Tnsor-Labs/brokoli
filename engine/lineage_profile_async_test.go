package engine

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Tnsor-Labs/brokoli/models"
	"github.com/Tnsor-Labs/brokoli/store"
)

// TestSaveNodeProfile_PersistsWithoutBlockingNodeCompletion is the
// regression test for saveNodeProfile running off the node's own execution
// path: a successful run must reach RunStatusSuccess without waiting on the
// profile write, and the profile must still land shortly after — proving
// the async change didn't silently drop the write it exists to make
// non-blocking.
func TestSaveNodeProfile_PersistsWithoutBlockingNodeCompletion(t *testing.T) {
	s, err := store.NewSQLiteStore(filepath.Join(t.TempDir(), "profile-async.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })

	csvPath := filepath.Join(t.TempDir(), "input.csv")
	if err := os.WriteFile(csvPath, []byte("id,name\n1,brokoli\n2,sql\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	pipeline := &models.Pipeline{
		ID:   "profile-async-pipeline",
		Name: "Profile async pipeline",
		Nodes: []models.Node{{
			ID:     "source",
			Type:   models.NodeTypeSourceFile,
			Name:   "Source",
			Config: map[string]interface{}{"path": csvPath, "format": "csv"},
		}},
		CreatedAt: time.Now().UTC(),
		UpdatedAt: time.Now().UTC(),
	}
	if err := s.CreatePipeline(pipeline); err != nil {
		t.Fatal(err)
	}

	eng := drainEngineOnCleanup(t, NewEngine(s))
	runID, err := eng.RunPipelineAsync(pipeline.ID)
	if err != nil {
		t.Fatal(err)
	}

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		run, getErr := s.GetRun(runID)
		if getErr == nil && run.Status == models.RunStatusSuccess {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	run, err := s.GetRun(runID)
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != models.RunStatusSuccess {
		t.Fatalf("run status = %s, want success", run.Status)
	}

	// The profile write is fire-and-forget from a goroutine spawned inside
	// the node's own completion handling, so it can land a beat after the
	// run itself is marked successful — poll instead of asserting inline.
	deadline = time.Now().Add(2 * time.Second)
	var profileJSON, schemaJSON string
	for time.Now().Before(deadline) {
		profileJSON, schemaJSON, err = s.GetLatestNodeProfile(pipeline.ID, "source")
		if err == nil && profileJSON != "" {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err != nil || profileJSON == "" {
		t.Fatalf("GetLatestNodeProfile: profile never appeared, last err=%v", err)
	}
	if schemaJSON == "" {
		t.Fatal("GetLatestNodeProfile: schema is empty")
	}
}
