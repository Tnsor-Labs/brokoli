package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/golang-jwt/jwt/v5"
)

// The webhook routes skip the membership check because they carry their
// own authorization. Any other POST whose path merely contains
// "/webhook" must not: a signed-in member naming a workspace they do not
// belong to was let through, and the handler then listed and wrote in it.
func TestWorkspaceWebhookExemptionIsExact(t *testing.T) {
	previous := UserWorkspaceResolverFunc
	UserWorkspaceResolverFunc = func(string) []string { return []string{"owned-workspace"} }
	t.Cleanup(func() { UserWorkspaceResolverFunc = previous })

	handler := WorkspaceMiddleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	claims := jwt.MapClaims{"sub": "user-1"}

	for _, path := range []string{
		"/api/integrations/github/repositories/webhook/repo/sync", // a GitHub owner named webhook
		"/api/pipelines/webhook/backfill",
		"/api/pipelines/p1%2Fwebhook/run",
		"/api/pipelines/p1/webhooks",
	} {
		req := httptest.NewRequest(http.MethodPost, path, nil)
		req.Header.Set("X-Workspace-ID", "foreign-workspace")
		req = req.WithContext(context.WithValue(req.Context(), "claims", &claims))
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusForbidden {
			t.Errorf("POST %s into a foreign workspace: status = %d, want 403", path, rec.Code)
		}
	}

	// The real webhook routes still pass with no session at all.
	for _, path := range []string{"/api/pipelines/p1/webhook", "/api/git/webhook"} {
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, path, nil))
		if rec.Code != http.StatusNoContent {
			t.Errorf("POST %s: status = %d, want the webhook to pass through", path, rec.Code)
		}
	}
}

// Where the middleware did not resolve the scope (a route it skips), a
// workspace header the caller does not belong to must not become the
// workspace the request lists, acts or writes in.
func TestUncheckedWorkspaceHeaderIsRefused(t *testing.T) {
	previous := UserWorkspaceResolverFunc
	UserWorkspaceResolverFunc = func(string) []string { return []string{"owned-workspace"} }
	t.Cleanup(func() { UserWorkspaceResolverFunc = previous })

	claims := jwt.MapClaims{"sub": "user-1"}
	request := func(ws string) *http.Request {
		req := httptest.NewRequest(http.MethodGet, "/api/auth/me", nil)
		ctx := context.WithValue(req.Context(), "claims", &claims)
		ctx = context.WithValue(ctx, OrgIDContextKey{}, "org-1")
		ctx = context.WithValue(ctx, workspaceKey, ws)
		return req.WithContext(ctx)
	}

	foreign := request("foreign-workspace")
	if s := RequestScope(foreign); s.Valid() {
		t.Errorf("scope for a foreign workspace = %+v, want one that sees nothing", s)
	}
	if ws, ok := effectiveWorkspace(foreign); ok {
		t.Errorf("effectiveWorkspace = %q, true; want refused", ws)
	}
	if ws, ok := writeWorkspace(foreign, ""); ok {
		t.Errorf("writeWorkspace = %q, true; want refused", ws)
	}

	own := request("owned-workspace")
	if s := RequestScope(own); !s.Valid() || s.Workspace != "owned-workspace" {
		t.Errorf("scope for an own workspace = %+v", s)
	}
	if ws, ok := effectiveWorkspace(request("default")); !ok || ws != "owned-workspace" {
		t.Errorf("effectiveWorkspace for default = %q, %v; want owned-workspace", ws, ok)
	}
}
