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
