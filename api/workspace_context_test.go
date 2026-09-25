package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Tnsor-Labs/brokoli/models"
	"github.com/golang-jwt/jwt/v5"
)

func TestWorkspaceMiddlewareFailsClosedInMultiTenantMode(t *testing.T) {
	previous := UserWorkspaceResolverFunc
	UserWorkspaceResolverFunc = func(userID string) []string {
		if userID == "user-1" {
			return []string{"owned-workspace"}
		}
		return nil
	}
	t.Cleanup(func() { UserWorkspaceResolverFunc = previous })

	handler := WorkspaceMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Resolved-Workspace", GetWorkspaceID(r))
		w.WriteHeader(http.StatusNoContent)
	}))
	claims := jwt.MapClaims{"sub": "user-1"}

	defaultReq := httptest.NewRequest(http.MethodGet, "/api/pipelines", nil)
	defaultReq = defaultReq.WithContext(context.WithValue(defaultReq.Context(), "claims", &claims))
	defaultRec := httptest.NewRecorder()
	handler.ServeHTTP(defaultRec, defaultReq)
	if defaultRec.Code != http.StatusNoContent || defaultRec.Header().Get("Resolved-Workspace") != "owned-workspace" {
		t.Fatalf("default workspace resolved status=%d workspace=%q", defaultRec.Code, defaultRec.Header().Get("Resolved-Workspace"))
	}

	foreignReq := httptest.NewRequest(http.MethodGet, "/api/pipelines", nil)
	foreignReq.Header.Set("X-Workspace-ID", "foreign-workspace")
	foreignReq = foreignReq.WithContext(context.WithValue(foreignReq.Context(), "claims", &claims))
	foreignRec := httptest.NewRecorder()
	handler.ServeHTTP(foreignRec, foreignReq)
	if foreignRec.Code != http.StatusForbidden {
		t.Fatalf("foreign workspace status = %d, want 403", foreignRec.Code)
	}

	unauthReq := httptest.NewRequest(http.MethodGet, "/api/pipelines", nil)
	unauthRec := httptest.NewRecorder()
	handler.ServeHTTP(unauthRec, unauthReq)
	if unauthRec.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated tenant route status = %d, want 401", unauthRec.Code)
	}

	for _, path := range []string{"/health", "/api/auth/login"} {
		publicReq := httptest.NewRequest(http.MethodGet, path, nil)
		publicRec := httptest.NewRecorder()
		handler.ServeHTTP(publicRec, publicReq)
		if publicRec.Code != http.StatusNoContent {
			t.Fatalf("public route %s status = %d, want 204", path, publicRec.Code)
		}
	}
}

// The counterpart to the multi-tenant contract above: with no tenant
// resolver installed there is no registry to check a workspace id against,
// so honouring one cannot scope a request -- it can only file it somewhere
// unreachable.
//
// This is the shape of #231 as it was actually hit: a browser held a
// workspace id from a different instance, every list came back an empty
// 200, and the product read as "you have no pipelines" while the run panel
// -- scoped by org rather than workspace -- listed runs of the pipelines it
// would not show. An empty 200 is the part that made it hard to see: a 403
// at least says something is wrong.
func TestWorkspaceHeaderIsIgnoredWithoutATenantResolver(t *testing.T) {
	previous := UserWorkspaceResolverFunc
	UserWorkspaceResolverFunc = nil
	t.Cleanup(func() { UserWorkspaceResolverFunc = previous })

	handler := WorkspaceMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Resolved-Workspace", GetWorkspaceID(r))
		w.WriteHeader(http.StatusNoContent)
	}))

	// A stale id, an absent header and an explicit default must all resolve
	// to the same scope, because in this build they name the same thing.
	for _, header := range []string{"", "default", "ws-from-another-instance", "01a03f9f"} {
		req := httptest.NewRequest(http.MethodGet, "/api/pipelines", nil)
		if header != "" {
			req.Header.Set("X-Workspace-ID", header)
		}
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)

		if rec.Code != http.StatusNoContent {
			t.Errorf("X-Workspace-ID %q: status = %d, want 204", header, rec.Code)
		}
		if got := rec.Header().Get("Resolved-Workspace"); got != models.DefaultWorkspaceID {
			t.Errorf("X-Workspace-ID %q resolved to %q, want %q",
				header, got, models.DefaultWorkspaceID)
		}
	}
}

