package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Tnsor-Labs/brokoli/models"
	"github.com/Tnsor-Labs/brokoli/store"
	"github.com/go-chi/chi/v5"
)

func createOrgPipe(t *testing.T, s store.Store, id, name, orgID string, deps []models.DependencyRule) *models.Pipeline {
	t.Helper()
	now := time.Now().UTC()
	p := &models.Pipeline{
		ID:              id,
		Name:            name,
		OrgID:           orgID,
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

// reqWithOrg constructs a request whose context carries the given org ID,
// mimicking what OrgMiddleware would set up.
func reqWithOrg(method, path string, body []byte, orgID string) *http.Request {
	var r *http.Request
	if body != nil {
		r = httptest.NewRequest(method, path, bytes.NewReader(body))
	} else {
		r = httptest.NewRequest(method, path, nil)
	}
	if orgID != "" {
		ctx := context.WithValue(r.Context(), OrgIDContextKey{}, orgID)
		r = r.WithContext(ctx)
	}
	return r
}

// Enterprise behavior is "GetOrgIDFromRequest returns context value OR calls OrgResolverFunc".
// Force the resolver path off so context-only works deterministically.
func withoutOrgResolver(t *testing.T) {
	t.Helper()
	prev := OrgResolverFunc
	OrgResolverFunc = nil
	t.Cleanup(func() { OrgResolverFunc = prev })
}

// SAVE-TIME: creating a pipeline with a rule pointing at another tenant's pipeline
// must be rejected with 400 and an opaque error (no leak of existence/name).
func TestCreatePipeline_RejectsCrossOrgDependency(t *testing.T) {
	withoutOrgResolver(t)
	s := newDepAPIStore(t)
	h := NewPipelineHandler(s, nil)

	// Tenant B owns a pipeline.
	createOrgPipe(t, s, "b-up", "B-Upstream", "org-b", nil)

	// Tenant A tries to create a pipeline depending on tenant B's pipeline.
	body, _ := json.Marshal(&models.Pipeline{
		Name:            "A-Down",
		Nodes:           []models.Node{},
		Edges:           []models.Edge{},
		DependencyRules: []models.DependencyRule{{PipelineID: "b-up"}},
	})
	req := reqWithOrg("POST", "/pipelines", body, "org-a")
	w := httptest.NewRecorder()
	h.Create(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("want 400, got %d body=%s", w.Code, w.Body.String())
	}
	// Error must be indistinguishable from "not found".
	if !bytes.Contains(w.Body.Bytes(), []byte("not found")) {
		t.Errorf("expected opaque 'not found' error, got %s", w.Body.String())
	}
	if bytes.Contains(w.Body.Bytes(), []byte("B-Upstream")) {
		t.Errorf("response leaks upstream name: %s", w.Body.String())
	}
}

// SAVE-TIME: updating a pipeline to reference another tenant's pipeline ID must fail.
func TestUpdatePipeline_RejectsCrossOrgDependency(t *testing.T) {
	withoutOrgResolver(t)
	s := newDepAPIStore(t)
	h := NewPipelineHandler(s, nil)

	createOrgPipe(t, s, "b-up", "B-Upstream", "org-b", nil)
	createOrgPipe(t, s, "a-p", "A-Pipeline", "org-a", nil)

	body, _ := json.Marshal(&models.Pipeline{
		Name:            "A-Pipeline",
		Nodes:           []models.Node{},
		Edges:           []models.Edge{},
		DependencyRules: []models.DependencyRule{{PipelineID: "b-up"}},
	})
	req := reqWithOrg("PUT", "/pipelines/a-p", body, "org-a")
	w := httptest.NewRecorder()
	r := chi.NewRouter()
	r.Put("/pipelines/{id}", h.Update)
	r.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("want 400, got %d body=%s", w.Code, w.Body.String())
	}
}

// Legacy depends_on strings must be validated the same way.
func TestCreatePipeline_RejectsCrossOrgLegacyDependsOn(t *testing.T) {
	withoutOrgResolver(t)
	s := newDepAPIStore(t)
	h := NewPipelineHandler(s, nil)

	createOrgPipe(t, s, "b-up", "B-Upstream", "org-b", nil)

	body, _ := json.Marshal(&models.Pipeline{
		Name:      "A-Down",
		Nodes:     []models.Node{},
		Edges:     []models.Edge{},
		DependsOn: []string{"b-up"},
	})
	req := reqWithOrg("POST", "/pipelines", body, "org-a")
	w := httptest.NewRecorder()
	h.Create(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("want 400, got %d body=%s", w.Code, w.Body.String())
	}
}

// DELETE RESOLVER: when dependents exist across orgs, only the same-org dependents
// are reported, cascaded, or decoupled. Another tenant's pipeline must never be touched.
func TestDeletePipeline_ConflictListsOnlySameOrgDependents(t *testing.T) {
	withoutOrgResolver(t)
	s := newDepAPIStore(t)
	h := NewPipelineHandler(s, nil)

	createOrgPipe(t, s, "a-up", "A-Upstream", "org-a", nil)
	// Legitimate same-org dependent.
	createOrgPipe(t, s, "a-down", "A-Downstream", "org-a", []models.DependencyRule{{PipelineID: "a-up"}})
	// Cross-org dependent (shouldn't even be possible via the API anymore,
	// but belt-and-braces: direct-to-store ingestion must still be filtered).
	createOrgPipe(t, s, "b-down", "B-Downstream", "org-b", []models.DependencyRule{{PipelineID: "a-up"}})

	req := reqWithOrg("DELETE", "/pipelines/a-up", nil, "org-a")
	w := httptest.NewRecorder()
	r := chi.NewRouter()
	r.Delete("/pipelines/{id}", h.Delete)
	r.ServeHTTP(w, req)

	if w.Code != http.StatusConflict {
		t.Fatalf("want 409, got %d body=%s", w.Code, w.Body.String())
	}
	var resp map[string]interface{}
	json.Unmarshal(w.Body.Bytes(), &resp)
	deps, _ := resp["dependents"].([]interface{})
	if len(deps) != 1 {
		t.Fatalf("want 1 same-org dependent in conflict, got %d: %v", len(deps), deps)
	}
	// The response must not even mention B-Downstream.
	if bytes.Contains(w.Body.Bytes(), []byte("B-Downstream")) {
		t.Errorf("conflict leaks cross-org dependent name: %s", w.Body.String())
	}
}

// CASCADE DELETE must never touch another tenant's pipelines.
func TestDeletePipeline_CascadeLeavesCrossOrgUntouched(t *testing.T) {
	withoutOrgResolver(t)
	s := newDepAPIStore(t)
	h := NewPipelineHandler(s, nil)

	createOrgPipe(t, s, "a-up", "A-Upstream", "org-a", nil)
	createOrgPipe(t, s, "a-down", "A-Downstream", "org-a", []models.DependencyRule{{PipelineID: "a-up"}})
	createOrgPipe(t, s, "b-down", "B-Downstream", "org-b", []models.DependencyRule{{PipelineID: "a-up"}})

	req := reqWithOrg("DELETE", "/pipelines/a-up?resolve=cascade", nil, "org-a")
	w := httptest.NewRecorder()
	r := chi.NewRouter()
	r.Delete("/pipelines/{id}", h.Delete)
	r.ServeHTTP(w, req)

	if w.Code != http.StatusNoContent {
		t.Fatalf("want 204, got %d body=%s", w.Code, w.Body.String())
	}
	if _, err := s.GetPipeline("a-down"); err == nil {
		t.Error("a-down should have been cascade-deleted")
	}
	if _, err := s.GetPipeline("b-down"); err != nil {
		t.Error("b-down (cross-org) must NOT be deleted")
	}
}

// The dependents endpoint must only return same-org pipelines.
func TestDependentsHandler_ScopedToOrg(t *testing.T) {
	withoutOrgResolver(t)
	s := newDepAPIStore(t)

	createOrgPipe(t, s, "a-up", "A-Upstream", "org-a", nil)
	createOrgPipe(t, s, "a-down", "A-Downstream", "org-a", []models.DependencyRule{{PipelineID: "a-up"}})
	createOrgPipe(t, s, "b-down", "B-Downstream", "org-b", []models.DependencyRule{{PipelineID: "a-up"}})

	req := reqWithOrg("GET", "/pipelines/a-up/dependents", nil, "org-a")
	w := httptest.NewRecorder()
	r := chi.NewRouter()
	r.Get("/pipelines/{id}/dependents", pipelineDependentsHandler(s))
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", w.Code)
	}
	var out []map[string]interface{}
	json.Unmarshal(w.Body.Bytes(), &out)
	if len(out) != 1 {
		t.Errorf("want 1 same-org dependent, got %d: %v", len(out), out)
	}
}

