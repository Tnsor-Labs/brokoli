package engine

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Tnsor-Labs/brokoli/models"
	"github.com/Tnsor-Labs/brokoli/store"
)

// newParameterizedTestEngine builds a single-node pipeline that declares one
// required typed parameter ("region", string) and one optional parameter
// ("threshold", float64, default 0.5) -- the ADR-032 rollout step 4 (#439)
// wiring under test.
func newParameterizedTestEngine(t *testing.T) (*Engine, *store.SQLiteStore, string) {
	t.Helper()
	s, err := store.NewSQLiteStore(filepath.Join(t.TempDir(), "params.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })

	csvPath := filepath.Join(t.TempDir(), "input.csv")
	if err := os.WriteFile(csvPath, []byte("id,name\n1,brokoli\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	pipeline := &models.Pipeline{
		ID:   "param-pipeline",
		Name: "Parameterized pipeline",
		Nodes: []models.Node{{
			ID:     "source",
			Type:   models.NodeTypeSourceFile,
			Name:   "Source",
			Config: map[string]interface{}{"path": csvPath, "format": "csv"},
		}},
		Parameters: map[string]interface{}{
			"region":    map[string]interface{}{"type": map[string]interface{}{"kind": "string"}, "required": true},
			"threshold": map[string]interface{}{"type": map[string]interface{}{"kind": "float64"}, "required": false, "default": 0.5},
		},
		CreatedAt: time.Now().UTC(),
		UpdatedAt: time.Now().UTC(),
	}
	if err := s.CreatePipeline(pipeline); err != nil {
		t.Fatal(err)
	}

	eng := drainEngineOnCleanup(t, NewEngine(s))
	return eng, s, pipeline.ID
}

func TestRunPipelineAsyncWithParameters_ResolvesAndPersistsOnInProcessPath(t *testing.T) {
	eng, s, pipelineID := newParameterizedTestEngine(t)

	runID, err := eng.RunPipelineAsyncWithParameters(pipelineID, nil, map[string]interface{}{"region": "us-east"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// RunPipelineAsyncWithParameters returns as soon as the runner goroutine
	// is dispatched -- the run row (and this preRunID) may not exist yet, so
	// a not-found GetRun is expected transient state here, not a failure.
	deadline := time.Now().Add(5 * time.Second)
	var run *models.Run
	for time.Now().Before(deadline) {
		run, err = s.GetRun(runID)
		if err == nil && run.Status != models.RunStatusRunning {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err != nil {
		t.Fatalf("run never appeared: %v", err)
	}
	if run.Parameters["region"] != "us-east" {
		t.Errorf("run parameters[region] = %v, want us-east", run.Parameters["region"])
	}
	if run.Parameters["threshold"] != 0.5 {
		t.Errorf("run parameters[threshold] = %v, want default 0.5", run.Parameters["threshold"])
	}
}

func TestRunPipelineAsyncWithParameters_ResolvesAndPersistsOnJobQueuePath(t *testing.T) {
	eng, s, pipelineID := newParameterizedTestEngine(t)
	queue := &recordingJobQueue{}
	eng.JobQueue = queue

	runID, err := eng.RunPipelineAsyncWithParameters(pipelineID, nil, map[string]interface{}{"region": "eu-west", "threshold": 0.9})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(queue.jobs) != 1 {
		t.Fatalf("queued jobs = %d, want 1", len(queue.jobs))
	}

	run, err := s.GetRun(runID)
	if err != nil {
		t.Fatal(err)
	}
	if run.Parameters["region"] != "eu-west" {
		t.Errorf("accepted run parameters[region] = %v, want eu-west", run.Parameters["region"])
	}
	if run.Parameters["threshold"] != 0.9 {
		t.Errorf("accepted run parameters[threshold] = %v, want submitted 0.9", run.Parameters["threshold"])
	}
}

func TestRunPipelineAsyncWithParameters_MissingRequired_RejectsBeforeCreatingRun(t *testing.T) {
	eng, s, pipelineID := newParameterizedTestEngine(t)

	before, err := s.ListRunsByPipeline(pipelineID, 100)
	if err != nil {
		t.Fatal(err)
	}

	_, err = eng.RunPipelineAsyncWithParameters(pipelineID, nil, map[string]interface{}{})
	if !errors.Is(err, ErrParameterResolution) {
		t.Fatalf("err = %v, want ErrParameterResolution", err)
	}

	after, err := s.ListRunsByPipeline(pipelineID, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != len(before) {
		t.Fatalf("run count changed from %d to %d; a rejected parameter resolution must not create a run", len(before), len(after))
	}
}

func TestRunPipelineAsyncWithParameters_NoDeclaredParameters_TypedParamsIgnored(t *testing.T) {
	s, err := store.NewSQLiteStore(filepath.Join(t.TempDir(), "noparams.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	csvPath := filepath.Join(t.TempDir(), "input.csv")
	if err := os.WriteFile(csvPath, []byte("id,name\n1,brokoli\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	pipeline := &models.Pipeline{
		ID:   "no-param-pipeline",
		Name: "Unparameterized pipeline",
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

	// Submitting a typed parameter that the pipeline never declared must not
	// error: ResolveParameters is skipped entirely when len(pipe.Parameters)
	// == 0 (engine.go's own documented short-circuit).
	if _, err := eng.RunPipelineAsyncWithParameters(pipeline.ID, nil, map[string]interface{}{"whatever": true}); err != nil {
		t.Fatalf("unexpected error for an unparameterized pipeline: %v", err)
	}
}
