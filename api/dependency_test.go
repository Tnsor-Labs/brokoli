package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/Tnsor-Labs/brokoli/models"
	"github.com/Tnsor-Labs/brokoli/store"
	"github.com/go-chi/chi/v5"
)

func newDepAPIStore(t *testing.T) store.Store {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "api-deps.db")
	s, err := store.NewSQLiteStore(dbPath)
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func createPipeForAPI(t *testing.T, s store.Store, id, name string, deps []models.DependencyRule) *models.Pipeline {
	t.Helper()
	now := time.Now().UTC()
	p := &models.Pipeline{
		ID:              id,
		Name:            name,
		Nodes:           []models.Node{},
		Edges:           []models.Edge{},
		DependencyRules: deps,
		CreatedAt:       now,
		UpdatedAt:       now,
	}
	if err := s.CreatePipeline(p); err != nil {
		t.Fatalf("CreatePipeline %s: %v", id, err)
	}
	return p
}

// routerWithID wraps a handler so chi.URLParam("id") works in tests.
func routerWithID(id string, h http.HandlerFunc) http.Handler {
	r := chi.NewRouter()
	r.MethodFunc("DELETE", "/pipelines/{id}", h)
	return r
}

func TestDeletePipeline_AbortWhenDependentsExist(t *testing.T) {
	s := newDepAPIStore(t)
	h := NewPipelineHandler(s, nil)

	createPipeForAPI(t, s, "up", "Upstream", nil)
	createPipeForAPI(t, s, "down", "Downstream", []models.DependencyRule{{PipelineID: "up"}})

	req := httptest.NewRequest("DELETE", "/pipelines/up", nil)
	w := httptest.NewRecorder()
	r := chi.NewRouter()
	r.Delete("/pipelines/{id}", h.Delete)
	r.ServeHTTP(w, req)

	if w.Code != http.StatusConflict {
		t.Errorf("expected 409, got %d body=%s", w.Code, w.Body.String())
	}
	var resp map[string]interface{}
	json.Unmarshal(w.Body.Bytes(), &resp)
	if resp["error"] == nil {
		t.Error("expected error in response")
	}
	deps, ok := resp["dependents"].([]interface{})
	if !ok || len(deps) != 1 {
		t.Errorf("expected 1 dependent, got %v", resp["dependents"])
	}
}

func TestDeletePipeline_CascadeRemovesDependents(t *testing.T) {
	s := newDepAPIStore(t)
	h := NewPipelineHandler(s, nil)

	createPipeForAPI(t, s, "up", "Upstream", nil)
	createPipeForAPI(t, s, "down", "Downstream", []models.DependencyRule{{PipelineID: "up"}})

	req := httptest.NewRequest("DELETE", "/pipelines/up?resolve=cascade", nil)
	w := httptest.NewRecorder()
	r := chi.NewRouter()
	r.Delete("/pipelines/{id}", h.Delete)
	r.ServeHTTP(w, req)

	if w.Code != http.StatusNoContent {
		t.Errorf("expected 204, got %d body=%s", w.Code, w.Body.String())
	}
	if _, err := s.GetPipeline("up"); err == nil {
		t.Error("up should be deleted")
	}
	if _, err := s.GetPipeline("down"); err == nil {
		t.Error("down should be cascade-deleted")
	}
}

func TestDeletePipeline_DecoupleStripsReference(t *testing.T) {
	s := newDepAPIStore(t)
	h := NewPipelineHandler(s, nil)

	createPipeForAPI(t, s, "up", "Upstream", nil)
	createPipeForAPI(t, s, "down", "Downstream", []models.DependencyRule{{PipelineID: "up"}})

	req := httptest.NewRequest("DELETE", "/pipelines/up?resolve=decouple", nil)
	w := httptest.NewRecorder()
	r := chi.NewRouter()
	r.Delete("/pipelines/{id}", h.Delete)
	r.ServeHTTP(w, req)

	if w.Code != http.StatusNoContent {
		t.Errorf("expected 204, got %d body=%s", w.Code, w.Body.String())
	}
	if _, err := s.GetPipeline("up"); err == nil {
		t.Error("up should be deleted")
	}
	down, err := s.GetPipeline("down")
	if err != nil {
		t.Fatal("down should still exist")
	}
	if len(down.DependencyRules) != 0 {
		t.Errorf("dependency should have been stripped, got %+v", down.DependencyRules)
	}
}

func TestCreatePipeline_RejectsCycle(t *testing.T) {
	s := newDepAPIStore(t)
	h := NewPipelineHandler(s, nil)

	createPipeForAPI(t, s, "a", "A", nil)
	createPipeForAPI(t, s, "b", "B", []models.DependencyRule{{PipelineID: "a"}})
	createPipeForAPI(t, s, "c", "C", []models.DependencyRule{{PipelineID: "b"}})

	// Attempt to update A so it depends on C (would create cycle a->c->b->a)
	body, _ := json.Marshal(&models.Pipeline{
		Name:            "A",
		Nodes:           []models.Node{},
		Edges:           []models.Edge{},
		DependencyRules: []models.DependencyRule{{PipelineID: "c"}},
	})
	req := httptest.NewRequest("PUT", "/pipelines/a", bytes.NewReader(body))
	w := httptest.NewRecorder()
	r := chi.NewRouter()
	r.Put("/pipelines/{id}", h.Update)
	r.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for cycle, got %d body=%s", w.Code, w.Body.String())
	}
}

func TestDependentsHandler(t *testing.T) {
	s := newDepAPIStore(t)

	createPipeForAPI(t, s, "up", "Upstream", nil)
	createPipeForAPI(t, s, "d1", "D1", []models.DependencyRule{{PipelineID: "up"}})
	createPipeForAPI(t, s, "d2", "D2", []models.DependencyRule{{PipelineID: "up", Mode: models.DepModeTrigger}})
	createPipeForAPI(t, s, "other", "Other", nil)

	req := httptest.NewRequest("GET", "/pipelines/up/dependents", nil)
	w := httptest.NewRecorder()
	r := chi.NewRouter()
	r.Get("/pipelines/{id}/dependents", pipelineDependentsHandler(s))
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	var out []map[string]interface{}
	json.Unmarshal(w.Body.Bytes(), &out)
	if len(out) != 2 {
		t.Errorf("expected 2 dependents, got %d: %v", len(out), out)
	}
}

func TestDependencyGraphHandler(t *testing.T) {
	s := newDepAPIStore(t)

	createPipeForAPI(t, s, "a", "A", nil)
	createPipeForAPI(t, s, "b", "B", []models.DependencyRule{{PipelineID: "a"}})
	createPipeForAPI(t, s, "c", "C", []models.DependencyRule{{PipelineID: "b"}})

	req := httptest.NewRequest("GET", "/pipelines/dependency-graph", nil)
	w := httptest.NewRecorder()
	pipelineDependencyGraphHandler(s)(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	var out map[string]interface{}
	json.Unmarshal(w.Body.Bytes(), &out)
	nodes := out["nodes"].([]interface{})
	edges := out["edges"].([]interface{})
	if len(nodes) != 3 {
		t.Errorf("expected 3 nodes, got %d", len(nodes))
	}
	if len(edges) != 2 {
		t.Errorf("expected 2 edges, got %d", len(edges))
	}
}

var _ = routerWithID // silence unused helper if I don't use it
