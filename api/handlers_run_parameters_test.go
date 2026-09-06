package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Tnsor-Labs/brokoli/engine"
	"github.com/Tnsor-Labs/brokoli/models"
	"github.com/Tnsor-Labs/brokoli/store"

	"github.com/go-chi/chi/v5"
)

// runParametersTestEnv builds a RunHandler over a real SQLite store and a
// real engine, with a pipeline that declares one required typed parameter
// ("region") -- ADR-032 rollout step 4 (#439): TriggerRun must resolve a
// submitted "parameters" field against the pipeline's declarations before
// creating a run.
func runParametersTestEnv(t *testing.T) (*RunHandler, *chi.Mux, string) {
	t.Helper()
	s, err := store.NewSQLiteStore(filepath.Join(t.TempDir(), "trigger-params.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })

	csvPath := filepath.Join(t.TempDir(), "input.csv")
	if err := os.WriteFile(csvPath, []byte("id,name\n1,brokoli\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	pipeline := &models.Pipeline{
		ID:   "trigger-param-pipeline",
		Name: "Trigger parameter pipeline",
		Nodes: []models.Node{{
			ID:     "source",
			Type:   models.NodeTypeSourceFile,
			Name:   "Source",
			Config: map[string]interface{}{"path": csvPath, "format": "csv"},
		}},
		Parameters: map[string]interface{}{
			"region": map[string]interface{}{"type": map[string]interface{}{"kind": "string"}, "required": true},
		},
		CreatedAt: time.Now().UTC(),
		UpdatedAt: time.Now().UTC(),
	}
	if err := s.CreatePipeline(pipeline); err != nil {
		t.Fatal(err)
	}

	eng := engine.NewEngine(s)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = eng.Close(ctx)
	})
	h := NewRunHandler(s, eng)

	r := chi.NewRouter()
	r.Post("/api/pipelines/{id}/run", h.TriggerRun)
	return h, r, pipeline.ID
}

func TestTriggerRun_ValidTypedParameters_ResolvesAndAccepts(t *testing.T) {
	_, r, pipelineID := runParametersTestEnv(t)

	body, _ := json.Marshal(map[string]interface{}{
		"parameters": map[string]interface{}{"region": "us-east"},
	})
	req := httptest.NewRequest("POST", "/api/pipelines/"+pipelineID+"/run", bytes.NewReader(body))
	req = req.WithContext(withOpenMode(req.Context()))
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != 202 {
		t.Fatalf("status = %d, want 202; body: %s", w.Code, w.Body.String())
	}
}

func TestTriggerRun_MissingRequiredTypedParameter_Returns400(t *testing.T) {
	_, r, pipelineID := runParametersTestEnv(t)

	body, _ := json.Marshal(map[string]interface{}{
		"parameters": map[string]interface{}{},
	})
	req := httptest.NewRequest("POST", "/api/pipelines/"+pipelineID+"/run", bytes.NewReader(body))
	req = req.WithContext(withOpenMode(req.Context()))
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != 400 {
		t.Fatalf("status = %d, want 400 for a missing required run parameter; body: %s", w.Code, w.Body.String())
	}
}
