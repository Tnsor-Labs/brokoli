package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/golang-jwt/jwt/v5"

	"github.com/Tnsor-Labs/brokoli/models"
	"github.com/Tnsor-Labs/brokoli/store"
)

// Pipelines are reachable by id, and until now those handlers checked only
// the org. Two workspaces inside one org are a supported shape (the team
// plan sells several), so a member of workspace A could read, edit and
// delete workspace B's pipeline by guessing or being given its id, while
// every list endpoint correctly hid it.

const (
	wsAlpha = "ws-alpha"
	wsBeta  = "ws-beta"
	wsOrg   = "org-shared"
)

func newWorkspaceAccessStore(t *testing.T) store.Store {
	t.Helper()
	s, err := store.NewSQLiteStore(filepath.Join(t.TempDir(), "ws.db"))
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

// multiTenant installs the resolvers that mark an enterprise deployment,
// with userID mapped to the workspaces it owns.
func multiTenant(t *testing.T, owned map[string][]string) {
	t.Helper()
	origWS, origOrg := UserWorkspaceResolverFunc, OrgResolverFunc
	UserWorkspaceResolverFunc = func(userID string) []string { return owned[userID] }
	OrgResolverFunc = func(userID string) string { return wsOrg }
	t.Cleanup(func() {
		UserWorkspaceResolverFunc, OrgResolverFunc = origWS, origOrg
	})
}

// seedPipeline stores a pipeline in one org and workspace.
func seedPipeline(t *testing.T, s store.Store, id, wsID string) *models.Pipeline {
	t.Helper()
	p := &models.Pipeline{
		ID:          id,
		PipelineID:  id,
		Name:        id,
		OrgID:       wsOrg,
		WorkspaceID: wsID,
		Enabled:     true,
		Nodes: []models.Node{
			{ID: "src", Type: models.NodeTypeSourceAPI, Name: "src",
				Config: map[string]interface{}{"url": "/api/samples/data/employees.json", "method": "GET"}},
		},
		Edges: []models.Edge{},
	}
	if err := s.CreatePipeline(p); err != nil {
		t.Fatalf("CreatePipeline(%s): %v", id, err)
	}
	return p
}

// request builds a call as userID, filed under the given current workspace.
func request(method, path, pipelineID, userID, currentWS string, body []byte) *http.Request {
	var r *http.Request
	if body == nil {
		r = httptest.NewRequest(method, path, nil)
	} else {
		r = httptest.NewRequest(method, path, bytes.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
	}
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", pipelineID)
	ctx := context.WithValue(r.Context(), chi.RouteCtxKey, rctx)
	ctx = context.WithValue(ctx, OrgIDContextKey{}, wsOrg)
	claims := jwt.MapClaims{"sub": userID, "role": "admin"}
	ctx = context.WithValue(ctx, "claims", &claims)
	if currentWS != "" {
		ctx = context.WithValue(ctx, workspaceKey, currentWS)
	}
	return r.WithContext(ctx)
}

func TestPipelineGet_DeniedAcrossWorkspacesInTheSameOrg(t *testing.T) {
	multiTenant(t, map[string][]string{"alice": {wsAlpha}, "bob": {wsBeta}})
	s := newWorkspaceAccessStore(t)
	seedPipeline(t, s, "beta-pipeline", wsBeta)
	h := NewPipelineHandler(s, nil)

	rec := httptest.NewRecorder()
	h.Get(rec, request("GET", "/api/pipelines/beta-pipeline", "beta-pipeline", "alice", wsAlpha, nil))

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404: alice (workspace alpha) read a pipeline owned by workspace beta; body=%s",
			rec.Code, rec.Body.String())
	}
}

func TestPipelineGet_AllowedInsideOwnWorkspace(t *testing.T) {
	multiTenant(t, map[string][]string{"alice": {wsAlpha}})
	s := newWorkspaceAccessStore(t)
	seedPipeline(t, s, "alpha-pipeline", wsAlpha)
	h := NewPipelineHandler(s, nil)

	rec := httptest.NewRecorder()
	h.Get(rec, request("GET", "/api/pipelines/alpha-pipeline", "alpha-pipeline", "alice", wsAlpha, nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 for a pipeline in the caller's own workspace; body=%s",
			rec.Code, rec.Body.String())
	}
}

