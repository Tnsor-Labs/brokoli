package api

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/golang-jwt/jwt/v5"

	"github.com/Tnsor-Labs/brokoli/engine"
	"github.com/Tnsor-Labs/brokoli/extensions"
	"github.com/Tnsor-Labs/brokoli/models"
	"github.com/Tnsor-Labs/brokoli/pkg/sodp"
	"github.com/Tnsor-Labs/brokoli/store"
)

func orgRequest(method, path, org string) *http.Request {
	r := httptest.NewRequest(method, path, strings.NewReader("{}"))
	claims := &jwt.MapClaims{"sub": "u1", "role": "admin"}
	ctx := context.WithValue(r.Context(), "claims", claims)
	return r.WithContext(context.WithValue(ctx, OrgIDContextKey{}, org))
}

// A pipeline the caller cannot see answers the same whether it is missing
// or another organization's. The checks ran only when the pipeline could
// be read, so "missing" got a 500 or an empty 200 where "another
// organization's" got 404 -- which ids exist elsewhere, by status code.
func TestPipelineScopedRoutesAnswerAlikeForMissingAndForeign(t *testing.T) {
	s := newOrgCheckStore(t)
	e := engine.NewEngine(s)
	t.Cleanup(func() { _ = e.Close(context.Background()) })
	foreign := &models.Pipeline{ID: "foreign", PipelineID: "foreign", Name: "foreign", OrgID: "org-other",
		Nodes: []models.Node{}, Edges: []models.Edge{}}
	if err := s.CreatePipeline(foreign); err != nil {
		t.Fatal(err)
	}
	r := chi.NewRouter()
	RegisterRoutes(r, s, e, sodp.NewServer(), nil, nil, nil)

	for _, route := range []struct{ method, path string }{
		{http.MethodPost, "/api/pipelines/%s/run"},
		{http.MethodPost, "/api/pipelines/%s/backfill"},
		{http.MethodGet, "/api/pipelines/%s/runs"},
		{http.MethodGet, "/api/pipelines/%s/node-stats"},
		{http.MethodGet, "/api/pipelines/%s/versions"},
	} {
		var answers []string
		for _, id := range []string{"foreign", "does-not-exist"} {
			rec := httptest.NewRecorder()
			r.ServeHTTP(rec, orgRequest(route.method, strings.Replace(route.path, "%s", id, 1), "org-mine"))
			answers = append(answers, rec.Result().Status+" "+strings.TrimSpace(rec.Body.String()))
			if rec.Code != http.StatusNotFound {
				t.Errorf("%s %s for %s: status %d, want 404", route.method, route.path, id, rec.Code)
			}
		}
		if answers[0] != answers[1] {
			t.Errorf("%s %s: foreign answered %q, missing answered %q", route.method, route.path, answers[0], answers[1])
		}
	}
}

// failingPipelineReads is a store whose pipeline reads fail for one id, the
// way a database blip does.
type failingPipelineReads struct {
	store.Store
	failing string
}

func (f failingPipelineReads) GetPipeline(id string) (*models.Pipeline, error) {
	if id == f.failing {
		return nil, errors.New("database is unavailable")
	}
	return f.Store.GetPipeline(id)
}

// A run whose pipeline cannot be read is not shown: the check used to be
// skipped whenever the read failed, so a blip opened another organization's
// runs.
func TestRunWithAnUnreadablePipelineIsRefused(t *testing.T) {
	base := newOrgCheckStore(t)
	if err := base.CreatePipeline(&models.Pipeline{ID: "p-other", PipelineID: "p-other", Name: "p", OrgID: "org-other",
		Nodes: []models.Node{}, Edges: []models.Edge{}}); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	if err := base.CreateRun(&models.Run{ID: "their-run", PipelineID: "p-other", Status: models.RunStatusSuccess, StartedAt: &now, OrgID: "org-other"}); err != nil {
		t.Fatal(err)
	}
	s := failingPipelineReads{Store: base, failing: "p-other"}
	e := engine.NewEngine(base)
	t.Cleanup(func() { _ = e.Close(context.Background()) })
	r := chi.NewRouter()
	RegisterRoutes(r, s, e, sodp.NewServer(), nil, nil, nil)
	for _, path := range []string{"/api/runs/their-run", "/api/runs/their-run/plan", "/api/runs/their-run/instances", "/api/pipelines/p-other/runs", "/api/pipelines/p-other/versions"} {
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, orgRequest(http.MethodGet, path, "org-mine"))
		if rec.Code != http.StatusNotFound {
			t.Errorf("GET %s with the pipeline unreadable: status %d, want 404 (%s)", path, rec.Code, rec.Body.String())
		}
	}
}

