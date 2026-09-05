package store

// Pipeline.Parameters (ADR-032 section 3, issue #439) was decoded into
// models.Pipeline but silently dropped by the store -- absent from every
// column list, exactly the same "decoded then discarded" bug hooks/
// extensions had before pipeline_hooks_test.go pinned that fix. A
// pipeline deployed with typed parameter declarations lost them on the
// very next read.

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/Tnsor-Labs/brokoli/models"
)

func parametersPipeline(id string) *models.Pipeline {
	now := time.Now().UTC()
	return &models.Pipeline{
		ID:   id,
		Name: "typed-params",
		Nodes: []models.Node{{
			ID: "src", Type: models.NodeTypeSourceFile, Name: "Source",
			Config: map[string]interface{}{"path": "/tmp/in.csv"},
		}},
		Edges: []models.Edge{},
		Parameters: map[string]interface{}{
			"threshold": map[string]interface{}{
				"type": map[string]interface{}{"kind": "float64"}, "required": false, "default": 0.5,
			},
			"region": map[string]interface{}{
				"type":     map[string]interface{}{"kind": "enum", "values": []interface{}{"us-east", "eu-west"}},
				"required": true,
			},
		},
		CreatedAt: now, UpdatedAt: now,
	}
}

func TestPipelineParametersSurviveCreateAndGet(t *testing.T) {
	s, err := NewSQLiteStore(filepath.Join(t.TempDir(), "params.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	if err := s.CreatePipeline(parametersPipeline("params-1")); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetPipeline("params-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Parameters) != 2 {
		t.Fatalf("parameters lost on read: %#v", got.Parameters)
	}
	threshold, ok := got.Parameters["threshold"].(map[string]interface{})
	if !ok {
		t.Fatalf("threshold parameter mangled: %#v", got.Parameters["threshold"])
	}
	if threshold["default"] != 0.5 {
		t.Fatalf("threshold default lost: %#v", threshold)
	}

	// List paths use a different scan path than GetPipeline -- parameters
	// must survive there too.
	all, err := s.ListPipelines()
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 1 || len(all[0].Parameters) != 2 {
		t.Fatalf("parameters lost in list scan: %#v", all[0].Parameters)
	}
}

func TestPipelineParametersUpdateAndClear(t *testing.T) {
	s, err := NewSQLiteStore(filepath.Join(t.TempDir(), "params.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	p := parametersPipeline("params-2")
	if err := s.CreatePipeline(p); err != nil {
		t.Fatal(err)
	}

	p.Parameters = map[string]interface{}{
		"limit": map[string]interface{}{"type": map[string]interface{}{"kind": "int64"}, "required": true},
	}
	p.UpdatedAt = time.Now().UTC()
	if err := s.UpdatePipeline(p); err != nil {
		t.Fatal(err)
	}
	got, _ := s.GetPipeline("params-2")
	if len(got.Parameters) != 1 {
		t.Fatalf("parameters not replaced on update: %#v", got.Parameters)
	}

	p.Parameters = nil
	if err := s.UpdatePipeline(p); err != nil {
		t.Fatal(err)
	}
	got, _ = s.GetPipeline("params-2")
	if got.Parameters != nil {
		t.Fatalf("cleared parameters resurrected: %#v", got.Parameters)
	}
}

func TestPipelineWithoutParametersUnchanged(t *testing.T) {
	s, err := NewSQLiteStore(filepath.Join(t.TempDir(), "params.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	p := parametersPipeline("plain")
	p.Parameters = nil
	if err := s.CreatePipeline(p); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetPipeline("plain")
	if err != nil {
		t.Fatal(err)
	}
	if got.Parameters != nil {
		t.Fatalf("parameter-less pipeline grew parameters: %#v", got.Parameters)
	}
}
