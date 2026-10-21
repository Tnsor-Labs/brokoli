package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// OAuth for MCP clients (ADR-046 phase 5), end to end through the server:
// discovery, registration, approval by a person, the code with PKCE,
// refresh with rotation, revocation.

func (b *agentBed) form(path string, values url.Values) (int, map[string]any) {
	b.t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(values.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	b.srv.router.ServeHTTP(rec, req)
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec.Code, out
}

// approve runs the authorization request as person token, approving, and
// returns the code the client receives.
func (b *agentBed) approve(token string, query url.Values) (int, string) {
	b.t.Helper()
	body, _ := json.Marshal(map[string]any{"query": query.Encode(), "approve": true})
	code, res, raw := b.do(http.MethodPost, "/api/oauth/authorize", token, string(body))
	if code != http.StatusOK {
		return code, raw
	}
	u, _ := url.Parse(res["redirect"].(string))
	if u.Query().Get("state") != query.Get("state") {
		b.t.Fatalf("state not returned: %s", u)
	}
	return code, u.Query().Get("code")
}

func TestAnMCPClientConnectsWithOAuth(t *testing.T) {
	b := newAgentBed(t)
	_, agent := b.agent("triage-bot", "", "ada", "ada", true)

	// Discovery.
	_, meta, raw := b.do(http.MethodGet, "/.well-known/oauth-protected-resource", "", "")
	if meta["resource"] != "http://example.com/api/mcp" || meta["authorization_servers"].([]any)[0] != "http://example.com" {
		t.Fatalf("resource metadata = %s", raw)
	}
	_, as, raw := b.do(http.MethodGet, "/.well-known/oauth-authorization-server", "", "")
	if as["token_endpoint"] != "http://example.com/oauth/token" || as["code_challenge_methods_supported"].([]any)[0] != "S256" {
		t.Fatalf("server metadata = %s", raw)
	}
	rec := serve(b.srv, http.MethodPost, "/api/mcp", "", `{}`)
	if rec.Code != http.StatusUnauthorized || !strings.Contains(rec.Header().Get("WWW-Authenticate"), `resource_metadata="http://example.com/.well-known/oauth-protected-resource"`) {
		t.Fatalf("/api/mcp unauthenticated: %d %q", rec.Code, rec.Header().Get("WWW-Authenticate"))
	}

	// Registration: public clients, https or loopback redirects only.
	if code, _, _ := b.do(http.MethodPost, "/oauth/register", "", `{"client_name":"evil","redirect_uris":["http://evil.example/cb"]}`); code != http.StatusBadRequest {
		t.Fatalf("an http redirect elsewhere was registered: %d", code)
	}
	code, client, raw := b.do(http.MethodPost, "/oauth/register", "", `{"client_name":"Claude","redirect_uris":["http://localhost:3333/cb"]}`)
	if code != http.StatusCreated || !strings.HasPrefix(client["client_id"].(string), "mcp_") {
		t.Fatalf("register: %d %s", code, raw)
	}
	clientID := client["client_id"].(string)
	verifier := "a-verifier-that-is-long-enough-to-be-a-real-pkce-verifier-0123"
	query := url.Values{"response_type": {"code"}, "client_id": {clientID}, "redirect_uri": {"http://localhost:3333/cb"},
		"code_challenge": {pkceS256(verifier)}, "code_challenge_method": {"S256"}, "state": {"xyz"}, "scope": {"mcp"}}

	// /oauth/authorize sends the browser to the approval page; an
	// unregistered redirect is never followed.
	rec = serve(b.srv, http.MethodGet, "/oauth/authorize?"+query.Encode(), "", "")
	if rec.Code != http.StatusFound || !strings.HasPrefix(rec.Header().Get("Location"), "/#/oauth/authorize?") {
		t.Fatalf("authorize: %d %q", rec.Code, rec.Header().Get("Location"))
	}
	bad := url.Values{}
	for k, v := range query {
		bad[k] = v
	}
	bad.Set("redirect_uri", "http://localhost:4444/elsewhere")
	if rec := serve(b.srv, http.MethodGet, "/oauth/authorize?"+bad.Encode(), "", ""); rec.Code != http.StatusBadRequest || rec.Header().Get("Location") != "" {
		t.Fatalf("an unregistered redirect: %d %q", rec.Code, rec.Header().Get("Location"))
	}
	noPKCE := url.Values{}
	for k, v := range query {
		noPKCE[k] = v
	}
	noPKCE.Del("code_challenge")
	if rec := serve(b.srv, http.MethodGet, "/oauth/authorize?"+noPKCE.Encode(), "", ""); rec.Code != http.StatusFound ||
		!strings.Contains(rec.Header().Get("Location"), "error=invalid_request") {
		t.Fatalf("no PKCE: %d %q", rec.Code, rec.Header().Get("Location"))
	}

	// Approval: a person, as JSON; never an agent.
	if code, raw := b.approve(agent, query); code != http.StatusForbidden {
		t.Fatalf("an agent approved: %d %s", code, raw)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/oauth/authorize", strings.NewReader(`{"query":"`+query.Encode()+`","approve":true}`))
	req.Header.Set("Authorization", "Bearer "+b.tokens["ada"])
	req.Header.Set("Content-Type", "text/plain")
	rec = httptest.NewRecorder()
	b.srv.router.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("a non-JSON approval: %d", rec.Code)
	}
	_, info, raw := b.do(http.MethodGet, "/api/oauth/authorize?"+query.Encode(), b.tokens["ada"], "")
	if info["client_name"] != "Claude" || info["redirect_host"] != "localhost:3333" {
		t.Fatalf("request info = %s", raw)
	}

	// The code, with the wrong verifier: refused, and spent.
	_, codeStr := b.approve(b.tokens["ada"], query)
	tokenForm := url.Values{"grant_type": {"authorization_code"}, "code": {codeStr}, "client_id": {clientID},
		"redirect_uri": {"http://localhost:3333/cb"}, "code_verifier": {"wrong"}}
	if code, res := b.form("/oauth/token", tokenForm); code != http.StatusBadRequest || res["error"] != "invalid_grant" {
		t.Fatalf("a wrong verifier: %d %v", code, res)
	}
	tokenForm.Set("code_verifier", verifier)
	if code, _ := b.form("/oauth/token", tokenForm); code != http.StatusBadRequest {
		t.Fatalf("a spent code was redeemed: %d", code)
	}
	// A fresh code with the right verifier.
	_, codeStr = b.approve(b.tokens["ada"], query)
	tokenForm.Set("code", codeStr)
	code, tokens := b.form("/oauth/token", tokenForm)
	if code != http.StatusOK || !strings.HasPrefix(tokens["access_token"].(string), OAuthAccessTokenPrefix) ||
		!strings.HasPrefix(tokens["refresh_token"].(string), OAuthRefreshTokenPrefix) {
		t.Fatalf("token: %d %v", code, tokens)
	}
	access := tokens["access_token"].(string)
	if code, _ := b.form("/oauth/token", tokenForm); code != http.StatusBadRequest {
		t.Fatalf("a code was redeemed twice: %d", code)
	}

	// The access token is ada, over the API and MCP.
	if code, me, raw := b.do(http.MethodGet, "/api/auth/me", access, ""); code != http.StatusOK || me["username"] != "ada" || me["oauth_client_id"] != clientID {
		t.Fatalf("me: %d %s", code, raw)
	}
	hs := httptest.NewServer(b.srv.router)
	defer hs.Close()
	cs, err := mcpSession(t, hs.URL, access)
	if err != nil {
		t.Fatal(err)
	}
	if names := toolNames(t, cs); len(names) == 0 {
		t.Fatal("no tools over OAuth")
	}
	cs.Close()

	// Refresh rotates; the spent refresh token ends the grant.
	refresh := tokens["refresh_token"].(string)
	code, next := b.form("/oauth/token", url.Values{"grant_type": {"refresh_token"}, "refresh_token": {refresh}, "client_id": {clientID}})
	if code != http.StatusOK || next["access_token"] == access || next["refresh_token"] == refresh {
		t.Fatalf("refresh: %d %v", code, next)
	}
	if code, _, _ := b.do(http.MethodGet, "/api/auth/me", access, ""); code != http.StatusUnauthorized {
		t.Fatalf("the replaced access token still works: %d", code)
	}
	newAccess := next["access_token"].(string)
	if code, _, _ := b.do(http.MethodGet, "/api/auth/me", newAccess, ""); code != http.StatusOK {
		t.Fatalf("the new access token: %d", code)
	}
	if code, _ := b.form("/oauth/token", url.Values{"grant_type": {"refresh_token"}, "refresh_token": {refresh}, "client_id": {clientID}}); code != http.StatusBadRequest {
		t.Fatalf("a spent refresh token was accepted: %d", code)
	}
	if code, _, _ := b.do(http.MethodGet, "/api/auth/me", newAccess, ""); code != http.StatusUnauthorized {
		t.Fatalf("replaying a spent refresh token did not end the grant: %d", code)
	}

	// Revocation.
	_, codeStr = b.approve(b.tokens["ada"], query)
	tokenForm.Set("code", codeStr)
	_, tokens = b.form("/oauth/token", tokenForm)
	access = tokens["access_token"].(string)
	if code, _ := b.form("/oauth/revoke", url.Values{"token": {access}}); code != http.StatusOK {
		t.Fatalf("revoke: %d", code)
	}
	if code, _, _ := b.do(http.MethodGet, "/api/auth/me", access, ""); code != http.StatusUnauthorized {
		t.Fatalf("a revoked token still works: %d", code)
	}
}

// connect registers a client and runs the whole flow as person token,
// approving as agentID when it is set; it returns the issued tokens.
func (b *agentBed) connect(name, token, agentID string) (clientID string, tokens map[string]any) {
	b.t.Helper()
	// Registered through the store: the endpoint's rate limit is not
	// what these tests are about.
	client, err := b.users.RegisterOAuthClient(name, []string{"http://localhost:3333/cb"})
	if err != nil {
		b.t.Fatal(err)
	}
	clientID = client.ID
	verifier := "a-verifier-that-is-long-enough-to-be-a-real-pkce-verifier-0123"
	query := url.Values{"response_type": {"code"}, "client_id": {clientID}, "redirect_uri": {"http://localhost:3333/cb"},
		"code_challenge": {pkceS256(verifier)}, "code_challenge_method": {"S256"}, "state": {"s"}}
	body, _ := json.Marshal(map[string]any{"query": query.Encode(), "approve": true, "agent_id": agentID})
	code, res, raw := b.do(http.MethodPost, "/api/oauth/authorize", token, string(body))
	if code != http.StatusOK {
		b.t.Fatalf("approve: %d %s", code, raw)
	}
	u, _ := url.Parse(res["redirect"].(string))
	code, tokens = b.form("/oauth/token", url.Values{"grant_type": {"authorization_code"}, "code": {u.Query().Get("code")},
		"client_id": {clientID}, "redirect_uri": {"http://localhost:3333/cb"}, "code_verifier": {verifier}})
	if code != http.StatusOK {
		b.t.Fatalf("token: %d %v", code, tokens)
	}
	return clientID, tokens
}

func TestAnOAuthConnectionActsAsYourAgent(t *testing.T) {
	b := newAgentBed(t)
	adaBot, _ := b.agent("ada-bot", "viewer", "ada", "ada", false)
	eveBot, _ := b.agent("eve-bot", "", "eve", "ada", false)

	// The approval page offers the person's own enabled agents only.
	_, client, _ := b.do(http.MethodPost, "/oauth/register", "", `{"client_name":"Claude","redirect_uris":["http://localhost:3333/cb"]}`)
	query := url.Values{"response_type": {"code"}, "client_id": {client["client_id"].(string)}, "redirect_uri": {"http://localhost:3333/cb"},
		"code_challenge": {pkceS256("x")}, "code_challenge_method": {"S256"}}
	_, info, raw := b.do(http.MethodGet, "/api/oauth/authorize?"+query.Encode(), b.tokens["ada"], "")
	agents, _ := info["agents"].([]any)
	if len(agents) != 1 || agents[0].(map[string]any)["name"] != "ada-bot" {
		t.Fatalf("ada's identities = %s", raw)
	}

	// Someone else's agent cannot be chosen.
	body, _ := json.Marshal(map[string]any{"query": query.Encode(), "approve": true, "agent_id": eveBot})
	if code, _, raw := b.do(http.MethodPost, "/api/oauth/authorize", b.tokens["ada"], string(body)); code != http.StatusForbidden {
		t.Fatalf("ada connected as eve's agent: %d %s", code, raw)
	}

	// Connected as ada-bot, the token is the agent: its name, its role,
	// answered for by ada.
	clientID, tokens := b.connect("Claude", b.tokens["ada"], adaBot)
	access := tokens["access_token"].(string)
	code, me, raw := b.do(http.MethodGet, "/api/auth/me", access, "")
	if code != http.StatusOK || me["username"] != "ada-bot" {
		t.Fatalf("me as the agent: %d %s", code, raw)
	}
	perms := b.permissions(access)
	for _, p := range perms {
		if p == "pipelines.edit" || p == "settings.manage_users" {
			t.Fatalf("the viewer agent holds %s over OAuth: %v", p, perms)
		}
	}
	if len(perms) == 0 {
		t.Fatal("the agent connection has no permissions at all")
	}
	// An agent cannot manage connections, even through one.
	if code, _, _ := b.do(http.MethodGet, "/api/oauth/connections", access, ""); code != http.StatusForbidden {
		t.Fatalf("the agent listed connections: %d", code)
	}

	// Every request re-checks the agent: handed to someone else, it no
	// longer acts for ada's connection.
	if _, err := b.users.db.Exec(b.users.q(`UPDATE users SET owner_id = ? WHERE id = ?`), b.ids["eve"], adaBot); err != nil {
		t.Fatal(err)
	}
	if code, _, _ := b.do(http.MethodGet, "/api/auth/me", access, ""); code != http.StatusUnauthorized {
		t.Fatalf("the connection still acts as an agent ada no longer owns: %d", code)
	}
	if _, err := b.users.db.Exec(b.users.q(`UPDATE users SET owner_id = ? WHERE id = ?`), b.ids["ada"], adaBot); err != nil {
		t.Fatal(err)
	}
	if code, _, _ := b.do(http.MethodGet, "/api/auth/me", access, ""); code != http.StatusOK {
		t.Fatalf("back with ada: %d", code)
	}

	// Disabling the agent ends the connection and its refresh.
	if code, _, raw := b.do(http.MethodDelete, "/api/agents/"+adaBot, b.tokens["ada"], ""); code != http.StatusOK && code != http.StatusNoContent {
		t.Fatalf("disable: %d %s", code, raw)
	}
	if code, _, _ := b.do(http.MethodGet, "/api/auth/me", access, ""); code != http.StatusUnauthorized {
		t.Fatalf("a disabled agent's connection still works: %d", code)
	}
	if code, _ := b.form("/oauth/token", url.Values{"grant_type": {"refresh_token"}, "refresh_token": {tokens["refresh_token"].(string)},
		"client_id": {clientID}}); code == http.StatusOK {
		t.Fatal("a disabled agent's connection refreshed")
	}
}

func TestConnectedAppsAreListedAndDisconnected(t *testing.T) {
	b := newAgentBed(t)
	bot, _ := b.agent("ada-bot", "", "ada", "ada", false)
	_, mine := b.connect("Claude", b.tokens["ada"], "")
	_, asBot := b.connect("Cursor", b.tokens["ada"], bot)
	b.connect("Other", b.tokens["eve"], "")

	code, res, raw := b.do(http.MethodGet, "/api/oauth/connections", b.tokens["ada"], "")
	list, _ := res["connections"].([]any)
	if code != http.StatusOK || len(list) != 2 {
		t.Fatalf("ada's connections: %d %s", code, raw)
	}
	byName := map[string]map[string]any{}
	for _, c := range list {
		byName[c.(map[string]any)["client_name"].(string)] = c.(map[string]any)
	}
	if byName["Cursor"]["agent_name"] != "ada-bot" || byName["Claude"]["agent_name"] != nil {
		t.Fatalf("identities in the list: %s", raw)
	}

	// Eve cannot end ada's connection; ada can, and its tokens stop.
	id := byName["Claude"]["id"].(string)
	if code, _, _ := b.do(http.MethodDelete, "/api/oauth/connections/"+id, b.tokens["eve"], ""); code != http.StatusNotFound {
		t.Fatalf("eve disconnected ada's app: %d", code)
	}
	if code, _, _ := b.do(http.MethodGet, "/api/auth/me", mine["access_token"].(string), ""); code != http.StatusOK {
		t.Fatalf("ada's app stopped after eve's attempt: %d", code)
	}
	if code, _, raw := b.do(http.MethodDelete, "/api/oauth/connections/"+id, b.tokens["ada"], ""); code != http.StatusOK {
		t.Fatalf("disconnect: %d %s", code, raw)
	}
	if code, _, _ := b.do(http.MethodGet, "/api/auth/me", mine["access_token"].(string), ""); code != http.StatusUnauthorized {
		t.Fatalf("a disconnected app still works: %d", code)
	}
	if code, _, _ := b.do(http.MethodGet, "/api/auth/me", asBot["access_token"].(string), ""); code != http.StatusOK {
		t.Fatalf("disconnecting one app stopped another: %d", code)
	}
	_, res, _ = b.do(http.MethodGet, "/api/oauth/connections", b.tokens["ada"], "")
	if list, _ := res["connections"].([]any); len(list) != 1 {
		t.Fatalf("after disconnecting: %v", res)
	}

	// Disabling the agent ends the connections made as it.
	if code, _, raw := b.do(http.MethodDelete, "/api/agents/"+bot, b.tokens["ada"], ""); code != http.StatusNoContent {
		t.Fatalf("disable: %d %s", code, raw)
	}
	_, res, _ = b.do(http.MethodGet, "/api/oauth/connections", b.tokens["ada"], "")
	if list, _ := res["connections"].([]any); len(list) != 0 {
		t.Fatalf("a disabled agent's connection is still listed: %v", res)
	}
}
