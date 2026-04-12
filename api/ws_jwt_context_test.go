package api

import (
	"database/sql"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	_ "modernc.org/sqlite"
)

func newTestUserStore(t *testing.T) *UserStore {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "auth.db")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	us, err := NewUserStore(db)
	if err != nil {
		t.Fatalf("NewUserStore: %v", err)
	}
	return us
}

// signTestToken builds a JWT with explicit claims (including org_id) so the
// test doesn't depend on OrgResolverFunc or any enterprise wiring.
func signTestToken(t *testing.T, sub, username, role, orgID string) string {
	t.Helper()
	InitJWTSecret()
	claims := jwt.MapClaims{
		"sub":      sub,
		"username": username,
		"role":     role,
		"exp":      time.Now().Add(1 * time.Hour).Unix(),
	}
	if orgID != "" {
		claims["org_id"] = orgID
	}
	tok, err := SignToken(claims)
	if err != nil {
		t.Fatalf("SignToken: %v", err)
	}
	return tok
}

// TestJWTAuthWebSocket_PropagatesClaimsToContext is a regression test for a
// bug where JWTAuth's WebSocket branch validated the token but did not set
// claims on the request context. Downstream WebSocket handlers (notably
// sodp.Server.HandleWS) read claims from the context to enforce per-session
// tenant isolation. With the bug, every authenticated WebSocket session was
// treated as the "default" org, collapsing multi-tenant separation.
//
// This test wraps a fake "downstream" handler with JWTAuth, sends a fake
// WebSocket upgrade request with a valid token, and asserts that claims +
// org_id are visible on the context the downstream handler receives.
func TestJWTAuthWebSocket_PropagatesClaimsToContext(t *testing.T) {
	us := newTestUserStore(t)

	// Create at least one user so JWTAuth doesn't fall into open-mode bypass.
	if _, err := us.CreateUser("alice", "TestPass123", RoleAdmin); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}

	// Mint a token with an explicit org_id (simulates a multi-tenant claim).
	token := signTestToken(t, "user-alice", "alice", "admin", "acme-corp")

	// Downstream handler captures the request context for assertion.
	var capturedClaims *jwt.MapClaims
	var capturedOrgID string
	var capturedReached bool
	downstream := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedReached = true
		if v := r.Context().Value("claims"); v != nil {
			if mc, ok := v.(*jwt.MapClaims); ok {
				capturedClaims = mc
			}
		}
		if v := r.Context().Value(OrgIDContextKey{}); v != nil {
			capturedOrgID, _ = v.(string)
		}
		w.WriteHeader(http.StatusSwitchingProtocols)
	})

	// Wrap downstream in the real JWTAuth middleware.
	handler := JWTAuth(us)(downstream)

	// Fake a WebSocket upgrade request to /api/ws with the JWT in the query
	// string (matches how browsers + the smoke test pass the token).
	req := httptest.NewRequest("GET", "/api/ws?token="+token, nil)
	req.Header.Set("Upgrade", "websocket")
	req.Header.Set("Connection", "Upgrade")

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if !capturedReached {
		t.Fatalf("downstream handler not invoked: status=%d body=%s", rec.Code, rec.Body.String())
	}
	if capturedClaims == nil {
		t.Error("downstream handler should see claims on the request context — got nil")
	} else {
		if sub, _ := (*capturedClaims)["sub"].(string); sub != "user-alice" {
			t.Errorf("claims.sub: got %q, want user-alice", sub)
		}
		if org, _ := (*capturedClaims)["org_id"].(string); org != "acme-corp" {
			t.Errorf("claims.org_id: got %q, want acme-corp", org)
		}
	}
	if capturedOrgID != "acme-corp" {
		t.Errorf("OrgIDContextKey value: got %q, want acme-corp", capturedOrgID)
	}
}

// TestJWTAuthWebSocket_RejectsMissingToken makes sure the upgrade still
// requires a token — the fix above must not loosen authentication.
func TestJWTAuthWebSocket_RejectsMissingToken(t *testing.T) {
	us := newTestUserStore(t)
	if _, err := us.CreateUser("bob", "TestPass123", RoleAdmin); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	InitJWTSecret()

	reached := false
	downstream := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { reached = true })
	handler := JWTAuth(us)(downstream)

	req := httptest.NewRequest("GET", "/api/ws", nil)
	req.Header.Set("Upgrade", "websocket")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if reached {
		t.Error("downstream should not be reached without a token")
	}
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status: got %d, want 401", rec.Code)
	}
}
