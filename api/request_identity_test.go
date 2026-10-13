package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Tnsor-Labs/brokoli/engine"
	"github.com/Tnsor-Labs/brokoli/models"
	"github.com/Tnsor-Labs/brokoli/store"
	"github.com/golang-jwt/jwt/v5"
)

// RequestIdentityFunc is consulted through the server's real middleware
// chain, so these tests build the server rather than the middleware alone:
// the property that matters is that every permission check and handler
// reads the refined identity, and that is a fact about where the
// middleware sits.
func identityServer(t *testing.T) (*Server, string) {
	t.Helper()
	s, err := store.NewSQLiteStore(filepath.Join(t.TempDir(), "identity.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	users := newTestUserStore(t)
	admin, err := users.CreateUser("identity-admin", "Str0ng!Passw0rd", RoleAdmin)
	if err != nil {
		t.Fatal(err)
	}
	token, err := GenerateToken(admin)
	if err != nil {
		t.Fatal(err)
	}
	srv := NewServer(0, s, engine.NewEngine(s), nil, nil, users, nil, nil)
	t.Cleanup(func() { close(srv.evictDone) })
	prev := RequestIdentityFunc
	t.Cleanup(func() { RequestIdentityFunc = prev })
	return srv, token
}

func serve(srv *Server, method, path, token, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.router.ServeHTTP(rec, req)
	return rec
}

func permissionsOf(t *testing.T, rec *httptest.ResponseRecorder) map[models.Permission]bool {
	t.Helper()
	var perms []models.Permission
	if err := json.Unmarshal(rec.Body.Bytes(), &perms); err != nil {
		t.Fatalf("decode permissions: %v; body=%s", err, rec.Body.String())
	}
	got := map[models.Permission]bool{}
	for _, p := range perms {
		got[p] = true
	}
	return got
}

// The refined role is the one permission checks read: an admin's token,
// refined to viewer, can no longer create a pipeline.
func TestRequestIdentityRefinesWhatPermissionChecksRead(t *testing.T) {
	srv, token := identityServer(t)
	body := `{"name":"refined","nodes":[{"id":"src","type":"source_file","name":"Source","config":{"path":"/tmp/x.csv"}}],"edges":[]}`

	RequestIdentityFunc = nil
	if perms := permissionsOf(t, serve(srv, http.MethodGet, "/api/auth/me/permissions", token, "")); !perms[models.PermPipelinesCreate] {
		t.Fatal("control: the admin token lacks pipelines.create without the hook")
	}

	RequestIdentityFunc = func(_ *http.Request, claims *jwt.MapClaims) (*jwt.MapClaims, int, string) {
		refined := jwt.MapClaims{}
		for k, v := range *claims {
			refined[k] = v
		}
		refined["role"] = "viewer"
		return &refined, 0, ""
	}
	if perms := permissionsOf(t, serve(srv, http.MethodGet, "/api/auth/me/permissions", token, "")); perms[models.PermPipelinesCreate] {
		t.Fatal("permissions still list pipelines.create after the role was refined to viewer")
	}
	if rec := serve(srv, http.MethodPost, "/api/pipelines", token, body); rec.Code != http.StatusForbidden {
		t.Fatalf("create as a refined viewer: status = %d, want 403; body=%s", rec.Code, rec.Body.String())
	}
}

// A refusal stops the request before any handler, with the hook's status
// and message.
func TestRequestIdentityCanRefuse(t *testing.T) {
	srv, token := identityServer(t)
	RequestIdentityFunc = func(*http.Request, *jwt.MapClaims) (*jwt.MapClaims, int, string) {
		return nil, http.StatusForbidden, "not a member of this organization"
	}
	rec := serve(srv, http.MethodGet, "/api/pipelines", token, "")
	if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "not a member of this organization") {
		t.Fatalf("status = %d, body=%s; want 403 with the hook's message", rec.Code, rec.Body.String())
	}
}

// Without an identity there is nothing to refine, and the hook is not
// consulted.
func TestRequestIdentityIgnoresAnonymousRequests(t *testing.T) {
	srv, _ := identityServer(t)
	RequestIdentityFunc = func(*http.Request, *jwt.MapClaims) (*jwt.MapClaims, int, string) {
		t.Fatal("hook consulted for an anonymous request")
		return nil, 0, ""
	}
	if rec := serve(srv, http.MethodGet, "/api/auth/setup", "", ""); rec.Code != http.StatusOK {
		t.Fatalf("anonymous setup check: status = %d; body=%s", rec.Code, rec.Body.String())
	}
}
