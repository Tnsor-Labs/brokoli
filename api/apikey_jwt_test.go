package api

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/golang-jwt/jwt/v5"
)

// A static API key and JWTAuth are two independent, stacked middlewares
// (see server.go: r.Use(APIKeyAuth(auth)); r.Use(JWTAuth(userStore))).
// A validated static key must be a complete identity on its own -- these
// tests pin that down at the exact seam where it previously wasn't:
// APIKeyAuth accepted the key but never told JWTAuth, which then still
// demanded a JWT a static key was never going to produce.
func newAPIKeyChain(t *testing.T, key string, us *UserStore) http.Handler {
	t.Helper()
	auth := NewAuthConfig()
	auth.AddKey(key, "test key")

	var sawRole string
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if claims, ok := r.Context().Value("claims").(*jwt.MapClaims); ok && claims != nil {
			sawRole, _ = (*claims)["role"].(string)
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(sawRole))
	})

	return APIKeyAuth(auth)(JWTAuth(us)(handler))
}

func TestAPIKeyAuth_SatisfiesJWTAuth_ZeroUsers(t *testing.T) {
	us := newTestUserStore(t)
	chain := newAPIKeyChain(t, "brk_test123", us)

	req := httptest.NewRequest(http.MethodGet, "/api/pipelines", nil)
	req.Header.Set("Authorization", "Bearer brk_test123")
	rec := httptest.NewRecorder()
	chain.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 with a valid static key before any user exists, got %d: %s", rec.Code, rec.Body)
	}
	if rec.Body.String() != string(RoleAdmin) {
		t.Fatalf("expected claims propagated with role %q, got %q", RoleAdmin, rec.Body.String())
	}
}

func TestAPIKeyAuth_SatisfiesJWTAuth_WithUsers(t *testing.T) {
	us := newTestUserStore(t)
	if _, err := us.CreateUser("admin", "Correct-Horse-Battery-9", RoleAdmin); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	chain := newAPIKeyChain(t, "brk_test123", us)

	req := httptest.NewRequest(http.MethodGet, "/api/pipelines", nil)
	req.Header.Set("Authorization", "Bearer brk_test123")
	rec := httptest.NewRecorder()
	chain.ServeHTTP(rec, req)

	// This is the regression this test file exists for: once a user
	// account exists (true of virtually every real deployment), JWTAuth
	// used to fall through to ParseToken() on the same Bearer value and
	// reject it with "invalid token" -- a static key could authenticate
	// nothing beyond /api/auth/* bootstrap routes.
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 with a valid static key once a user exists, got %d: %s", rec.Code, rec.Body)
	}
}

func TestAPIKeyAuth_WrongKey_StillRejectedByJWTAuth(t *testing.T) {
	us := newTestUserStore(t)
	if _, err := us.CreateUser("admin", "Correct-Horse-Battery-9", RoleAdmin); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	chain := newAPIKeyChain(t, "brk_test123", us)

	req := httptest.NewRequest(http.MethodGet, "/api/pipelines", nil)
	req.Header.Set("Authorization", "Bearer brk_not_the_right_key")
	rec := httptest.NewRecorder()
	chain.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403 for a wrong static key, got %d: %s", rec.Code, rec.Body)
	}
}

func TestAPIKeyAuth_NoAuth_StillRejected(t *testing.T) {
	us := newTestUserStore(t)
	chain := newAPIKeyChain(t, "brk_test123", us)

	req := httptest.NewRequest(http.MethodGet, "/api/pipelines", nil)
	rec := httptest.NewRecorder()
	chain.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 with no credentials at all, got %d: %s", rec.Code, rec.Body)
	}
}