// Ignoring the header must not reach the multi-tenant path: there a
// workspace id is checkable, and silently rewriting it to the default would
// turn a refusal into a cross-tenant read. The test above and this one pin
// the two halves against each other.
func TestIgnoringTheHeaderDoesNotWeakenMultiTenantRefusal(t *testing.T) {
	previous := UserWorkspaceResolverFunc
	UserWorkspaceResolverFunc = func(string) []string { return []string{"owned"} }
	t.Cleanup(func() { UserWorkspaceResolverFunc = previous })

	handler := WorkspaceMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Resolved-Workspace", GetWorkspaceID(r))
		w.WriteHeader(http.StatusNoContent)
	}))
	claims := jwt.MapClaims{"sub": "user-1"}

	req := httptest.NewRequest(http.MethodGet, "/api/pipelines", nil)
	req.Header.Set("X-Workspace-ID", "someone-elses")
	req = req.WithContext(context.WithValue(req.Context(), "claims", &claims))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403; a checkable id must still be refused", rec.Code)
	}
}

// A worker fetching a staged task input holds no session and belongs to
// no user, so there is no workspace to resolve for it. The middleware
// used to answer 401 before blobAuth could resolve its identity, which
// made every reference-based task input fail on a multi-tenant fleet
// with "authentication required".
//
// Only reachable with a workspace resolver installed. Without one the
// rejecting branch is skipped, which is why every single-tenant test
// passed while a real deployment could not fetch a single blob.
func TestWorkspaceMiddlewareLetsTheDataPlaneThrough(t *testing.T) {
	previous := UserWorkspaceResolverFunc
	UserWorkspaceResolverFunc = func(string) []string { return []string{"owned-workspace"} }
	t.Cleanup(func() { UserWorkspaceResolverFunc = previous })

	reached := false
	handler := WorkspaceMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reached = true
		w.WriteHeader(http.StatusNoContent)
	}))

	const blob = "/api/runs/run-1/nodes/t_py/attempts/0/blobs/sha256:" +
		"d7e4d755ebc3af1c5b26c9a5b9b1f143a55837cc9e44784452fe35dcf42d2032"
	for _, m := range []string{http.MethodGet, http.MethodPut} {
		reached = false
		rec := httptest.NewRecorder()
		// No claims: exactly what a worker sends.
		handler.ServeHTTP(rec, httptest.NewRequest(m, blob, nil))
		if rec.Code != http.StatusNoContent || !reached {
			t.Errorf("%s blob: status = %d, reached handler = %v; want the data plane to pass through",
				m, rec.Code, reached)
		}
	}

	// The collection POST, whose object id the server assigns.
	reached = false
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodPost,
		"/api/runs/run-1/nodes/t_py/attempts/0/blobs", nil))
	if rec.Code != http.StatusNoContent || !reached {
		t.Errorf("POST blobs: status = %d, reached = %v", rec.Code, reached)
	}
}

// The exemption is for the data plane's own shape, not for anything
// containing the word. An ordinary API route with no identity must
// still be refused, or this would be a hole rather than a fix.
func TestWorkspaceExemptionDoesNotGeneralise(t *testing.T) {
	previous := UserWorkspaceResolverFunc
	UserWorkspaceResolverFunc = func(string) []string { return []string{"owned-workspace"} }
	t.Cleanup(func() { UserWorkspaceResolverFunc = previous })

	handler := WorkspaceMiddleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))

	for _, path := range []string{
		"/api/pipelines",
		"/api/pipelines/blobs",                      // the word, wrong shape
		"/api/runs/r/nodes/n/attempts/0/blobs/a/b",  // one segment too many
		"/api/blobs/sha256:aa",                      // wrong depth
		"/api/runs/r/nodes/n/attempts/0/notblobs/x", // near miss
	} {
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("%s: status = %d, want 401; only the data-plane shape is exempt", path, rec.Code)
		}
	}
}
