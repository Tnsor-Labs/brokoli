package api

import (
	"database/sql"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/golang-jwt/jwt/v5"
	_ "modernc.org/sqlite"
)

// inviteAuthStore is a real UserStore, because JWTAuth dereferences it
// before it reaches the invite branch.
func inviteAuthStore(t *testing.T) *UserStore {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "u.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	us, err := NewUserStore(db)
	if err != nil {
		t.Fatal(err)
	}
	// A real account, so the middleware's user-count probe succeeds and
	// the install does not read as unconfigured.
	if _, err := us.CreateUser("someone", "Passw0rd-test", RoleAdmin); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	return us
}

// The invite routes must serve two different callers: someone with no
// account yet, and someone already signed in who is joining a second
// organization. The middleware skipped them entirely, which answers
// "always anonymous" -- so the accept handler could not tell who a
// signed-in person was and returned 401 even with a valid session.
//
// Anyone who signs in with GitHub, Google or Keycloak has no password to
// fall back on, so for them accepting an invite was impossible.
//
// These go through the real JWTAuth chain. The pre-existing handler test
// injected claims directly into the context, which is why it passed
// against the broken middleware.

// sawClaims reports what the middleware handed the next handler.
func sawClaims(t *testing.T, req *http.Request) (*jwt.MapClaims, int) {
	t.Helper()
	us := inviteAuthStore(t)
	var got *jwt.MapClaims
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if c, ok := r.Context().Value("claims").(*jwt.MapClaims); ok {
			got = c
		}
		w.WriteHeader(http.StatusOK)
	})
	rec := httptest.NewRecorder()
	JWTAuth(us)(next).ServeHTTP(rec, req)
	return got, rec.Code
}

func TestInviteAcceptSeesASignedInUser(t *testing.T) {
	token, err := GenerateToken(&User{ID: "u-1", Username: "invitee", Role: RoleViewer})
	if err != nil {
		t.Fatalf("GenerateToken: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/invites/tok-123/accept", nil)
	req.Header.Set("Authorization", "Bearer "+token)

	claims, status := sawClaims(t, req)
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200", status)
	}
	if claims == nil {
		t.Fatal("the handler saw no claims, so it cannot tell who is accepting")
	}
	if sub, _ := (*claims)["sub"].(string); sub != "u-1" {
		t.Errorf("sub = %q, want the signed-in user", sub)
	}
}

// A session cookie must work too: that is how a browser arrives.
func TestInviteAcceptSeesACookieSession(t *testing.T) {
	token, _ := GenerateToken(&User{ID: "u-2", Username: "cookie-user", Role: RoleViewer})
	req := httptest.NewRequest(http.MethodPost, "/api/invites/tok-123/accept", nil)
	req.AddCookie(&http.Cookie{Name: "brokoli_session", Value: token})

	claims, status := sawClaims(t, req)
	if status != http.StatusOK || claims == nil {
		t.Fatalf("cookie session did not reach the handler (status %d)", status)
	}
	if sub, _ := (*claims)["sub"].(string); sub != "u-2" {
		t.Errorf("sub = %q", sub)
	}
}

// And the anonymous read must keep working, or nobody without an account
// can look at the invite they were sent.
func TestInviteStaysReadableAnonymously(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/api/invites/tok-123", nil)

	claims, status := sawClaims(t, req)
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200 for an anonymous read", status)
	}
	if claims != nil {
		t.Error("claims appeared for a request that carried no token")
	}
}

// A stale token is treated as absent, not rejected: someone whose session
// expired in another tab must still be able to read the invite and sign
// in from it.
func TestAnExpiredTokenDoesNotBlockTheInvitePage(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/api/invites/tok-123", nil)
	req.Header.Set("Authorization", "Bearer not-a-real-token")

	claims, status := sawClaims(t, req)
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200; an invalid token must read as anonymous", status)
	}
	if claims != nil {
		t.Error("an unparseable token produced claims")
	}
}