// The dependency graph must be scoped to the caller's org.
func TestDependencyGraphHandler_ScopedToOrg(t *testing.T) {
	withoutOrgResolver(t)
	s := newDepAPIStore(t)

	createOrgPipe(t, s, "a1", "A1", "org-a", nil)
	createOrgPipe(t, s, "a2", "A2", "org-a", []models.DependencyRule{{PipelineID: "a1"}})
	createOrgPipe(t, s, "b1", "B1", "org-b", nil)
	createOrgPipe(t, s, "b2", "B2", "org-b", []models.DependencyRule{{PipelineID: "b1"}})

	req := reqWithOrg("GET", "/pipelines/dependency-graph", nil, "org-a")
	w := httptest.NewRecorder()
	pipelineDependencyGraphHandler(s)(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", w.Code)
	}
	var out map[string]interface{}
	json.Unmarshal(w.Body.Bytes(), &out)
	nodes := out["nodes"].([]interface{})
	if len(nodes) != 2 {
		t.Errorf("org-a should see only its 2 pipelines, got %d", len(nodes))
	}
	if bytes.Contains(w.Body.Bytes(), []byte("B1")) || bytes.Contains(w.Body.Bytes(), []byte("B2")) {
		t.Errorf("graph leaks org-b pipelines: %s", w.Body.String())
	}
}