type recordingGitSync struct {
	extensions.CommunityGitSync
	pushed []string
}

func (g *recordingGitSync) Push(id string) error { g.pushed = append(g.pushed, id); return nil }

// Push exports a pipeline into the repository, so it has to be one the
// caller's organization can see, named by id or by pipeline_id.
func TestGitPushOnlyExportsTheCallersPipelines(t *testing.T) {
	s := newOrgCheckStore(t)
	e := engine.NewEngine(s)
	t.Cleanup(func() { _ = e.Close(context.Background()) })
	for _, p := range []*models.Pipeline{
		{ID: "mine-id", PipelineID: "mine-slug", Name: "mine", OrgID: "org-mine", Nodes: []models.Node{}, Edges: []models.Edge{}},
		{ID: "theirs-id", PipelineID: "theirs-slug", Name: "theirs", OrgID: "org-other", Nodes: []models.Node{}, Edges: []models.Edge{}},
	} {
		if err := s.CreatePipeline(p); err != nil {
			t.Fatal(err)
		}
	}
	git := &recordingGitSync{}
	r := chi.NewRouter()
	RegisterRoutes(r, s, e, sodp.NewServer(), nil, &extensions.Registry{GitSync: git}, nil)
	for _, c := range []struct {
		id   string
		want int
	}{{"theirs-id", 404}, {"theirs-slug", 404}, {"nope", 404}, {"mine-id", 200}, {"mine-slug", 200}} {
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, orgRequest(http.MethodPost, "/api/git/push/"+c.id, "org-mine"))
		if rec.Code != c.want {
			t.Errorf("push %s: status %d, want %d", c.id, rec.Code, c.want)
		}
	}
	if strings.Join(git.pushed, ",") != "mine-id,mine-slug" {
		t.Errorf("pushed %v, want only the caller's pipeline", git.pushed)
	}
}

// Deleting a legacy pipeline with no organization looks for its dependents
// in the caller's organization, not in every organization: the store reads
// an empty organization as "all pipelines".
func TestDeletingALegacyPipelineSeesOnlyTheCallersDependents(t *testing.T) {
	s := newOrgCheckStore(t)
	legacy := &models.Pipeline{ID: "legacy", PipelineID: "legacy", Name: "legacy", Nodes: []models.Node{}, Edges: []models.Edge{}}
	if err := s.CreatePipeline(legacy); err != nil {
		t.Fatal(err)
	}
	dependent := &models.Pipeline{ID: "theirs", PipelineID: "theirs", Name: "Their secret pipeline", OrgID: "org-other",
		Nodes: []models.Node{}, Edges: []models.Edge{},
		DependencyRules: []models.DependencyRule{{PipelineID: "legacy", Mode: models.DepModeTrigger}}}
	if err := s.CreatePipeline(dependent); err != nil {
		t.Fatal(err)
	}
	if got := depsScope(orgRequest(http.MethodDelete, "/", "org-mine"), legacy.OrgID); got != "org-mine" {
		t.Fatalf("depsScope for a legacy pipeline = %q, want the caller's organization", got)
	}
	h := NewPipelineHandler(s, nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", "legacy")
	req := orgRequest(http.MethodDelete, "/api/pipelines/legacy", "org-mine")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	rec := httptest.NewRecorder()
	h.Delete(rec, req)
	if strings.Contains(rec.Body.String(), "Their secret pipeline") {
		t.Errorf("delete listed another organization's dependent: %s", rec.Body.String())
	}
	if p, err := s.GetPipeline("theirs"); err != nil || p == nil {
		t.Errorf("another organization's dependent was affected: %v", err)
	}
}
