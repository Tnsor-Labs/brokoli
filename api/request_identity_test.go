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

	RequestIdentityFunc = func(_ *http.Request, claims *jwt.MapClaims) IdentityRefinement {
		refined := jwt.MapClaims{}
		for k, v := range *claims {
			refined[k] = v
		}
		refined["role"] = "viewer"
		return IdentityRefinement{Claims: &refined}
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
	RequestIdentityFunc = func(*http.Request, *jwt.MapClaims) IdentityRefinement {
		return IdentityRefinement{Status: http.StatusForbidden, Message: "not a member of this organization"}
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
	RequestIdentityFunc = func(*http.Request, *jwt.MapClaims) IdentityRefinement {
		t.Fatal("hook consulted for an anonymous request")
		return IdentityRefinement{}
	}
	if rec := serve(srv, http.MethodGet, "/api/auth/setup", "", ""); rec.Code != http.StatusOK {
		t.Fatalf("anonymous setup check: status = %d; body=%s", rec.Code, rec.Body.String())
	}
}

// Permissions the hook resolves are the request's permissions, whatever the
// role claim says: a role the store does not hold grants exactly what the
// hook resolved, on the permissions endpoint and on every gated route.
func TestRequestIdentityPermissionsReplaceTheRoleLookup(t *testing.T) {
	srv, token := identityServer(t)
	body := `{"name":"resolved","nodes":[{"id":"src","type":"source_file","name":"Source","config":{"path":"/tmp/x.csv"}}],"edges":[]}`
	refineTo := func(role string, perms []models.Permission) {
		RequestIdentityFunc = func(_ *http.Request, claims *jwt.MapClaims) IdentityRefinement {
			refined := jwt.MapClaims{}
			for k, v := range *claims {
				refined[k] = v
			}
			refined["role"] = role
			return IdentityRefinement{Claims: &refined, Permissions: perms}
		}
	}

	// A role the store does not know, resolved by the hook to view and
	// create pipelines and nothing else.
	refineTo("deployer", []models.Permission{models.PermPipelinesView, models.PermPipelinesCreate})
	perms := permissionsOf(t, serve(srv, http.MethodGet, "/api/auth/me/permissions", token, ""))
	if len(perms) != 2 || !perms[models.PermPipelinesCreate] || !perms[models.PermPipelinesView] {
		t.Fatalf("permissions = %v, want exactly pipelines.view and pipelines.create", perms)
	}
	if rec := serve(srv, http.MethodPost, "/api/pipelines", token, body); rec.Code != http.StatusCreated {
		t.Fatalf("create with a resolved pipelines.create: status = %d; body=%s", rec.Code, rec.Body.String())
	}
	conn := `{"conn_id":"resolved","type":"postgres","host":"localhost"}`
	if rec := serve(srv, http.MethodPost, "/api/connections", token, conn); rec.Code != http.StatusForbidden {
		t.Fatalf("create a connection without connections.create: status = %d, want 403; body=%s", rec.Code, rec.Body.String())
	}

	// The resolved list wins over the role claim in both directions: an
	// "admin" claim with nothing resolved grants nothing.
	refineTo("admin", []models.Permission{})
	if perms := permissionsOf(t, serve(srv, http.MethodGet, "/api/auth/me/permissions", token, "")); len(perms) != 0 {
		t.Fatalf("permissions = %v, want none: an empty resolved list grants nothing", perms)
	}
	if rec := serve(srv, http.MethodPost, "/api/pipelines", token, strings.Replace(body, `"resolved"`, `"resolved-2"`, 1)); rec.Code != http.StatusForbidden {
		t.Fatalf("create with nothing resolved: status = %d, want 403; body=%s", rec.Code, rec.Body.String())
	}

	// Control: with no list resolved, the role claim decides as before.
	refineTo("admin", nil)
	if perms := permissionsOf(t, serve(srv, http.MethodGet, "/api/auth/me/permissions", token, "")); !perms[models.PermConnectionsView] {
		t.Fatal("control: an admin claim with nothing resolved lacks connections.view")
	}
}

// PermissionChecker.HasPermission reads resolved permissions too, so an
// application checking through it sees the same answer as the gates.
func TestPermissionCheckerReadsResolvedPermissions(t *testing.T) {
	s, err := store.NewSQLiteStore(filepath.Join(t.TempDir(), "checker.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	pc := NewPermissionChecker(s)
	var through *http.Request
	h := RequestIdentity(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) { through = r }))
	prev := RequestIdentityFunc
	t.Cleanup(func() { RequestIdentityFunc = prev })

	check := func(role string, resolved []models.Permission, perm models.Permission) bool {
		RequestIdentityFunc = func(*http.Request, *jwt.MapClaims) IdentityRefinement {
			return IdentityRefinement{Permissions: resolved}
		}
		claims := jwt.MapClaims{"sub": "u1", "role": role}
		req := httptest.NewRequest(http.MethodGet, "/api/pipelines", nil)
		h.ServeHTTP(httptest.NewRecorder(), req.WithContext(contextWithClaims(req.Context(), &claims)))
		return pc.HasPermission(through, perm)
	}
	if !check("deployer", []models.Permission{models.PermPipelinesRun}, models.PermPipelinesRun) {
		t.Fatal("a resolved pipelines.run is not honored for a role the store does not hold")
	}
	if check("admin", []models.Permission{models.PermPipelinesRun}, models.PermPipelinesDelete) {
		t.Fatal("an admin claim granted pipelines.delete although the resolved list leaves it out")
	}
	if !check("admin", nil, models.PermPipelinesDelete) {
		t.Fatal("control: an admin claim with nothing resolved lacks pipelines.delete")
	}
}
