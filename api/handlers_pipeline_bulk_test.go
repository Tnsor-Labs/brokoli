package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/golang-jwt/jwt/v5"

	"github.com/Tnsor-Labs/brokoli/models"
	"github.com/Tnsor-Labs/brokoli/store"
)

type bulkAnswer struct {
	Succeeded int           `json:"succeeded"`
	Failed    int           `json:"failed"`
	Errors    []bulkFailure `json:"errors"`
}

// postBulk sends body to POST /pipelines/bulk as a signed-in person with
// role, through gate.
func postBulk(t *testing.T, s store.Store, gate func(models.Permission) func(http.Handler) http.Handler, role, body string) (*httptest.ResponseRecorder, bulkAnswer) {
	t.Helper()
	r := chi.NewRouter()
	r.Post("/pipelines/bulk", NewPipelineHandler(s, nil).Bulk(gate))
	req := reqWithOrg("POST", "/pipelines/bulk", []byte(body), "org-a")
	claims := jwt.MapClaims{"sub": "u1", "role": role}
	req = req.WithContext(context.WithValue(req.Context(), "claims", &claims))
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	var out bulkAnswer
	if rec.Code == http.StatusOK {
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatalf("decode %s: %v", rec.Body.String(), err)
		}
	}
	return rec, out
}

func exists(s store.Store, id string) bool {
	_, err := s.GetPipeline(id)
	return err == nil
}

// An action the endpoint does not know was counted as succeeded for every
// id, with nothing done (#593).
func TestBulkRefusesAnUnknownAction(t *testing.T) {
	withoutOrgResolver(t)
	s := newDepAPIStore(t)
	createOrgPipe(t, s, "a", "A", "org-a", nil)
	gated := false
	gate := func(models.Permission) func(http.Handler) http.Handler {
		gated = true
		return func(next http.Handler) http.Handler { return next }
	}
	for _, action := range []string{"frobnicate", "", "Delete"} {
		rec, _ := postBulk(t, s, gate, "admin", `{"ids":["a"],"action":"`+action+`"}`)
		if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "unknown action") {
			t.Errorf("action %q: %d %s, want 400 unknown action", action, rec.Code, rec.Body.String())
		}
	}
	if gated || !exists(s, "a") {
		t.Errorf("an unknown action reached the gate (%v) or changed something", gated)
	}
}

// Each action asks for what its single endpoint asks for: enable and
// disable needed pipelines.delete (#593).
func TestBulkAsksForEachActionsOwnPermission(t *testing.T) {
	withoutOrgResolver(t)
	s := newDepAPIStore(t)
	createOrgPipe(t, s, "a", "A", "org-a", nil)
	for action, want := range map[string]models.Permission{
		"enable":  models.PermPipelinesEdit,
		"disable": models.PermPipelinesEdit,
		"delete":  models.PermPipelinesDelete,
	} {
		var asked models.Permission
		refuse := func(p models.Permission) func(http.Handler) http.Handler {
			asked = p
			return func(http.Handler) http.Handler {
				return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					writeError(w, http.StatusForbidden, "insufficient permissions")
				})
			}
		}
		rec, _ := postBulk(t, s, refuse, "admin", `{"ids":["a"],"action":"`+action+`"}`)
		if asked != want || rec.Code != http.StatusForbidden {
			t.Errorf("%s asked for %q and got %d, want %q and 403", action, asked, rec.Code, want)
		}
	}
	if !exists(s, "a") {
		t.Fatal("a refused request deleted the pipeline")
	}
	// Through the open-source gate, a viewer may not disable.
	if rec, _ := postBulk(t, s, fallbackPermissionMiddleware, "viewer", `{"ids":["a"],"action":"disable"}`); rec.Code != http.StatusForbidden {
		t.Errorf("viewer disable: %d, want 403", rec.Code)
	}
}

