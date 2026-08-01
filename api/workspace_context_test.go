package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

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
}
