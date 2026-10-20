package api

import (
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/Tnsor-Labs/brokoli/engine"
	"github.com/Tnsor-Labs/brokoli/models"
	"github.com/Tnsor-Labs/brokoli/store"
)

// Agent identities (ADR-046 phase 2), through the real server.

type agentBed struct {
	t      *testing.T
	srv    *Server
	users  *UserStore
	s      store.Store
	tokens map[string]string // person -> session token
	ids    map[string]string // person -> user id
}

func newAgentBed(t *testing.T) *agentBed {
	t.Helper()
	s, err := store.NewSQLiteStore(filepath.Join(t.TempDir(), "agents.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	b := &agentBed{t: t, s: s, users: newTestUserStore(t), tokens: map[string]string{}, ids: map[string]string{}}
	for name, role := range map[string]Role{"ada": RoleAdmin, "vic": RoleViewer, "eve": RoleEditor} {
		u, err := b.users.CreateUser(name, "Str0ng!Passw0rd", role)
		if err != nil {
			t.Fatal(err)
		}
		b.ids[name] = u.ID
		if b.tokens[name], err = GenerateToken(u); err != nil {
			t.Fatal(err)
		}
	}
	b.srv = NewServer(0, s, engine.NewEngine(s), nil, nil, b.users, nil, nil)
	t.Cleanup(func() { close(b.srv.evictDone) })
	return b
}

func (b *agentBed) do(method, path, token, body string) (int, map[string]any, string) {
	b.t.Helper()
	rec := serve(b.srv, method, path, token, body)
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec.Code, out, rec.Body.String()
}

// agent creates an agent as ada and issues it a token as issuer.
func (b *agentBed) agent(name, role, owner, issuer string, behalf bool) (id, token string) {
	b.t.Helper()
	body := `{"name":"` + name + `","role":"` + role + `","owner_id":"` + b.ids[owner] + `"}`
	code, a, raw := b.do(http.MethodPost, "/api/agents", b.tokens["ada"], body)
	if code != http.StatusCreated {
		b.t.Fatalf("create %s: %d %s", name, code, raw)
	}
	id = a["id"].(string)
	behalfJSON := "false"
	if behalf {
		behalfJSON = "true"
	}
	code, res, raw := b.do(http.MethodPost, "/api/agents/"+id+"/tokens", b.tokens[issuer], `{"name":"laptop","on_behalf_of_owner":`+behalfJSON+`}`)
	if code != http.StatusCreated {
		b.t.Fatalf("token for %s: %d %s", name, code, raw)
	}
	return id, res["token"].(string)
}

func (b *agentBed) permissions(token string) []string {
	b.t.Helper()
	rec := serve(b.srv, http.MethodGet, "/api/auth/me/permissions", token, "")
	if rec.Code != http.StatusOK {
		return nil
	}
	var perms []string
	_ = json.Unmarshal(rec.Body.Bytes(), &perms)
	sort.Strings(perms)
	return perms
}

func TestAnAgentActsWithItsOwnTokenAndRole(t *testing.T) {
	b := newAgentBed(t)
	id, token := b.agent("triage-bot", "", "ada", "ada", true)
	if !strings.HasPrefix(token, AgentTokenPrefix) {
		t.Fatalf("token = %q", token)
	}

	// Who it is.
	code, me, raw := b.do(http.MethodGet, "/api/auth/me", token, "")
	if code != http.StatusOK || me["sub"] != id || me["username"] != "triage-bot" || me["kind"] != "agent" || me["on_behalf_of"] != "ada" {
		t.Fatalf("me = %d %s", code, raw)
	}
	// The agent role, within its owner's.
	if got := strings.Join(b.permissions(token), " "); got != "pipelines.propose pipelines.run pipelines.view runs.view" {
		t.Fatalf("permissions = %s", got)
	}

	// The token is shown once: listed by its prefix only, with its last use.
	_, _, list := b.do(http.MethodGet, "/api/agents/"+id+"/tokens", b.tokens["ada"], "")
	if strings.Contains(list, token) || !strings.Contains(list, token[:10]) || !strings.Contains(list, `"last_used_at"`) {
		t.Fatalf("tokens = %s", list)
	}

	// A run it starts is the agent's, for its owner.
	p := &models.Pipeline{ID: "p1", Name: "p1", Enabled: true, WorkspaceID: models.DefaultWorkspaceID,
		Nodes: []models.Node{{ID: "n", Type: "source_api", Name: "N", Config: map[string]interface{}{"url": "https://example.invalid/x"}}}, Edges: []models.Edge{}}
	if err := b.s.CreatePipeline(p); err != nil {
		t.Fatal(err)
	}
	code, run, raw := b.do(http.MethodPost, "/api/pipelines/p1/run", token, `{}`)
	if code != http.StatusOK && code != http.StatusAccepted && code != http.StatusCreated {
		t.Fatalf("run: %d %s", code, raw)
	}
	// Who started it, as the run is served.
	var tb *models.RunAttribution
	for deadline := time.Now().Add(10 * time.Second); tb == nil && time.Now().Before(deadline); time.Sleep(50 * time.Millisecond) {
		rec := serve(b.srv, http.MethodGet, "/api/runs/"+run["id"].(string), token, "")
		var got models.Run
		if rec.Code == http.StatusOK && json.Unmarshal(rec.Body.Bytes(), &got) == nil {
			tb = got.TriggeredBy
		}
	}
	if tb == nil || tb.Kind != models.RunTriggerKindAgent || tb.UserID != id ||
		tb.UserName != "triage-bot" || tb.OnBehalfOf != "ada" || tb.TokenName != "laptop" {
		t.Fatalf("triggered by %+v", tb)
	}

	// An agent cannot sign in, manage agents, or edit what its role lacks.
	if code, _, _ := b.do(http.MethodPost, "/api/auth/login", "", `{"username":"triage-bot","password":"!"}`); code != http.StatusUnauthorized {
		t.Fatalf("an agent signed in: %d", code)
	}
	for _, c := range []struct{ method, path, body string }{
		{http.MethodGet, "/api/agents", ""},
		{http.MethodPost, "/api/agents/" + id + "/tokens", `{"name":"more"}`},
		{http.MethodPut, "/api/pipelines/p1", `{"name":"x","nodes":[],"edges":[]}`},
	} {
		if code, _, raw := b.do(c.method, c.path, token, c.body); code != http.StatusForbidden {
			t.Errorf("%s %s by the agent: %d %s", c.method, c.path, code, raw)
		}
	}
	// It is not a person on the users list.
	if _, _, raw := b.do(http.MethodGet, "/api/auth/users", b.tokens["ada"], ""); strings.Contains(raw, "triage-bot") {
		t.Fatalf("users = %s", raw)
	}
}

// An agent never holds more than its owner, nor secrets or user management
// whatever its role, and narrows when its owner does.
func TestAnAgentIsNeverAboveItsOwner(t *testing.T) {
	b := newAgentBed(t)
	_, editorOfViewer := b.agent("helper", "editor", "vic", "ada", false)
	if got, viewer := b.permissions(editorOfViewer), b.permissions(b.tokens["vic"]); strings.Join(got, " ") != strings.Join(intersect(viewer, b.editorPerms()), " ") {
		t.Fatalf("editor owned by a viewer: %v\nviewer: %v", got, viewer)
	}

	_, operator := b.agent("ops", "operator", "ada", "ada", false)
	for _, p := range b.permissions(operator) {
		if p == string(models.PermSettingsManageUsers) || p == string(models.PermVariablesViewSecret) {
			t.Fatalf("an agent holds %s", p)
		}
	}
	// A custom role with everything still withholds what no agent holds.
	all := models.AllPermissions()
	if err := b.s.CreateRole(&models.Role{ID: "everything", Name: "everything", Permissions: all}); err != nil {
		t.Fatal(err)
	}
	_, everything := b.agent("big", "everything", "ada", "ada", false)
	got := strings.Join(b.permissions(everything), " ")
	for p := range agentNeverPermissions {
		if strings.Contains(got, string(p)) {
			t.Fatalf("an agent with every permission holds %s", p)
		}
	}

	// The owner demoted: the agent narrows at once, with the same token.
	_, opsOfEve := b.agent("eves-bot", "operator", "eve", "ada", false)
	before := b.permissions(opsOfEve)
	if _, err := b.users.db.Exec(b.users.q(`UPDATE users SET role = 'viewer' WHERE id = ?`), b.ids["eve"]); err != nil {
		t.Fatal(err)
	}
	after := b.permissions(opsOfEve)
	if len(after) >= len(before) || strings.Contains(strings.Join(after, " "), "pipelines.run") {
		t.Fatalf("before %v after %v", before, after)
	}
	// The owner gone: the agent stops.
	if _, err := b.users.db.Exec(b.users.q(`DELETE FROM users WHERE id = ?`), b.ids["eve"]); err != nil {
		t.Fatal(err)
	}
	if code, _, _ := b.do(http.MethodGet, "/api/auth/me", opsOfEve, ""); code != http.StatusUnauthorized {
		t.Fatalf("an agent whose owner is gone: %d", code)
	}
}

func (b *agentBed) editorPerms() []string {
	r, err := b.s.GetRole("editor")
	if err != nil {
		b.t.Fatal(err)
	}
	out := []string{}
	for _, p := range r.Permissions {
		out = append(out, string(p))
	}
	return out
}

func intersect(a, b []string) []string {
	in := map[string]bool{}
	for _, x := range b {
		in[x] = true
	}
	out := []string{}
	for _, x := range a {
		if in[x] && !agentNeverPermissions[models.Permission(x)] {
			out = append(out, x)
		}
	}
	sort.Strings(out)
	return out
}

func TestAgentTokensExpireAndAreRevokedOneByOne(t *testing.T) {
	b := newAgentBed(t)
	id, first := b.agent("bot", "", "ada", "ada", false)
	_, res, _ := b.do(http.MethodPost, "/api/agents/"+id+"/tokens", b.tokens["ada"], `{"name":"ci"}`)
	second := res["token"].(string)
	secondID := res["record"].(map[string]any)["id"].(string)

	if code, _, _ := b.do(http.MethodDelete, "/api/agents/"+id+"/tokens/"+secondID, b.tokens["ada"], ""); code != http.StatusNoContent {
		t.Fatalf("revoke: %d", code)
	}
	if code, _, _ := b.do(http.MethodGet, "/api/auth/me", second, ""); code != http.StatusUnauthorized {
		t.Fatalf("a revoked token: %d", code)
	}
	if code, _, _ := b.do(http.MethodGet, "/api/auth/me", first, ""); code != http.StatusOK {
		t.Fatalf("the other token stopped too: %d", code)
	}

	// Expired.
	if _, err := b.users.db.Exec(b.users.q(`UPDATE agent_tokens SET expires_at = ? WHERE token_hash = ?`),
		time.Now().Add(-time.Minute).UTC().Format(time.RFC3339), hashAgentToken(first)); err != nil {
		t.Fatal(err)
	}
	if code, _, _ := b.do(http.MethodGet, "/api/auth/me", first, ""); code != http.StatusUnauthorized {
		t.Fatalf("an expired token: %d", code)
	}

	// Disabling the agent revokes everything and refuses new tokens.
	_, third := b.agent("bot2", "", "ada", "ada", false)
	id2 := b.agentID("bot2")
	if code, _, _ := b.do(http.MethodDelete, "/api/agents/"+id2, b.tokens["ada"], ""); code != http.StatusNoContent {
		t.Fatalf("disable: %d", code)
	}
	if code, _, _ := b.do(http.MethodGet, "/api/auth/me", third, ""); code != http.StatusUnauthorized {
		t.Fatalf("a disabled agent's token: %d", code)
	}
	if code, _, _ := b.do(http.MethodPost, "/api/agents/"+id2+"/tokens", b.tokens["ada"], `{"name":"x"}`); code != http.StatusConflict {
		t.Fatalf("a token for a disabled agent: %d", code)
	}
	// Disabled is enough on its own, revoked or not.
	_, fourth := b.agent("bot3", "", "ada", "ada", false)
	if _, err := b.users.db.Exec(b.users.q(`UPDATE users SET disabled_at = ? WHERE id = ?`), time.Now().UTC().Format(time.RFC3339), b.agentID("bot3")); err != nil {
		t.Fatal(err)
	}
	if code, _, _ := b.do(http.MethodGet, "/api/auth/me", fourth, ""); code != http.StatusUnauthorized {
		t.Fatalf("a disabled agent's unrevoked token: %d", code)
	}
	// A made-up token is nothing.
	if code, _, _ := b.do(http.MethodGet, "/api/auth/me", AgentTokenPrefix+strings.Repeat("0", 64), ""); code != http.StatusUnauthorized {
		t.Fatalf("an unknown token: %d", code)
	}
	// Lifetimes are bounded.
	if code, _, _ := b.do(http.MethodPost, "/api/agents/"+id+"/tokens", b.tokens["ada"], `{"name":"long","expires_in_days":400}`); code != http.StatusBadRequest {
		t.Fatalf("a 400-day token: %d", code)
	}
}

func (b *agentBed) agentID(name string) string {
	agents, err := b.users.ListAgents("")
	if err != nil {
		b.t.Fatal(err)
	}
	for _, a := range agents {
		if a.Name == name {
			return a.ID
		}
	}
	b.t.Fatalf("no agent %s", name)
	return ""
}

func TestWhoManagesWhichAgents(t *testing.T) {
	b := newAgentBed(t)
	// Only someone who manages users creates agents, and never an admin one.
	if code, _, _ := b.do(http.MethodPost, "/api/agents", b.tokens["vic"], `{"name":"mine"}`); code != http.StatusForbidden {
		t.Fatalf("a viewer created an agent: %d", code)
	}
	for body, want := range map[string]int{
		`{"name":"boss","role":"admin"}`:        http.StatusBadRequest,
		`{"name":"boss","role":"superadmin"}`:   http.StatusBadRequest,
		`{"name":"x","role":"nope"}`:            http.StatusBadRequest,
		`{"name":"Not A Name"}`:                 http.StatusBadRequest,
		`{"name":"ada"}`:                        http.StatusConflict, // a person's name
		`{"name":"orphan","owner_id":"nobody"}`: http.StatusBadRequest,
	} {
		if code, _, raw := b.do(http.MethodPost, "/api/agents", b.tokens["ada"], body); code != want {
			t.Errorf("%s: %d %s", body, code, raw)
		}
	}

	vicsID, _ := b.agent("vics-bot", "", "vic", "ada", false)
	// An agent answers to a person, not to another agent.
	if code, _, raw := b.do(http.MethodPost, "/api/agents", b.tokens["ada"], `{"name":"sub-bot","owner_id":"`+vicsID+`"}`); code != http.StatusBadRequest {
		t.Fatalf("an agent owning an agent: %d %s", code, raw)
	}
	adasID, _ := b.agent("adas-bot", "", "ada", "ada", false)
	// The owner sees and manages theirs, not anyone else's.
	_, _, raw := b.do(http.MethodGet, "/api/agents", b.tokens["vic"], "")
	if !strings.Contains(raw, "vics-bot") || strings.Contains(raw, "adas-bot") {
		t.Fatalf("vic's list = %s", raw)
	}
	if code, _, _ := b.do(http.MethodGet, "/api/agents/"+adasID+"/tokens", b.tokens["vic"], ""); code != http.StatusNotFound {
		t.Fatalf("vic reached ada's agent: %d", code)
	}
	if code, _, _ := b.do(http.MethodPost, "/api/agents/"+vicsID+"/tokens", b.tokens["vic"], `{"name":"mine","on_behalf_of_owner":true}`); code != http.StatusCreated {
		t.Fatalf("vic issuing a token: %d", code)
	}
	// Only the owner says the agent acts for them; only an admin changes its role.
	if code, _, _ := b.do(http.MethodPost, "/api/agents/"+vicsID+"/tokens", b.tokens["ada"], `{"name":"x","on_behalf_of_owner":true}`); code != http.StatusForbidden {
		t.Fatalf("ada issued a token on vic's behalf: %d", code)
	}
	if code, _, _ := b.do(http.MethodPatch, "/api/agents/"+vicsID, b.tokens["vic"], `{"role":"editor"}`); code != http.StatusForbidden {
		t.Fatalf("vic changed the role: %d", code)
	}
	if code, a, raw := b.do(http.MethodPatch, "/api/agents/"+vicsID, b.tokens["ada"], `{"role":"operator","description":"triage"}`); code != http.StatusOK || a["role"] != "operator" {
		t.Fatalf("ada changed the role: %d %s", code, raw)
	}
	if code, _, _ := b.do(http.MethodPatch, "/api/agents/"+vicsID, b.tokens["ada"], `{"role":"admin"}`); code != http.StatusBadRequest {
		t.Fatalf("an agent made admin: %d", code)
	}
}

// Over MCP, an agent's token is offered its role's tools and is told who it
// acts as.
func TestAnAgentOverMCP(t *testing.T) {
	b := newAgentBed(t)
	_, token := b.agent("triage-bot", "", "ada", "ada", true)
	hs := httptest.NewServer(b.srv.router)
	defer hs.Close()
	cs, err := mcpSession(t, hs.URL, token)
	if err != nil {
		t.Fatal(err)
	}
	defer cs.Close()
	if got := strings.Join(toolNames(t, cs), " "); got != "explain_run get_lineage get_pipeline get_run get_run_logs list_change_requests list_pipelines list_runs propose_change validate_pipeline" {
		t.Fatalf("tools = %s", got)
	}
	init := cs.InitializeResult()
	if init == nil || !strings.HasPrefix(init.Instructions, "You act in Brokoli as agent triage-bot, on behalf of ada.") {
		t.Fatalf("instructions = %+v", init)
	}
}

// The agent store's SQL on both dialects.
func TestAgentStoreOnBothDialects(t *testing.T) {
	run := func(t *testing.T, db *sql.DB) {
		t.Helper()
		us, err := NewUserStore(db)
		if err != nil {
			t.Fatal(err)
		}
		suffix := strings.ToLower(generateID()[:6])
		owner, err := us.CreateUser("owner-"+suffix, "Str0ng!Passw0rd", RoleEditor)
		if err != nil {
			t.Fatal(err)
		}
		a, err := us.CreateAgent("bot-"+suffix, "d", AgentRole, owner.ID)
		if err != nil || a.OwnerName != owner.Username || a.Role != AgentRole || a.Disabled || a.LastUsedAt != nil {
			t.Fatalf("create = %+v, %v", a, err)
		}
		if _, err := us.CreateAgent("bot-"+suffix, "", AgentRole, owner.ID); !errors.Is(err, ErrUserExists) {
			t.Fatalf("a second agent of that name: %v", err)
		}
		if _, err := us.Authenticate("bot-"+suffix, "!"); err == nil {
			t.Fatal("an agent authenticated with a password")
		}
		token, rec, err := us.CreateAgentToken(a.ID, "ci", owner.ID, 0, true)
		if err != nil || rec.ExpiresAt.Sub(rec.CreatedAt) != 90*24*time.Hour {
			t.Fatalf("token: %+v %v", rec, err)
		}
		claims, ok := us.ResolveAgentToken(token)
		if !ok || (*claims)["sub"] != a.ID || (*claims)["owner_role"] != string(RoleEditor) || (*claims)["on_behalf_of"] != owner.Username {
			t.Fatalf("resolve = %v %v", claims, ok)
		}
		if got, _ := us.GetAgent(a.ID); got.LastUsedAt == nil {
			t.Fatal("last use not recorded")
		}
		mine, _ := us.ListAgents(owner.ID)
		if len(mine) != 1 || mine[0].ID != a.ID {
			t.Fatalf("list = %+v", mine)
		}
		people, _ := us.ListUsers()
		for _, p := range people {
			if p.ID == a.ID {
				t.Fatal("the agent is listed as a person")
			}
		}
		if err := us.UpdateAgent(a.ID, "viewer", "new"); err != nil {
			t.Fatal(err)
		}
		if err := us.RevokeAgentToken(a.ID, rec.ID); err != nil {
			t.Fatal(err)
		}
		if err := us.RevokeAgentToken(a.ID, rec.ID); !errors.Is(err, ErrTokenNotFound) {
			t.Fatalf("revoked twice: %v", err)
		}
		if _, ok := us.ResolveAgentToken(token); ok {
			t.Fatal("a revoked token resolved")
		}
		token2, _, _ := us.CreateAgentToken(a.ID, "two", owner.ID, 1, false)
		if err := us.DisableAgent(a.ID); err != nil {
			t.Fatal(err)
		}
		if err := us.DisableAgent(a.ID); err != nil {
			t.Fatalf("disabling twice: %v", err)
		}
		if _, ok := us.ResolveAgentToken(token2); ok {
			t.Fatal("a disabled agent's token resolved")
		}
		tokens, _ := us.ListAgentTokens(a.ID)
		if len(tokens) != 2 || tokens[0].RevokedAt == nil || tokens[1].RevokedAt == nil {
			t.Fatalf("tokens = %+v", tokens)
		}
		if err := us.DisableAgent("nobody"); !errors.Is(err, ErrAgentNotFound) {
			t.Fatalf("disable nobody: %v", err)
		}
	}
	t.Run("sqlite", func(t *testing.T) {
		db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "u.db"))
		if err != nil {
			t.Fatal(err)
		}
		defer db.Close()
		run(t, db)
	})
	t.Run("postgres", func(t *testing.T) {
		uri := os.Getenv("BROKOLI_TEST_POSTGRES_URL")
		if uri == "" {
			t.Skip("BROKOLI_TEST_POSTGRES_URL not set")
		}
		st, err := store.NewPostgresStore(uri)
		if err != nil {
			t.Fatal(err)
		}
		run(t, st.RawDB().(*sql.DB))
	})
}

// A distribution can turn agent identities off: no agents API, and an
// existing agent's token stops working.
func TestAgentIdentitiesCanBeTurnedOff(t *testing.T) {
	b := newAgentBed(t)
	id, token := b.agent("bot", "", "ada", "ada", false)
	AgentIdentitiesEnabled = false
	t.Cleanup(func() { AgentIdentitiesEnabled = true })
	if code, _, _ := b.do(http.MethodGet, "/api/auth/me", token, ""); code != http.StatusUnauthorized {
		t.Fatalf("an agent token with agents off: %d", code)
	}
	for _, path := range []string{"/api/agents", "/api/agents/" + id + "/tokens"} {
		if code, _, _ := b.do(http.MethodGet, path, b.tokens["ada"], ""); code != http.StatusNotFound {
			t.Fatalf("%s with agents off: %d", path, code)
		}
	}
	if code, _, _ := b.do(http.MethodPost, "/api/agents", b.tokens["ada"], `{"name":"new"}`); code != http.StatusNotFound {
		t.Fatalf("creating an agent with agents off: %d", code)
	}
}
