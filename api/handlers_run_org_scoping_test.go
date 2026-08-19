package api

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
)

// TestRunHandler_CrossOrgAccessDenied pins the org-scoping fix for
// ListByPipeline, Backfill, DryRun, and NodeStats: sibling endpoints in
// this file (Get, TriggerRun, GetPlan, ...) already checked
// ValidateOrgAccess before this fix; these four didn't, so an editor in
// one org could list another org's run history, trigger real execution
// (Backfill/DryRun actually run the pipeline) against another org's live
// connections, or read another org's per-node timing stats.
func TestRunHandler_CrossOrgAccessDenied(t *testing.T) {
	withoutOrgResolver(t)
	s := newDepAPIStore(t)
	h := NewRunHandler(s, nil) // nil engine: a denied request must never reach it

	// Org B owns the pipeline; the request below carries org A.
	pipe := createOrgPipe(t, s, "b-pipe", "B Pipeline", "org-b", nil)

	cases := []struct {
		name    string
		method  string
		pattern string
		path    string
		handler func(*RunHandler) func(http.ResponseWriter, *http.Request)
	}{
		{"ListByPipeline", "GET", "/pipelines/{id}/runs", "/pipelines/" + pipe.ID + "/runs",
			func(h *RunHandler) func(http.ResponseWriter, *http.Request) { return h.ListByPipeline }},
		{"Backfill", "POST", "/pipelines/{id}/backfill", "/pipelines/" + pipe.ID + "/backfill",
			func(h *RunHandler) func(http.ResponseWriter, *http.Request) { return h.Backfill }},
		{"DryRun", "POST", "/pipelines/{id}/dry-run", "/pipelines/" + pipe.ID + "/dry-run",
			func(h *RunHandler) func(http.ResponseWriter, *http.Request) { return h.DryRun }},
		{"NodeStats", "GET", "/pipelines/{id}/node-stats", "/pipelines/" + pipe.ID + "/node-stats",
			func(h *RunHandler) func(http.ResponseWriter, *http.Request) { return h.NodeStats }},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			router := chi.NewRouter()
			switch tc.method {
			case "GET":
				router.Get(tc.pattern, tc.handler(h))
			case "POST":
				router.Post(tc.pattern, tc.handler(h))
			}

			req := reqWithOrg(tc.method, tc.path, []byte(`{"start_date":"2026-01-01","end_date":"2026-01-02"}`), "org-a")
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, req)

			if rec.Code != http.StatusNotFound {
				t.Fatalf("%s as org-a against org-b's pipeline: status = %d, want 404; body=%s",
					tc.name, rec.Code, rec.Body.String())
			}
		})
	}
}

// TestRunHandler_SameOrgAccessAllowed confirms the fix isn't a blanket
// deny -- a caller in the pipeline's own org still reaches past the
// check (ListByPipeline and NodeStats are exercised since they don't
// require an engine; Backfill/DryRun's own-org path needs a real engine
// and is covered by this package's pre-existing execution tests, not
// duplicated here).
func TestRunHandler_SameOrgAccessAllowed(t *testing.T) {
	withoutOrgResolver(t)
	s := newDepAPIStore(t)
	h := NewRunHandler(s, nil)
	pipe := createOrgPipe(t, s, "a-pipe", "A Pipeline", "org-a", nil)

	router := chi.NewRouter()
	router.Get("/pipelines/{id}/runs", h.ListByPipeline)
	router.Get("/pipelines/{id}/node-stats", h.NodeStats)

	for _, path := range []string{
		"/pipelines/" + pipe.ID + "/runs",
		"/pipelines/" + pipe.ID + "/node-stats",
	} {
		req := reqWithOrg("GET", path, nil, "org-a")
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s as org-a against org-a's own pipeline: status = %d, want 200; body=%s",
				path, rec.Code, rec.Body.String())
		}
	}
}
