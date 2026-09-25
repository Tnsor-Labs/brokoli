package engine

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Tnsor-Labs/brokoli/models"
	"github.com/Tnsor-Labs/brokoli/store"
)

// A run started on the in-process path is attributed when the
// attribution travels with the run.
//
// The in-process path returns the run ID before the run row exists, so a
// caller that writes attribution against the returned ID straight away
// races the runner's own CreateRun, and under run_attribution's foreign
// key it loses. The only ordering that is guaranteed is the runner's: it
// creates the run, then records what it was given. This proves the
// exported path hands attribution to the runner rather than dropping it.
func TestTheInProcessPathRecordsWhoStartedTheRun(t *testing.T) {
	dir := t.TempDir()
	// A real run writes node outputs here; left unset it writes into the
	// package directory.
	t.Setenv("BROKOLI_ARTIFACT_DIR", filepath.Join(dir, "artifacts"))

	s, err := store.NewSQLiteStore(filepath.Join(dir, "attrib.db"))
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	t.Cleanup(func() { s.Close() })

	in := filepath.Join(dir, "in.csv")
	if err := os.WriteFile(in, []byte("id\n1\n2\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	pipe := &models.Pipeline{
		ID: "p-local", Name: "p-local", Enabled: true,
		Nodes: []models.Node{
			{ID: "src", Type: models.NodeTypeSourceFile,
				Config: map[string]interface{}{"path": in, "format": "csv"}},
			{ID: "dst", Type: models.NodeTypeSinkFile,
				Config: map[string]interface{}{"path": filepath.Join(dir, "out.csv"), "format": "csv"}},
		},
		Edges:     []models.Edge{{From: "src", To: "dst"}},
		CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}
	if err := s.CreatePipeline(pipe); err != nil {
		t.Fatalf("create pipeline: %v", err)
	}

	eng := drainEngineOnCleanup(t, NewEngine(s))
	runID, err := eng.RunPipelineAsyncLocalOpts(pipe.ID, nil, RunOptions{
		TriggeredBy: &models.RunAttribution{
			Kind: models.RunTriggerKindUser, UserID: "u1", UserName: "Alice",
		},
	})
	if err != nil {
		t.Fatalf("start run: %v", err)
	}

	deadline := time.Now().Add(30 * time.Second)
	for {
		run, err := s.GetRun(runID)
		if err == nil && run.Status != models.RunStatusRunning && run.Status != models.RunStatusPending {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("run %s never finished", runID)
		}
		time.Sleep(50 * time.Millisecond)
	}

	got, err := s.GetRunAttribution([]string{runID})
	if err != nil {
		t.Fatalf("get attribution: %v", err)
	}
	a, ok := got[runID]
	if !ok {
		t.Fatal("the run carries no attribution; the in-process path dropped TriggeredBy")
	}
	if a.Kind != models.RunTriggerKindUser || a.UserID != "u1" || a.UserName != "Alice" {
		t.Errorf("attribution = %+v, want user/u1/Alice", a)
	}
}
