package api

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/golang-jwt/jwt/v5"

	"github.com/Tnsor-Labs/brokoli/store"
)

// ADR-049 phase 2: the pipeline routes that checked the organization only
// (dead-letter queue, dependencies, dependents, the dependency graph and
// bulk actions) keep to the caller's workspaces.
func TestPipelineSideRoutesKeepToTheCallersWorkspaces(t *testing.T) {
	multiTenant(t, map[string][]string{"alice": {wsAlpha}, "carol": {wsAlpha, wsBeta}})
	s := newWorkspaceAccessStore(t)
	seedPipeline(t, s, "pa", wsAlpha)
	pb := seedPipeline(t, s, "pb", wsBeta)
	pc := seedPipeline(t, s, "pc", wsAlpha)
	for _, p := range []string{pb.ID, pc.ID} {
		got, _ := s.GetPipeline(p)
		got.DependsOn = []string{"pa"}
		if err := s.UpdatePipeline(got); err != nil {
			t.Fatal(err)
		}
	}

	r := chi.NewRouter()
	r.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(rw http.ResponseWriter, req *http.Request) {
			claims := jwt.MapClaims{"sub": req.Header.Get("X-Test-User"), "role": "admin"}
			ctx := context.WithValue(req.Context(), "claims", &claims)
			ctx = context.WithValue(ctx, OrgIDContextKey{}, wsOrg)
			next.ServeHTTP(rw, req.WithContext(ctx))
		})
	})
	r.Use(WorkspaceMiddleware)
	r.Get("/api/pipelines/{id}/dlq", dlqListHandler(s))
	r.Get("/api/pipelines/{id}/deps", pipelineDepsHandler(s))
	r.Get("/api/pipelines/{id}/dependents", pipelineDependentsHandler(s))
	r.Get("/api/pipelines/dependency-graph", pipelineDependencyGraphHandler(s))
	r.Post("/api/pipelines/bulk", bulkPipelineHandler(s))
	r.Get("/api/dlq", NewAlertHandler(s).ListDLQ)
	db := s.(*store.SQLiteStore).RawDB().(*sql.DB)
	for id, pipe := range map[string]string{"dlq-a": "pa", "dlq-b": "pb"} {
		if _, err := db.Exec(`INSERT INTO dead_letter_queue (id, pipeline_id, run_id, error, created_at) VALUES (?, ?, 'r', 'boom', '2026-11-01T00:00:00Z')`, id, pipe); err != nil {
			t.Fatal(err)
		}
	}
	call := func(method, path, user string, body []byte) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, bytes.NewReader(body))
		req.Header.Set("X-Test-User", user)
		req.Header.Set("X-Workspace-ID", wsAlpha)
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)
		return rec
	}

	for _, path := range []string{"/api/pipelines/pb/dlq", "/api/pipelines/pb/deps"} {
		if rec := call("GET", path, "alice", nil); rec.Code != http.StatusNotFound {
			t.Errorf("alice (alpha only) GET %s: %d, want 404", path, rec.Code)
		}
		if rec := call("GET", path, "carol", nil); rec.Code != http.StatusOK {
			t.Errorf("carol (both) GET %s: %d, want 200", path, rec.Code)
		}
	}

	names := func(rec *httptest.ResponseRecorder) map[string]bool {
		var out []map[string]any
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
		got := map[string]bool{}
		for _, d := range out {
			got[d["id"].(string)] = true
		}
		return got
	}
	if got := names(call("GET", "/api/pipelines/pa/dependents", "alice", nil)); !got["pc"] || got["pb"] {
		t.Errorf("dependents for alice: %v, want pc and not beta's pb", got)
	}
	if got := names(call("GET", "/api/pipelines/pa/dependents", "carol", nil)); !got["pc"] || !got["pb"] {
		t.Errorf("dependents for carol (both): %v, want pb and pc", got)
	}

	var graph struct {
		Nodes []map[string]any `json:"nodes"`
	}
	_ = json.Unmarshal(call("GET", "/api/pipelines/dependency-graph", "carol", nil).Body.Bytes(), &graph)
	inGraph := map[string]bool{}
	for _, n := range graph.Nodes {
		inGraph[n["id"].(string)] = true
	}
	if !inGraph["pa"] || !inGraph["pc"] || inGraph["pb"] {
		t.Errorf("graph in alpha: %v, want pa and pc, not beta's pb", inGraph)
	}

	var dlq []store.DLQEntry
	_ = json.Unmarshal(call("GET", "/api/dlq", "carol", nil).Body.Bytes(), &dlq)
	if len(dlq) != 1 || dlq[0].ID != "dlq-a" {
		t.Errorf("dead-letter list in alpha: %+v, want only dlq-a", dlq)
	}

	rec := call("POST", "/api/pipelines/bulk", "alice", []byte(`{"ids":["pb"],"action":"disable"}`))
	if bytes.Contains(rec.Body.Bytes(), []byte("access denied")) || !bytes.Contains(rec.Body.Bytes(), []byte("not found")) {
		t.Errorf("bulk on another workspace's pipeline: %s, want it answered as not found", rec.Body.String())
	}
	if got, _ := s.GetPipeline("pb"); !got.Enabled {
		t.Error("alice disabled a pipeline in a workspace she does not belong to")
	}
}