// A bulk delete went straight to the store, leaving dependents pointing at
// a pipeline that no longer existed (#593). It refuses that id, names the
// dependents, and goes on with the rest.
func TestBulkDeleteRefusesAPipelineOthersDependOn(t *testing.T) {
	withoutOrgResolver(t)
	s := newDepAPIStore(t)
	createOrgPipe(t, s, "up", "Upstream", "org-a", nil)
	createOrgPipe(t, s, "down", "Downstream", "org-a", []models.DependencyRule{{PipelineID: "up"}})
	createOrgPipe(t, s, "alone", "Alone", "org-a", nil)

	rec, out := postBulk(t, s, fallbackPermissionMiddleware, "admin", `{"ids":["up","alone","missing"],"action":"delete"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	if out.Succeeded != 1 || out.Failed != 2 {
		t.Fatalf("answer %+v, want 1 deleted and 2 refused", out)
	}
	if !exists(s, "up") || !exists(s, "down") || exists(s, "alone") {
		t.Errorf("up %v, down %v, alone %v: want up and down kept, alone deleted", exists(s, "up"), exists(s, "down"), exists(s, "alone"))
	}
	got := map[string]string{}
	for _, e := range out.Errors {
		got[e.ID] = e.Error
	}
	if !strings.Contains(got["up"], "has dependents: Downstream") {
		t.Errorf("up refused with %q, want the dependent named", got["up"])
	}
	if got["missing"] != "not found" {
		t.Errorf("missing refused with %q, want not found", got["missing"])
	}
}

// A dependent named in the same request does not block its upstream,
// whatever the order of the ids.
func TestBulkDeleteTakesAPipelineAndItsDependentsInAnyOrder(t *testing.T) {
	withoutOrgResolver(t)
	s := newDepAPIStore(t)
	createOrgPipe(t, s, "a", "A", "org-a", nil)
	createOrgPipe(t, s, "b", "B", "org-a", []models.DependencyRule{{PipelineID: "a"}})
	createOrgPipe(t, s, "c", "C", "org-a", []models.DependencyRule{{PipelineID: "b"}})

	rec, out := postBulk(t, s, fallbackPermissionMiddleware, "admin", `{"ids":["a","b","c"],"action":"delete"}`)
	if rec.Code != http.StatusOK || out.Succeeded != 3 || out.Failed != 0 {
		t.Fatalf("%d %+v, want all three deleted", rec.Code, out)
	}
	for _, id := range []string{"a", "b", "c"} {
		if exists(s, id) {
			t.Errorf("%s still exists", id)
		}
	}
}

// Enable and disable went straight to the store: a Git-managed pipeline
// could be changed, and no version was recorded (#593). They now go
// through the edit path.
func TestBulkEnableGoesThroughTheEditPath(t *testing.T) {
	withoutOrgResolver(t)
	s := newDepAPIStore(t)
	ui := createOrgPipe(t, s, "ui", "UI", "org-a", nil)
	ui.Enabled = true
	if err := s.UpdatePipeline(ui); err != nil {
		t.Fatal(err)
	}
	git := createOrgPipe(t, s, "git", "Git", "org-a", nil)
	git.Source = models.PipelineSourceGit
	git.Enabled = true
	if err := s.UpdatePipeline(git); err != nil {
		t.Fatal(err)
	}
	before, _ := s.ListPipelineVersions("ui")

	rec, out := postBulk(t, s, fallbackPermissionMiddleware, "admin", `{"ids":["ui","git"],"action":"disable"}`)
	if rec.Code != http.StatusOK || out.Succeeded != 1 || out.Failed != 1 {
		t.Fatalf("%d %+v, want ui disabled and git refused", rec.Code, out)
	}
	if out.Errors[0].ID != "git" || !strings.Contains(out.Errors[0].Error, "Git") {
		t.Errorf("refusal %+v, want git refused as Git-managed", out.Errors[0])
	}
	if p, _ := s.GetPipeline("git"); !p.Enabled {
		t.Error("a Git-managed pipeline was disabled")
	}
	if p, _ := s.GetPipeline("ui"); p.Enabled {
		t.Error("ui is still enabled")
	}
	if after, _ := s.ListPipelineVersions("ui"); len(after) != len(before)+1 {
		t.Errorf("versions of ui: %d before, %d after, want one more", len(before), len(after))
	}
}
