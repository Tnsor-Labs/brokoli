package store

// Run.Parameters (ADR-032 rollout step 4, issue #439) is the resolved,
// typed run-parameter snapshot -- distinct from the legacy untyped
// Params -- persisted once at run creation. Only CreateRun/GetRun carry
// it; list views deliberately don't (the same "skip the expensive blob
// for list shapes" precedent ListPipelineDepsByOrg already established
// for pipelines), so these tests exercise the single-run path only.

import (
	"testing"
	"time"

	"github.com/Tnsor-Labs/brokoli/models"
)

func TestRunParametersSurviveCreateAndGet(t *testing.T) {
	s := newTestStore(t)
	now := time.Now().UTC().Truncate(time.Millisecond)
	if err := s.CreatePipeline(&models.Pipeline{
		ID: "pipe-params", Name: "Params Test Pipeline", CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatalf("CreatePipeline: %v", err)
	}

	run := &models.Run{
		ID: "run-params-1", PipelineID: "pipe-params", Status: models.RunStatusRunning, StartedAt: &now,
		Parameters: map[string]interface{}{"threshold": 0.9, "region": "us-east"},
	}
	if err := s.CreateRun(run); err != nil {
		t.Fatalf("CreateRun: %v", err)
	}

	got, err := s.GetRun("run-params-1")
	if err != nil {
		t.Fatalf("GetRun: %v", err)
	}
	if len(got.Parameters) != 2 {
		t.Fatalf("run parameters lost on read: %#v", got.Parameters)
	}
	if got.Parameters["threshold"] != 0.9 || got.Parameters["region"] != "us-east" {
		t.Fatalf("run parameters mangled: %#v", got.Parameters)
	}
}

func TestRunWithoutParametersUnchanged(t *testing.T) {
	s := newTestStore(t)
	now := time.Now().UTC().Truncate(time.Millisecond)
	if err := s.CreatePipeline(&models.Pipeline{
		ID: "pipe-noparams", Name: "No Params Pipeline", CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatalf("CreatePipeline: %v", err)
	}
	if err := s.CreateRun(&models.Run{ID: "run-noparams", PipelineID: "pipe-noparams", Status: models.RunStatusRunning, StartedAt: &now}); err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	got, err := s.GetRun("run-noparams")
	if err != nil {
		t.Fatalf("GetRun: %v", err)
	}
	if got.Parameters != nil {
		t.Fatalf("parameter-less run grew parameters: %#v", got.Parameters)
	}
	// Legacy Params must be entirely unaffected by Parameters existing.
	if got.Params != nil {
		t.Fatalf("expected nil legacy Params, got: %#v", got.Params)
	}
}