// The regression that a header-only comparison would introduce: the request
// is filed under alpha (no header sent, so the first owned workspace wins),
// but the caller also owns beta and the pipeline is theirs.
func TestPipelineGet_AllowedInASecondOwnedWorkspace(t *testing.T) {
	multiTenant(t, map[string][]string{"alice": {wsAlpha, wsBeta}})
	s := newWorkspaceAccessStore(t)
	seedPipeline(t, s, "second-ws-pipeline", wsBeta)
	h := NewPipelineHandler(s, nil)

	rec := httptest.NewRecorder()
	h.Get(rec, request("GET", "/api/pipelines/second-ws-pipeline", "second-ws-pipeline", "alice", wsAlpha, nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: alice owns beta too, so her own pipeline must not be hidden; body=%s",
			rec.Code, rec.Body.String())
	}
}

func TestPipelineUpdate_DeniedAcrossWorkspacesAndLeavesTheRowIntact(t *testing.T) {
	multiTenant(t, map[string][]string{"alice": {wsAlpha}})
	s := newWorkspaceAccessStore(t)
	seedPipeline(t, s, "beta-update", wsBeta)
	h := NewPipelineHandler(s, nil)

	body, _ := json.Marshal(map[string]any{
		"name":    "renamed by another workspace",
		"enabled": true,
		"nodes": []map[string]any{
			{"id": "src", "type": "source_api", "name": "src",
				"config": map[string]any{"url": "/api/samples/data/employees.json", "method": "GET"}},
		},
		"edges": []any{},
	})
	rec := httptest.NewRecorder()
	h.Update(rec, request("PUT", "/api/pipelines/beta-update", "beta-update", "alice", wsAlpha, body))

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 for a cross-workspace update; body=%s", rec.Code, rec.Body.String())
	}
	after, err := s.GetPipeline("beta-update")
	if err != nil {
		t.Fatalf("GetPipeline after denied update: %v", err)
	}
	if after.Name != "beta-update" {
		t.Errorf("name = %q, want it unchanged: the denied update still wrote", after.Name)
	}
}

func TestPipelineDelete_DeniedAcrossWorkspacesAndLeavesTheRowPresent(t *testing.T) {
	multiTenant(t, map[string][]string{"alice": {wsAlpha}})
	s := newWorkspaceAccessStore(t)
	seedPipeline(t, s, "beta-delete", wsBeta)
	h := NewPipelineHandler(s, nil)

	rec := httptest.NewRecorder()
	h.Delete(rec, request("DELETE", "/api/pipelines/beta-delete", "beta-delete", "alice", wsAlpha, nil))

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 for a cross-workspace delete; body=%s", rec.Code, rec.Body.String())
	}
	if _, err := s.GetPipeline("beta-delete"); err != nil {
		t.Fatalf("the pipeline is gone after a denied delete: %v", err)
	}
}

// Every other by-id handler shares the same exposure.
func TestPipelineByIDHandlers_AllDenyAcrossWorkspaces(t *testing.T) {
	cases := []struct {
		name   string
		method string
		call   func(h *PipelineHandler, w http.ResponseWriter, r *http.Request)
	}{
		{"Export", "GET", func(h *PipelineHandler, w http.ResponseWriter, r *http.Request) { h.Export(w, r) }},
		{"Validate", "GET", func(h *PipelineHandler, w http.ResponseWriter, r *http.Request) { h.Validate(w, r) }},
		{"Plan", "GET", func(h *PipelineHandler, w http.ResponseWriter, r *http.Request) { h.Plan(w, r) }},
		{"ListVersions", "GET", func(h *PipelineHandler, w http.ResponseWriter, r *http.Request) { h.ListVersions(w, r) }},
		{"ValidateNodes", "POST", func(h *PipelineHandler, w http.ResponseWriter, r *http.Request) { h.ValidateNodes(w, r) }},
		{"Clone", "POST", func(h *PipelineHandler, w http.ResponseWriter, r *http.Request) { h.Clone(w, r) }},
		{"Rollback", "POST", func(h *PipelineHandler, w http.ResponseWriter, r *http.Request) { h.Rollback(w, r) }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			multiTenant(t, map[string][]string{"alice": {wsAlpha}})
			s := newWorkspaceAccessStore(t)
			seedPipeline(t, s, "beta-target", wsBeta)
			h := NewPipelineHandler(s, nil)

			var body []byte
			if tc.method == "POST" {
				body = []byte(`{}`)
			}
			rec := httptest.NewRecorder()
			tc.call(h, rec, request(tc.method, "/api/pipelines/beta-target", "beta-target", "alice", wsAlpha, body))

			if rec.Code == http.StatusOK || rec.Code == http.StatusCreated {
				t.Fatalf("%s returned %d for a pipeline in another workspace; body=%s",
					tc.name, rec.Code, rec.Body.String())
			}
			if rec.Code != http.StatusNotFound {
				t.Logf("%s denied with %d rather than 404 (acceptable, but 404 avoids leaking existence)", tc.name, rec.Code)
			}
		})
	}
}

