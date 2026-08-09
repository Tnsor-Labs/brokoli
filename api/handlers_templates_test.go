package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/golang-jwt/jwt/v5"

	"github.com/Tnsor-Labs/brokoli/models"
	"github.com/Tnsor-Labs/brokoli/store"
)

func newTemplateTestStore(t *testing.T) store.Store {
	t.Helper()
	s, err := store.NewSQLiteStore(filepath.Join(t.TempDir(), "templates.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

// newTemplateTestRouter mirrors routes.go's actual wiring (requireAdmin
// gating create/update/delete, List open) so these tests exercise the
// real authorization behavior, not a stand-in.
func newTemplateTestRouter(s store.Store) *chi.Mux {
	th := NewTemplateHandler(s)
	r := chi.NewRouter()
	r.Get("/templates", th.List)
	r.With(requireAdmin).Post("/templates", th.Create)
	r.With(requireAdmin).Put("/templates/{id}", th.Update)
	r.With(requireAdmin).Delete("/templates/{id}", th.Delete)
	return r
}

func withRole(req *http.Request, role string) *http.Request {
	claims := jwt.MapClaims{"sub": "test-user", "role": role}
	return req.WithContext(context.WithValue(req.Context(), "claims", &claims))
}

func TestTemplateHandler_List_ReturnsSeededTemplatesWithNoAuth(t *testing.T) {
	router := newTemplateTestRouter(newTemplateTestStore(t))
	req := httptest.NewRequest(http.MethodGet, "/templates", nil) // no claims at all
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body: %s", rec.Code, rec.Body.String())
	}
	var list []models.PipelineTemplate
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(list) != 4 {
		t.Fatalf("got %d templates, want 4 executable seeded templates", len(list))
	}
	for _, template := range list {
		if len(template.Nodes) == 0 {
			t.Fatalf("listed non-creatable template %q with no nodes", template.Name)
		}
	}
}

func TestTemplateHandler_Create_UnauthenticatedReturns401(t *testing.T) {
	router := newTemplateTestRouter(newTemplateTestStore(t))
	req := httptest.NewRequest(http.MethodPost, "/templates", strings.NewReader(`{"name":"x"}`))
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", rec.Code)
	}
}

func TestTemplateHandler_Create_NonAdminReturns403(t *testing.T) {
	router := newTemplateTestRouter(newTemplateTestStore(t))
	for _, role := range []string{"editor", "operator", "viewer"} {
		t.Run(role, func(t *testing.T) {
			req := withRole(httptest.NewRequest(http.MethodPost, "/templates", strings.NewReader(`{"name":"x"}`)), role)
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, req)
			if rec.Code != http.StatusForbidden {
				t.Errorf("role %q: status = %d, want 403", role, rec.Code)
			}
		})
	}
}

func TestTemplateHandler_Create_AdminSucceeds(t *testing.T) {
	s := newTemplateTestStore(t)
	router := newTemplateTestRouter(s)

	body := `{"name":"Custom","description":"an org-specific starter","icon":"file","nodes":[],"edges":[]}`
	req := withRole(httptest.NewRequest(http.MethodPost, "/templates", strings.NewReader(body)), "admin")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201, body: %s", rec.Code, rec.Body.String())
	}
	var created models.PipelineTemplate
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if created.ID == "" {
		t.Error("expected a generated ID")
	}

	list, err := s.ListPipelineTemplates()
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list) != 6 {
		t.Fatalf("got %d templates after create, want 6 (5 seeded + 1 new)", len(list))
	}
}

func TestTemplateHandler_Create_SuperadminAlsoSucceeds(t *testing.T) {
	router := newTemplateTestRouter(newTemplateTestStore(t))
	body := `{"name":"Custom"}`
	req := withRole(httptest.NewRequest(http.MethodPost, "/templates", strings.NewReader(body)), "superadmin")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201, body: %s", rec.Code, rec.Body.String())
	}
}

func TestTemplateHandler_Create_RequiresName(t *testing.T) {
	router := newTemplateTestRouter(newTemplateTestStore(t))
	req := withRole(httptest.NewRequest(http.MethodPost, "/templates", strings.NewReader(`{}`)), "admin")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rec.Code)
	}
}

func TestTemplateHandler_Update_AdminSucceeds_PartialUpdatePreservesUnsetFields(t *testing.T) {
	s := newTemplateTestStore(t)
	router := newTemplateTestRouter(s)

	before, err := s.GetPipelineTemplate("hello-world")
	if err != nil {
		t.Fatalf("get seeded template: %v", err)
	}
	originalNodeCount := len(before.Nodes)

	// Only rename it — nodes/edges/icon must survive untouched.
	req := withRole(httptest.NewRequest(http.MethodPut, "/templates/hello-world", strings.NewReader(`{"name":"Hello World (renamed)"}`)), "admin")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body: %s", rec.Code, rec.Body.String())
	}

	after, err := s.GetPipelineTemplate("hello-world")
	if err != nil {
		t.Fatalf("get updated template: %v", err)
	}
	if after.Name != "Hello World (renamed)" {
		t.Errorf("Name = %q, want the renamed value", after.Name)
	}
	if len(after.Nodes) != originalNodeCount {
		t.Errorf("Nodes count = %d, want unchanged %d (partial update must not clobber unset fields)", len(after.Nodes), originalNodeCount)
	}
	if !after.UpdatedAt.After(before.UpdatedAt) {
		t.Error("expected UpdatedAt to advance")
	}
}

func TestTemplateHandler_Update_NonAdminReturns403(t *testing.T) {
	router := newTemplateTestRouter(newTemplateTestStore(t))
	req := withRole(httptest.NewRequest(http.MethodPut, "/templates/hello-world", strings.NewReader(`{"name":"x"}`)), "editor")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403", rec.Code)
	}
}

func TestTemplateHandler_Update_UnknownIDReturns404(t *testing.T) {
	router := newTemplateTestRouter(newTemplateTestStore(t))
	req := withRole(httptest.NewRequest(http.MethodPut, "/templates/does-not-exist", strings.NewReader(`{"name":"x"}`)), "admin")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rec.Code)
	}
}

func TestTemplateHandler_Delete_AdminSucceeds(t *testing.T) {
	s := newTemplateTestStore(t)
	router := newTemplateTestRouter(s)

	req := withRole(httptest.NewRequest(http.MethodDelete, "/templates/blank", nil), "admin")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204, body: %s", rec.Code, rec.Body.String())
	}
	if _, err := s.GetPipelineTemplate("blank"); err == nil {
		t.Error("expected the template to be gone")
	}
}

func TestTemplateHandler_Delete_NonAdminReturns403(t *testing.T) {
	router := newTemplateTestRouter(newTemplateTestStore(t))
	req := withRole(httptest.NewRequest(http.MethodDelete, "/templates/blank", nil), "viewer")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403", rec.Code)
	}
}

func TestTemplateHandler_Delete_UnknownIDReturns404(t *testing.T) {
	router := newTemplateTestRouter(newTemplateTestStore(t))
	req := withRole(httptest.NewRequest(http.MethodDelete, "/templates/does-not-exist", nil), "admin")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rec.Code)
	}
}
