package api

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Tnsor-Labs/brokoli/models"
	"github.com/go-chi/chi/v5"
)

// Transitive cascade: a -> b -> c. Deleting a with resolve=cascade must wipe all three.
func TestDeletePipeline_CascadeTransitive(t *testing.T) {
	withoutOrgResolver(t)
	s := newDepAPIStore(t)
	h := NewPipelineHandler(s, nil)

	createOrgPipe(t, s, "a", "A", "org-a", nil)
	createOrgPipe(t, s, "b", "B", "org-a", []models.DependencyRule{{PipelineID: "a"}})
	createOrgPipe(t, s, "c", "C", "org-a", []models.DependencyRule{{PipelineID: "b"}})

	req := reqWithOrg("DELETE", "/pipelines/a?resolve=cascade", nil, "org-a")
	w := httptest.NewRecorder()
	r := chi.NewRouter()
	r.Delete("/pipelines/{id}", h.Delete)
	r.ServeHTTP(w, req)

	if w.Code != http.StatusNoContent {
		t.Fatalf("want 204, got %d body=%s", w.Code, w.Body.String())
	}
	for _, id := range []string{"a", "b", "c"} {
		if _, err := s.GetPipeline(id); err == nil {
			t.Errorf("%s should have been cascade-deleted", id)
		}
	}
}

// Transitive cascade with a diamond: a -> b, a -> c, b -> d, c -> d.
// Deleting a with cascade wipes {a, b, c, d} and doesn't try to delete d twice.
func TestDeletePipeline_CascadeDiamond(t *testing.T) {
	withoutOrgResolver(t)
	s := newDepAPIStore(t)
	h := NewPipelineHandler(s, nil)

	createOrgPipe(t, s, "a", "A", "org-a", nil)
	createOrgPipe(t, s, "b", "B", "org-a", []models.DependencyRule{{PipelineID: "a"}})
	createOrgPipe(t, s, "c", "C", "org-a", []models.DependencyRule{{PipelineID: "a"}})
	createOrgPipe(t, s, "d", "D", "org-a", []models.DependencyRule{
		{PipelineID: "b"}, {PipelineID: "c"},
	})

	req := reqWithOrg("DELETE", "/pipelines/a?resolve=cascade", nil, "org-a")
	w := httptest.NewRecorder()
	r := chi.NewRouter()
	r.Delete("/pipelines/{id}", h.Delete)
	r.ServeHTTP(w, req)

	if w.Code != http.StatusNoContent {
		t.Fatalf("want 204, got %d body=%s", w.Code, w.Body.String())
	}
	for _, id := range []string{"a", "b", "c", "d"} {
		if _, err := s.GetPipeline(id); err == nil {
			t.Errorf("%s should have been cascade-deleted", id)
		}
	}
}

// Decouple must only strip direct dependents (transitive stays intact).
func TestDeletePipeline_DecoupleOnlyDirect(t *testing.T) {
	withoutOrgResolver(t)
	s := newDepAPIStore(t)
	h := NewPipelineHandler(s, nil)

	createOrgPipe(t, s, "a", "A", "org-a", nil)
	createOrgPipe(t, s, "b", "B", "org-a", []models.DependencyRule{{PipelineID: "a"}})
	createOrgPipe(t, s, "c", "C", "org-a", []models.DependencyRule{{PipelineID: "b"}})

	req := reqWithOrg("DELETE", "/pipelines/a?resolve=decouple", nil, "org-a")
	w := httptest.NewRecorder()
	r := chi.NewRouter()
	r.Delete("/pipelines/{id}", h.Delete)
	r.ServeHTTP(w, req)

	if w.Code != http.StatusNoContent {
		t.Fatalf("want 204, got %d body=%s", w.Code, w.Body.String())
	}
	if _, err := s.GetPipeline("a"); err == nil {
		t.Error("a should have been deleted")
	}
	b, err := s.GetPipeline("b")
	if err != nil {
		t.Fatal("b should still exist")
	}
	if len(b.DependencyRules) != 0 {
		t.Errorf("b's reference to a should be stripped, got %+v", b.DependencyRules)
	}
	c, err := s.GetPipeline("c")
	if err != nil {
		t.Fatal("c should still exist")
	}
	if len(c.DependencyRules) != 1 || c.DependencyRules[0].PipelineID != "b" {
		t.Errorf("c->b should be untouched, got %+v", c.DependencyRules)
	}
}

func TestDeletePipeline_InvalidResolveValue(t *testing.T) {
	withoutOrgResolver(t)
	s := newDepAPIStore(t)
	h := NewPipelineHandler(s, nil)

	createOrgPipe(t, s, "a", "A", "org-a", nil)

	req := reqWithOrg("DELETE", "/pipelines/a?resolve=bogus", nil, "org-a")
	w := httptest.NewRecorder()
	r := chi.NewRouter()
	r.Delete("/pipelines/{id}", h.Delete)
	r.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("want 400 for invalid resolve, got %d", w.Code)
	}
}
