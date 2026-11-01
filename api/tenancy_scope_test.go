package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/golang-jwt/jwt/v5"

	"github.com/Tnsor-Labs/brokoli/models"
	"github.com/Tnsor-Labs/brokoli/pkg/common"
)

// ADR-049 phase 1, through the real middleware: runs by id, the runs a
// person started, and alerts, for a member of one workspace and a member
// of both. Each was filtered by organization alone before.

type scopeWorld struct {
	router       chi.Router
	runA, runB   string
	pipeA, pipeB string
}

func newScopeWorld(t *testing.T) scopeWorld {
	t.Helper()
	multiTenant(t, map[string][]string{"alice": {wsAlpha}, "carol": {wsAlpha, wsBeta}})
	s := newWorkspaceAccessStore(t)
	pA := seedPipeline(t, s, "pipe-alpha", wsAlpha).ID
	pB := seedPipeline(t, s, "pipe-beta", wsBeta).ID
	mkRun := func(pipelineID string) string {
		now := time.Now()
		run := &models.Run{ID: common.NewID(), PipelineID: pipelineID, Status: models.RunStatusFailed, OrgID: wsOrg, StartedAt: &now}
		if err := s.CreateRun(run); err != nil {
			t.Fatal(err)
		}
		if err := s.SetRunAttribution(run.ID, &models.RunAttribution{Kind: models.RunTriggerKindUser, UserID: "carol"}); err != nil {
			t.Fatal(err)
		}
		if err := s.CreateAlert(&models.Alert{ID: common.NewID(), OrgID: wsOrg, Kind: "run_failed", Severity: "error",
			Title: "failed", PipelineID: pipelineID, RunID: run.ID, CreatedAt: now}); err != nil {
			t.Fatal(err)
		}
		return run.ID
	}
	w := scopeWorld{pipeA: pA, pipeB: pB, runA: mkRun(pA), runB: mkRun(pB)}

	rh := NewRunHandler(s, nil)
	ah := NewAlertHandler(s)
	r := chi.NewRouter()
	// What authentication leaves on the request: the token's claims and
	// its organization. The scope is then built by the real middleware.
	r.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(rw http.ResponseWriter, req *http.Request) {
			claims := jwt.MapClaims{"sub": req.Header.Get("X-Test-User"), "role": "admin"}
			ctx := context.WithValue(req.Context(), "claims", &claims)
			ctx = context.WithValue(ctx, OrgIDContextKey{}, wsOrg)
			next.ServeHTTP(rw, req.WithContext(ctx))
		})
	})
	r.Use(WorkspaceMiddleware)
	r.Get("/api/runs/{id}", rh.Get)
	r.Get("/api/runs/{id}/logs", rh.GetLogs)
	r.Get("/api/pipelines/{id}/grid", rh.Grid)
	r.Get("/api/runs", rh.ListStartedBy)
	r.Get("/api/alerts", ah.List)
	w.router = r
	return w
}

func (w scopeWorld) get(t *testing.T, path, user, ws string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.Header.Set("X-Test-User", user)
	if ws != "" {
		req.Header.Set("X-Workspace-ID", ws)
	}
	rec := httptest.NewRecorder()
	w.router.ServeHTTP(rec, req)
	return rec
}

func TestRunRoutesByIDKeepToTheCallersWorkspaces(t *testing.T) {
	w := newScopeWorld(t)
	for _, path := range []string{"/api/runs/" + w.runB, "/api/runs/" + w.runB + "/logs", "/api/pipelines/" + w.pipeB + "/grid"} {
		if rec := w.get(t, path, "alice", wsAlpha); rec.Code != http.StatusNotFound {
			t.Errorf("alice (alpha only) GET %s: %d, want 404", path, rec.Code)
		}
		// A member of both reaches it by id from either workspace.
		if rec := w.get(t, path, "carol", wsAlpha); rec.Code != http.StatusOK {
			t.Errorf("carol (both) GET %s while in alpha: %d, want 200", path, rec.Code)
		}
	}
	if rec := w.get(t, "/api/runs/"+w.runA, "alice", wsAlpha); rec.Code != http.StatusOK {
		t.Errorf("alice GET her own workspace's run: %d", rec.Code)
	}
}

func TestRunsYouStartedAreTheCurrentWorkspaces(t *testing.T) {
	w := newScopeWorld(t)
	for ws, want := range map[string]string{wsAlpha: w.runA, wsBeta: w.runB} {
		rec := w.get(t, "/api/runs?started_by=me", "carol", ws)
		var runs []models.Run
		_ = json.Unmarshal(rec.Body.Bytes(), &runs)
		if rec.Code != http.StatusOK || len(runs) != 1 || runs[0].ID != want {
			t.Errorf("carol's runs in %s: %d %d runs, want only %s", ws, rec.Code, len(runs), want)
		}
	}
}

func TestAlertsAreTheCurrentWorkspaces(t *testing.T) {
	w := newScopeWorld(t)
	for ws, wantPipe := range map[string]string{wsAlpha: w.pipeA, wsBeta: w.pipeB} {
		rec := w.get(t, "/api/alerts", "carol", ws)
		var out struct {
			Alerts []models.Alert `json:"alerts"`
			Unread int            `json:"unread_count"`
		}
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
		if rec.Code != http.StatusOK || len(out.Alerts) != 1 || out.Alerts[0].PipelineID != wantPipe || out.Unread != 1 {
			t.Errorf("alerts in %s: %d %+v unread %d, want one alert of %s and unread 1", ws, rec.Code, out.Alerts, out.Unread, wantPipe)
		}
	}
}

// The middleware carries the scope; a handler mounted without it computes
// the same one, so the rules do not depend on how it was reached.
func TestRequestScopeIsTheSameWithOrWithoutTheMiddleware(t *testing.T) {
	multiTenant(t, map[string][]string{"carol": {wsAlpha, wsBeta}})
	var viaMiddleware string
	h := WorkspaceMiddleware(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		s := RequestScope(r)
		viaMiddleware = s.Workspace
		if !s.Sees(wsOrg, wsBeta) || s.Lists(wsOrg, wsBeta) {
			t.Error("scope from the middleware: wrong rules")
		}
	}))
	req := request("GET", "/api/x", "", "carol", "", nil)
	req.Header.Set("X-Workspace-ID", wsAlpha)
	h.ServeHTTP(httptest.NewRecorder(), req)
	direct := RequestScope(request("GET", "/api/x", "", "carol", wsAlpha, nil))
	if viaMiddleware != wsAlpha || direct.Workspace != wsAlpha || !direct.Sees(wsOrg, wsBeta) || direct.Lists(wsOrg, wsBeta) {
		t.Fatalf("middleware %q, direct %+v", viaMiddleware, direct)
	}
}