// A single-tenant build installs no resolvers, so nothing may change for it.
func TestPipelineGet_SingleTenantUnaffected(t *testing.T) {
	origWS, origOrg := UserWorkspaceResolverFunc, OrgResolverFunc
	UserWorkspaceResolverFunc, OrgResolverFunc = nil, nil
	t.Cleanup(func() { UserWorkspaceResolverFunc, OrgResolverFunc = origWS, origOrg })

	s := newWorkspaceAccessStore(t)
	p := &models.Pipeline{
		ID: "solo", PipelineID: "solo", Name: "solo", WorkspaceID: wsBeta, Enabled: true,
		Nodes: []models.Node{{ID: "src", Type: models.NodeTypeSourceAPI, Name: "src",
			Config: map[string]interface{}{"url": "/api/samples/data/employees.json", "method": "GET"}}},
		Edges: []models.Edge{},
	}
	if err := s.CreatePipeline(p); err != nil {
		t.Fatalf("CreatePipeline: %v", err)
	}
	h := NewPipelineHandler(s, nil)

	r := httptest.NewRequest("GET", "/api/pipelines/solo", nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", "solo")
	r = r.WithContext(context.WithValue(r.Context(), chi.RouteCtxKey, rctx))
	rec := httptest.NewRecorder()
	h.Get(rec, r)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: a single-tenant install has no workspace boundary to enforce; body=%s",
			rec.Code, rec.Body.String())
	}
}

// Pre-workspace rows carry "default" (the column is NOT NULL DEFAULT
// 'default'), and must stay reachable inside their org.
func TestPipelineGet_LegacyDefaultWorkspaceStillReachable(t *testing.T) {
	multiTenant(t, map[string][]string{"alice": {wsAlpha}})
	s := newWorkspaceAccessStore(t)
	seedPipeline(t, s, "legacy", models.DefaultWorkspaceID)
	h := NewPipelineHandler(s, nil)

	rec := httptest.NewRecorder()
	h.Get(rec, request("GET", "/api/pipelines/legacy", "legacy", "alice", wsAlpha, nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 for a pre-workspace row inside the caller's org; body=%s",
			rec.Code, rec.Body.String())
	}
}

// An org member whose workspace set is empty gets nothing, matching
// validateConnectionAccess's "org user with no workspaces" rule.
func TestPipelineGet_DeniedWhenCallerOwnsNoWorkspace(t *testing.T) {
	multiTenant(t, map[string][]string{})
	s := newWorkspaceAccessStore(t)
	seedPipeline(t, s, "beta-orphan", wsBeta)
	h := NewPipelineHandler(s, nil)

	rec := httptest.NewRecorder()
	h.Get(rec, request("GET", "/api/pipelines/beta-orphan", "beta-orphan", "nobody", wsAlpha, nil))

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 for a caller with no workspaces; body=%s", rec.Code, rec.Body.String())
	}
}

// Enterprise installs the org resolver and the workspace resolver at two
// separate points, so they are independent globals rather than one switch.
// With org context set but no workspace resolver there is no owned set to
// consult, and the early return is what keeps userOwnsWorkspace from
// calling a nil function value: without it this panics rather than denying.
func TestPipelineGet_OrgWithoutWorkspaceResolverIsAllowed(t *testing.T) {
	origWS, origOrg := UserWorkspaceResolverFunc, OrgResolverFunc
	UserWorkspaceResolverFunc = nil
	OrgResolverFunc = func(userID string) string { return wsOrg }
	t.Cleanup(func() { UserWorkspaceResolverFunc, OrgResolverFunc = origWS, origOrg })

	s := newWorkspaceAccessStore(t)
	seedPipeline(t, s, "no-resolver", wsBeta)
	h := NewPipelineHandler(s, nil)

	rec := httptest.NewRecorder()
	h.Get(rec, request("GET", "/api/pipelines/no-resolver", "no-resolver", "alice", wsAlpha, nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: with no workspace resolver installed there is no set to check against; body=%s",
			rec.Code, rec.Body.String())
	}
}
