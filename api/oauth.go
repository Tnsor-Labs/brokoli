package api

// OAuth for MCP clients (ADR-046 phase 5): a hosted client (a chat
// assistant's connector, an IDE) connects to /api/mcp without a pasted
// token. Brokoli is both the authorization server and the protected
// resource, per the MCP authorization specification:
//
//   - discovery: /.well-known/oauth-protected-resource (RFC 9728) names the
//     authorization server, /.well-known/oauth-authorization-server
//     (RFC 8414) its endpoints; /api/mcp answers 401 with a
//     WWW-Authenticate header pointing at the first;
//   - dynamic client registration (RFC 7591), public clients only: no
//     client secret, so PKCE (S256) is required on every authorization;
//   - the authorization code grant: /oauth/authorize sends the browser to
//     the approval page, where the signed-in person approves or denies
//     (POST /api/oauth/authorize); /oauth/token trades the code, with its
//     verifier, for tokens;
//   - tokens are opaque and stored hashed: a bko_ access token lasts an
//     hour, a bkr_ refresh token 30 days and is replaced on each use;
//     /oauth/revoke ends a grant.
//
// A grant acts as the person who approved it, with that person's
// permissions as they are on each request, and stops when the person is
// disabled or removed. Agents cannot approve grants.

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/golang-jwt/jwt/v5"
)

const (
	// OAuthAccessTokenPrefix and OAuthRefreshTokenPrefix mark Brokoli's
	// OAuth tokens, which are looked up rather than parsed.
	OAuthAccessTokenPrefix  = "bko_"
	OAuthRefreshTokenPrefix = "bkr_"

	oauthAccessLifetime  = time.Hour
	oauthRefreshLifetime = 30 * 24 * time.Hour
	oauthCodeLifetime    = 10 * time.Minute
	oauthMaxRedirectURIs = 10
)

// OAuthClient is a registered client.
type OAuthClient struct {
	ID           string    `json:"client_id"`
	Name         string    `json:"client_name"`
	RedirectURIs []string  `json:"redirect_uris"`
	CreatedAt    time.Time `json:"-"`
}

func (us *UserStore) migrateOAuth() error {
	for _, stmt := range []string{
		`CREATE TABLE IF NOT EXISTS oauth_clients (
			id TEXT PRIMARY KEY,
			name TEXT NOT NULL,
			redirect_uris TEXT NOT NULL,
			created_at TEXT NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS oauth_codes (
			code_hash TEXT PRIMARY KEY,
			client_id TEXT NOT NULL,
			user_id TEXT NOT NULL,
			redirect_uri TEXT NOT NULL,
			challenge TEXT NOT NULL,
			scope TEXT NOT NULL DEFAULT '',
			expires_at TEXT NOT NULL,
			used INTEGER NOT NULL DEFAULT 0,
			agent_id TEXT NOT NULL DEFAULT ''
		)`,
		`CREATE TABLE IF NOT EXISTS oauth_grants (
			id TEXT PRIMARY KEY,
			client_id TEXT NOT NULL,
			user_id TEXT NOT NULL,
			scope TEXT NOT NULL DEFAULT '',
			access_hash TEXT NOT NULL,
			access_expires_at TEXT NOT NULL,
			refresh_hash TEXT NOT NULL,
			refresh_expires_at TEXT NOT NULL,
			previous_refresh_hash TEXT NOT NULL DEFAULT '',
			created_at TEXT NOT NULL,
			last_used_at TEXT NOT NULL DEFAULT '',
			revoked_at TEXT NOT NULL DEFAULT '',
			agent_id TEXT NOT NULL DEFAULT ''
		)`,
		`ALTER TABLE oauth_grants ADD COLUMN previous_refresh_hash TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE oauth_codes ADD COLUMN agent_id TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE oauth_grants ADD COLUMN agent_id TEXT NOT NULL DEFAULT ''`,
		`CREATE INDEX IF NOT EXISTS idx_oauth_grants_access ON oauth_grants (access_hash)`,
		`CREATE INDEX IF NOT EXISTS idx_oauth_grants_refresh ON oauth_grants (refresh_hash)`,
		`CREATE INDEX IF NOT EXISTS idx_oauth_grants_user ON oauth_grants (user_id)`,
	} {
		if _, err := us.db.Exec(stmt); err != nil {
			if strings.HasPrefix(stmt, "ALTER TABLE") {
				continue // the column exists: the normal case on every boot
			}
			return fmt.Errorf("oauth tables: %w", err)
		}
	}
	return nil
}

func oauthHash(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

func oauthSecret(prefix string) (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return prefix + hex.EncodeToString(buf), nil
}

// validRedirectURI accepts https URLs, and http only to the loopback
// address, as OAuth 2.1 allows for native clients.
func validRedirectURI(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || u.Fragment != "" || u.Host == "" {
		return false
	}
	switch u.Scheme {
	case "https":
		return true
	case "http":
		host := u.Hostname()
		return host == "localhost" || host == "127.0.0.1" || host == "::1"
	}
	return false
}

// RegisterOAuthClient records a public client.
func (us *UserStore) RegisterOAuthClient(name string, redirectURIs []string) (*OAuthClient, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		name = "MCP client"
	}
	if len(name) > 100 {
		name = name[:100]
	}
	if len(redirectURIs) == 0 || len(redirectURIs) > oauthMaxRedirectURIs {
		return nil, fmt.Errorf("give between 1 and %d redirect_uris", oauthMaxRedirectURIs)
	}
	for _, u := range redirectURIs {
		if !validRedirectURI(u) {
			return nil, fmt.Errorf("redirect_uri %q must be https, or http to localhost", u)
		}
	}
	id, err := oauthSecret("mcp_")
	if err != nil {
		return nil, err
	}
	c := &OAuthClient{ID: id[:4+24], Name: name, RedirectURIs: redirectURIs, CreatedAt: time.Now().UTC()}
	uris, _ := json.Marshal(redirectURIs)
	if _, err := us.db.Exec(us.q(`INSERT INTO oauth_clients (id, name, redirect_uris, created_at) VALUES (?, ?, ?, ?)`),
		c.ID, c.Name, string(uris), c.CreatedAt.Format(time.RFC3339)); err != nil {
		return nil, err
	}
	return c, nil
}

// OAuthClientByID returns a registered client.
func (us *UserStore) OAuthClientByID(id string) (*OAuthClient, error) {
	var c OAuthClient
	var uris, created string
	if err := us.db.QueryRow(us.q(`SELECT id, name, redirect_uris, created_at FROM oauth_clients WHERE id = ?`), id).
		Scan(&c.ID, &c.Name, &uris, &created); err != nil {
		return nil, err
	}
	_ = json.Unmarshal([]byte(uris), &c.RedirectURIs)
	c.CreatedAt, _ = time.Parse(time.RFC3339, created)
	return &c, nil
}

func (c *OAuthClient) allows(redirectURI string) bool {
	for _, u := range c.RedirectURIs {
		if u == redirectURI {
			return true
		}
	}
	return false
}

// pkceS256 is the S256 code challenge of a verifier.
func pkceS256(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

// issueOAuthCode records an approved authorization and returns its code.
// agentID, when set, is the approver's agent the connection acts as.
func (us *UserStore) issueOAuthCode(clientID, userID, agentID, redirectURI, challenge, scope string) (string, error) {
	code, err := oauthSecret("")
	if err != nil {
		return "", err
	}
	_, err = us.db.Exec(us.q(`INSERT INTO oauth_codes (code_hash, client_id, user_id, agent_id, redirect_uri, challenge, scope, expires_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`), oauthHash(code), clientID, userID, agentID, redirectURI, challenge, scope,
		time.Now().UTC().Add(oauthCodeLifetime).Format(time.RFC3339))
	return code, err
}

var errOAuthGrant = errors.New("invalid_grant")

// redeemOAuthCode spends a code once, checking its client, redirect and
// verifier, and returns who approved it and the agent it acts as, if any.
func (us *UserStore) redeemOAuthCode(code, clientID, redirectURI, verifier string) (userID, agentID, scope string, err error) {
	var cClient, cUser, cRedirect, challenge, expires string
	var used int
	h := oauthHash(code)
	if err := us.db.QueryRow(us.q(`SELECT client_id, user_id, agent_id, redirect_uri, challenge, scope, expires_at, used FROM oauth_codes WHERE code_hash = ?`), h).
		Scan(&cClient, &cUser, &agentID, &cRedirect, &challenge, &scope, &expires, &used); err != nil {
		return "", "", "", errOAuthGrant
	}
	// Spent whatever happens next: a code is good for one attempt.
	res, err := us.db.Exec(us.q(`UPDATE oauth_codes SET used = 1 WHERE code_hash = ? AND used = 0`), h)
	if err != nil {
		return "", "", "", err
	}
	if n, _ := res.RowsAffected(); n != 1 || used != 0 {
		return "", "", "", errOAuthGrant
	}
	exp, _ := time.Parse(time.RFC3339, expires)
	switch {
	case !time.Now().UTC().Before(exp):
		return "", "", "", errOAuthGrant
	case cClient != clientID || cRedirect != redirectURI:
		return "", "", "", errOAuthGrant
	case verifier == "" || subtle.ConstantTimeCompare([]byte(pkceS256(verifier)), []byte(challenge)) != 1:
		return "", "", "", errOAuthGrant
	}
	return cUser, agentID, scope, nil
}

// OAuthTokens is a token endpoint answer.
type OAuthTokens struct {
	AccessToken  string `json:"access_token"`
	TokenType    string `json:"token_type"`
	ExpiresIn    int    `json:"expires_in"`
	RefreshToken string `json:"refresh_token"`
	Scope        string `json:"scope,omitempty"`
}

func newOAuthPair() (access, refresh string, err error) {
	if access, err = oauthSecret(OAuthAccessTokenPrefix); err != nil {
		return "", "", err
	}
	refresh, err = oauthSecret(OAuthRefreshTokenPrefix)
	return access, refresh, err
}

// createOAuthGrant starts a grant for an approved code.
func (us *UserStore) createOAuthGrant(clientID, userID, agentID, scope string) (*OAuthTokens, error) {
	access, refresh, err := newOAuthPair()
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	_, err = us.db.Exec(us.q(`INSERT INTO oauth_grants (id, client_id, user_id, agent_id, scope, access_hash, access_expires_at, refresh_hash, refresh_expires_at, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`), generateID(), clientID, userID, agentID, scope, oauthHash(access),
		now.Add(oauthAccessLifetime).Format(time.RFC3339), oauthHash(refresh), now.Add(oauthRefreshLifetime).Format(time.RFC3339),
		now.Format(time.RFC3339))
	if err != nil {
		return nil, err
	}
	return &OAuthTokens{AccessToken: access, TokenType: "Bearer", ExpiresIn: int(oauthAccessLifetime.Seconds()), RefreshToken: refresh, Scope: scope}, nil
}

// refreshOAuthGrant replaces a grant's tokens. A refresh token is good
// once; presenting a spent one ends the grant (a stolen copy is in use).
func (us *UserStore) refreshOAuthGrant(refresh, clientID string) (*OAuthTokens, error) {
	var id, gClient, userID, agentID, scope, expires, revoked string
	if err := us.db.QueryRow(us.q(`SELECT id, client_id, user_id, agent_id, scope, refresh_expires_at, revoked_at FROM oauth_grants WHERE refresh_hash = ?`),
		oauthHash(refresh)).Scan(&id, &gClient, &userID, &agentID, &scope, &expires, &revoked); err != nil {
		return nil, errOAuthGrant
	}
	exp, _ := time.Parse(time.RFC3339, expires)
	if revoked != "" || gClient != clientID || !time.Now().UTC().Before(exp) {
		return nil, errOAuthGrant
	}
	if _, ok := us.oauthPerson(userID); !ok {
		return nil, errOAuthGrant
	}
	if agentID != "" {
		if _, ok := us.oauthAgent(agentID, userID); !ok {
			return nil, errOAuthGrant
		}
	}
	access, next, err := newOAuthPair()
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	// The token just spent is kept as the previous one: presented again, it
	// is a copy in someone else's hands, and the grant ends.
	res, err := us.db.Exec(us.q(`UPDATE oauth_grants SET access_hash = ?, access_expires_at = ?, refresh_hash = ?, refresh_expires_at = ?,
		previous_refresh_hash = ? WHERE id = ? AND refresh_hash = ? AND revoked_at = ''`), oauthHash(access), now.Add(oauthAccessLifetime).Format(time.RFC3339),
		oauthHash(next), now.Add(oauthRefreshLifetime).Format(time.RFC3339), oauthHash(refresh), id, oauthHash(refresh))
	if err != nil {
		return nil, err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return nil, errOAuthGrant
	}
	return &OAuthTokens{AccessToken: access, TokenType: "Bearer", ExpiresIn: int(oauthAccessLifetime.Seconds()), RefreshToken: next, Scope: scope}, nil
}

// RevokeOAuthToken ends the grant an access or refresh token belongs to.
func (us *UserStore) RevokeOAuthToken(token string) {
	h := oauthHash(token)
	_, _ = us.db.Exec(us.q(`UPDATE oauth_grants SET revoked_at = ? WHERE (access_hash = ? OR refresh_hash = ? OR previous_refresh_hash = ?) AND revoked_at = ''`),
		time.Now().UTC().Format(time.RFC3339), h, h, h)
}

// oauthPerson is the person a grant acts as, while they are an enabled
// person.
func (us *UserStore) oauthPerson(userID string) (*User, bool) {
	var kind, disabled string
	if err := us.db.QueryRow(us.q(`SELECT kind, disabled_at FROM users WHERE id = ?`), userID).Scan(&kind, &disabled); err != nil {
		return nil, false
	}
	if kind != UserKindPerson || disabled != "" {
		return nil, false
	}
	u, err := us.GetUserByID(userID)
	if err != nil || u == nil {
		return nil, false
	}
	return u, true
}

// oauthAgent is the agent a grant acts as, while it is enabled and still
// owned by the person who approved the grant.
func (us *UserStore) oauthAgent(agentID, ownerID string) (*Agent, bool) {
	if !AgentIdentitiesEnabled {
		return nil, false
	}
	a, err := us.GetAgent(agentID)
	if err != nil || a.Disabled || a.OwnerID != ownerID {
		return nil, false
	}
	return a, true
}

// ResolveOAuthToken returns the claims for a live OAuth access token: the
// person who approved the grant, or the agent of theirs they chose, as
// they are now.
func (us *UserStore) ResolveOAuthToken(token string) (*jwt.MapClaims, bool) {
	if !strings.HasPrefix(token, OAuthAccessTokenPrefix) {
		return nil, false
	}
	var id, clientID, userID, agentID, expires, revoked, used string
	if err := us.db.QueryRow(us.q(`SELECT id, client_id, user_id, agent_id, access_expires_at, revoked_at, last_used_at FROM oauth_grants WHERE access_hash = ?`),
		oauthHash(token)).Scan(&id, &clientID, &userID, &agentID, &expires, &revoked, &used); err != nil {
		return nil, false
	}
	exp, err := time.Parse(time.RFC3339, expires)
	now := time.Now().UTC()
	if err != nil || revoked != "" || !now.Before(exp) {
		return nil, false
	}
	u, ok := us.oauthPerson(userID)
	if !ok {
		return nil, false
	}
	var agent *Agent
	if agentID != "" {
		if agent, ok = us.oauthAgent(agentID, userID); !ok {
			return nil, false
		}
	}
	if last := parseStoredTime(used); last == nil || now.Sub(*last) >= lastUsedEvery {
		_, _ = us.db.Exec(us.q(`UPDATE oauth_grants SET last_used_at = ? WHERE id = ?`), now.Format(time.RFC3339), id)
	}
	if agent != nil {
		// The connection acts as the agent, answered for by the person who
		// approved it, exactly as one of the agent's own tokens would.
		claims := agentClaims(agent.ID, agent.Name, agent.Role, u.ID, string(u.Role))
		claims["exp"] = exp.Unix()
		claims["oauth_client_id"] = clientID
		claims["oauth_grant_id"] = id
		claims["token_name"] = "oauth:" + clientID
		return &claims, true
	}
	claims := jwt.MapClaims{
		"sub":             u.ID,
		"username":        u.Username,
		"role":            string(u.Role),
		"exp":             exp.Unix(),
		"oauth_client_id": clientID,
		"oauth_grant_id":  id,
	}
	if u.DisplayName != "" {
		claims["display_name"] = u.DisplayName
	}
	if u.Email != "" {
		claims["email"] = u.Email
	}
	if OrgResolverFunc != nil {
		if orgID := OrgResolverFunc(u.ID); orgID != "" {
			claims["org_id"] = orgID
		}
	}
	return &claims, true
}

// --- the endpoints

// oauthIssuer is the server's own base URL, as the client reached it.
func oauthIssuer(r *http.Request) string {
	if base := strings.TrimRight(oauthPublicURL, "/"); base != "" {
		return base
	}
	scheme := "http"
	if r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https") {
		scheme = "https"
	}
	return scheme + "://" + r.Host
}

// oauthPublicURL, when set (BROKOLI_PUBLIC_URL), is the base URL the
// discovery documents name, instead of the one a request arrived on.
var oauthPublicURL = strings.TrimSpace(os.Getenv("BROKOLI_PUBLIC_URL"))

// OAuthResourceMetadataURL is where /api/mcp's 401 points a client.
func OAuthResourceMetadataURL(r *http.Request) string {
	return oauthIssuer(r) + "/.well-known/oauth-protected-resource"
}

func oauthProtectedResource(w http.ResponseWriter, r *http.Request) {
	base := oauthIssuer(r)
	writeJSON(w, http.StatusOK, map[string]any{
		"resource":                 base + "/api/mcp",
		"authorization_servers":    []string{base},
		"bearer_methods_supported": []string{"header"},
		"resource_name":            "Brokoli",
	})
}

func oauthAuthorizationServer(w http.ResponseWriter, r *http.Request) {
	base := oauthIssuer(r)
	writeJSON(w, http.StatusOK, map[string]any{
		"issuer":                                base,
		"authorization_endpoint":                base + "/oauth/authorize",
		"token_endpoint":                        base + "/oauth/token",
		"registration_endpoint":                 base + "/oauth/register",
		"revocation_endpoint":                   base + "/oauth/revoke",
		"response_types_supported":              []string{"code"},
		"grant_types_supported":                 []string{"authorization_code", "refresh_token"},
		"code_challenge_methods_supported":      []string{"S256"},
		"token_endpoint_auth_methods_supported": []string{"none"},
		"scopes_supported":                      []string{"mcp"},
	})
}

func oauthError(w http.ResponseWriter, status int, code, description string) {
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, status, map[string]string{"error": code, "error_description": description})
}

func oauthRegister(us *UserStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			ClientName   string   `json:"client_name"`
			RedirectURIs []string `json:"redirect_uris"`
			AuthMethod   string   `json:"token_endpoint_auth_method"`
		}
		if err := json.NewDecoder(io.LimitReader(r.Body, 16<<10)).Decode(&req); err != nil {
			oauthError(w, http.StatusBadRequest, "invalid_client_metadata", "send the client's metadata as JSON")
			return
		}
		if req.AuthMethod != "" && req.AuthMethod != "none" {
			oauthError(w, http.StatusBadRequest, "invalid_client_metadata", "only public clients are registered: token_endpoint_auth_method must be none, with PKCE")
			return
		}
		c, err := us.RegisterOAuthClient(req.ClientName, req.RedirectURIs)
		if err != nil {
			oauthError(w, http.StatusBadRequest, "invalid_redirect_uri", err.Error())
			return
		}
		writeJSON(w, http.StatusCreated, map[string]any{
			"client_id": c.ID, "client_name": c.Name, "redirect_uris": c.RedirectURIs,
			"token_endpoint_auth_method": "none", "grant_types": []string{"authorization_code", "refresh_token"},
			"response_types": []string{"code"}, "client_id_issued_at": c.CreatedAt.Unix(),
		})
	}
}

// authorizeRequest is an authorization request's parameters, checked.
type authorizeRequest struct {
	ClientID, RedirectURI, State, Challenge, Scope string
}

// checkAuthorize validates an authorization request. A request whose
// client or redirect is unknown is answered here, never redirected:
// sending a browser to an unregistered address is how codes leak.
func (us *UserStore) checkAuthorize(q url.Values) (*authorizeRequest, *OAuthClient, string) {
	c, err := us.OAuthClientByID(q.Get("client_id"))
	if err != nil {
		return nil, nil, "unknown client_id; register the client first"
	}
	redirect := q.Get("redirect_uri")
	if !c.allows(redirect) {
		return nil, nil, "redirect_uri is not one the client registered"
	}
	if q.Get("response_type") != "code" {
		return nil, c, "unsupported_response_type"
	}
	if q.Get("code_challenge") == "" || q.Get("code_challenge_method") != "S256" {
		return nil, c, "invalid_request: PKCE with code_challenge_method S256 is required"
	}
	scope := q.Get("scope")
	if scope == "" {
		scope = "mcp"
	}
	return &authorizeRequest{ClientID: c.ID, RedirectURI: redirect, State: q.Get("state"), Challenge: q.Get("code_challenge"), Scope: scope}, c, ""
}

func redirectWith(base string, params map[string]string) string {
	u, _ := url.Parse(base)
	q := u.Query()
	for k, v := range params {
		if v != "" {
			q.Set(k, v)
		}
	}
	u.RawQuery = q.Encode()
	return u.String()
}

// oauthAuthorize checks the request and sends the browser to the approval
// page, which signs the person in first if need be.
func oauthAuthorize(us *UserStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		req, c, problem := us.checkAuthorize(r.URL.Query())
		switch {
		case req == nil && c == nil:
			http.Error(w, "Brokoli cannot authorize this client: "+problem, http.StatusBadRequest)
			return
		case req == nil:
			code, desc, _ := strings.Cut(problem, ": ")
			http.Redirect(w, r, redirectWith(r.URL.Query().Get("redirect_uri"), map[string]string{"error": code, "error_description": desc, "state": r.URL.Query().Get("state")}), http.StatusFound)
			return
		}
		http.Redirect(w, r, "/#/oauth/authorize?"+r.URL.RawQuery, http.StatusFound)
	}
}

// oauthDecide serves POST /api/oauth/authorize: the signed-in person
// approves or denies. It answers where to send the browser.
func oauthDecide(us *UserStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		claims, _ := r.Context().Value("claims").(*jwt.MapClaims)
		if claims == nil {
			writeError(w, http.StatusUnauthorized, "sign in to approve")
			return
		}
		if isAgentClaims(claims) {
			writeError(w, http.StatusForbidden, "an agent cannot approve a connection; a person does")
			return
		}
		// JSON only: a cross-site form cannot send it, so a signed-in
		// browser cannot be made to approve from another site.
		if ct := r.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
			writeError(w, http.StatusUnsupportedMediaType, "send the decision as application/json")
			return
		}
		userID, _ := (*claims)["sub"].(string)
		var body struct {
			Query   string `json:"query"`
			Approve bool   `json:"approve"`
			AgentID string `json:"agent_id"`
		}
		if err := json.NewDecoder(io.LimitReader(r.Body, 16<<10)).Decode(&body); err != nil {
			writeError(w, http.StatusBadRequest, `send {"query": "<the authorization request's query>", "approve": true}`)
			return
		}
		q, err := url.ParseQuery(strings.TrimPrefix(body.Query, "?"))
		if err != nil {
			writeError(w, http.StatusBadRequest, "the authorization request is malformed")
			return
		}
		req, c, problem := us.checkAuthorize(q)
		if req == nil {
			writeError(w, http.StatusBadRequest, problem)
			return
		}
		if !body.Approve {
			AuditLog(r, "oauth_deny", "oauth_client", c.ID, nil, map[string]any{"client": c.Name})
			writeJSON(w, http.StatusOK, map[string]string{"redirect": redirectWith(req.RedirectURI, map[string]string{"error": "access_denied", "state": req.State})})
			return
		}
		if _, ok := us.oauthPerson(userID); !ok {
			writeError(w, http.StatusForbidden, "only a person's account can approve a connection")
			return
		}
		identity := map[string]any{"client": c.Name, "redirect_uri": req.RedirectURI}
		if body.AgentID != "" {
			a, ok := us.oauthAgent(body.AgentID, userID)
			if !ok {
				writeError(w, http.StatusForbidden, "the connection can act only as you or an enabled agent you own")
				return
			}
			identity["agent"] = a.Name
		}
		code, err := us.issueOAuthCode(c.ID, userID, body.AgentID, req.RedirectURI, req.Challenge, req.Scope)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "could not approve the connection")
			return
		}
		AuditLog(r, "oauth_approve", "oauth_client", c.ID, nil, identity)
		writeJSON(w, http.StatusOK, map[string]string{"redirect": redirectWith(req.RedirectURI, map[string]string{"code": code, "state": req.State})})
	}
}

// oauthRequestInfo serves GET /api/oauth/authorize?<the request's query>
// for the approval page: the client's name and where it sends the person.
func oauthRequestInfo(us *UserStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		req, c, problem := us.checkAuthorize(r.URL.Query())
		if req == nil {
			writeError(w, http.StatusBadRequest, problem)
			return
		}
		u, _ := url.Parse(req.RedirectURI)
		// The identities the person may give the connection: themselves, or
		// one of their enabled agents.
		agents := []map[string]string{}
		if claims, _ := r.Context().Value("claims").(*jwt.MapClaims); claims != nil && !isAgentClaims(claims) && AgentIdentitiesEnabled {
			userID, _ := (*claims)["sub"].(string)
			if owned, err := us.ListAgents(userID); err == nil {
				for _, a := range owned {
					if !a.Disabled {
						agents = append(agents, map[string]string{"id": a.ID, "name": a.Name, "role": a.Role, "description": a.Description})
					}
				}
			}
		}
		writeJSON(w, http.StatusOK, map[string]any{"client_name": c.Name, "redirect_host": u.Host, "scope": req.Scope, "agents": agents})
	}
}

func oauthToken(us *UserStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
		if err := r.ParseForm(); err != nil {
			oauthError(w, http.StatusBadRequest, "invalid_request", "send the token request form-encoded")
			return
		}
		clientID := r.PostForm.Get("client_id")
		if _, err := us.OAuthClientByID(clientID); err != nil {
			oauthError(w, http.StatusUnauthorized, "invalid_client", "unknown client_id")
			return
		}
		var tokens *OAuthTokens
		var err error
		switch r.PostForm.Get("grant_type") {
		case "authorization_code":
			var userID, agentID, scope string
			userID, agentID, scope, err = us.redeemOAuthCode(r.PostForm.Get("code"), clientID, r.PostForm.Get("redirect_uri"), r.PostForm.Get("code_verifier"))
			if err == nil {
				tokens, err = us.createOAuthGrant(clientID, userID, agentID, scope)
			}
		case "refresh_token":
			tokens, err = us.refreshOAuthGrant(r.PostForm.Get("refresh_token"), clientID)
			if errors.Is(err, errOAuthGrant) {
				// A refresh token that is not live may be a stolen, already
				// spent one: whatever grant it names ends.
				us.RevokeOAuthToken(r.PostForm.Get("refresh_token"))
			}
		default:
			oauthError(w, http.StatusBadRequest, "unsupported_grant_type", "grant_type is authorization_code or refresh_token")
			return
		}
		if errors.Is(err, errOAuthGrant) {
			oauthError(w, http.StatusBadRequest, "invalid_grant", "the code or refresh token is invalid, expired, spent, or not this client's")
			return
		}
		if err != nil {
			oauthError(w, http.StatusInternalServerError, "server_error", "could not issue tokens")
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		writeJSON(w, http.StatusOK, tokens)
	}
}

func oauthRevoke(us *UserStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
		_ = r.ParseForm()
		if t := r.PostForm.Get("token"); t != "" {
			us.RevokeOAuthToken(t)
		}
		// RFC 7009: the answer is the same whether or not the token existed.
		w.WriteHeader(http.StatusOK)
	}
}

// oauthAuthenticate marks a 401 from /api/mcp with where to find the
// authorization server (RFC 9728 section 5.1).
func oauthAuthenticate(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/api/mcp" || strings.HasPrefix(r.URL.Path, "/api/mcp/") {
		w.Header().Set("WWW-Authenticate", fmt.Sprintf(`Bearer resource_metadata=%q`, OAuthResourceMetadataURL(r)))
	}
}

// --- connected apps

// OAuthConnection is a live grant, as its person sees it.
type OAuthConnection struct {
	ID         string     `json:"id"`
	ClientID   string     `json:"client_id"`
	ClientName string     `json:"client_name"`
	AgentID    string     `json:"agent_id,omitempty"`
	AgentName  string     `json:"agent_name,omitempty"`
	CreatedAt  time.Time  `json:"created_at"`
	LastUsedAt *time.Time `json:"last_used_at,omitempty"`
}

// ListOAuthConnections returns userID's live grants, newest first. A grant
// whose refresh token has expired is not live: it can never act again.
func (us *UserStore) ListOAuthConnections(userID string) ([]OAuthConnection, error) {
	rows, err := us.db.Query(us.q(`SELECT g.id, g.client_id, COALESCE(c.name, ''), g.agent_id, COALESCE(NULLIF(a.display_name, ''), a.username, ''), g.created_at, g.last_used_at, g.refresh_expires_at
		FROM oauth_grants g
		LEFT JOIN oauth_clients c ON c.id = g.client_id
		LEFT JOIN users a ON a.id = g.agent_id AND g.agent_id <> ''
		WHERE g.user_id = ? AND g.revoked_at = ''
		ORDER BY g.created_at DESC`), userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	now := time.Now().UTC()
	out := []OAuthConnection{}
	for rows.Next() {
		var c OAuthConnection
		var created, used, refreshExpires string
		if err := rows.Scan(&c.ID, &c.ClientID, &c.ClientName, &c.AgentID, &c.AgentName, &created, &used, &refreshExpires); err != nil {
			return nil, err
		}
		if exp, err := time.Parse(time.RFC3339, refreshExpires); err != nil || !now.Before(exp) {
			continue
		}
		c.CreatedAt, _ = time.Parse(time.RFC3339, created)
		c.LastUsedAt = parseStoredTime(used)
		out = append(out, c)
	}
	return out, rows.Err()
}

// RevokeOAuthConnection ends one of userID's grants.
func (us *UserStore) RevokeOAuthConnection(userID, grantID string) error {
	res, err := us.db.Exec(us.q(`UPDATE oauth_grants SET revoked_at = ? WHERE id = ? AND user_id = ? AND revoked_at = ''`),
		time.Now().UTC().Format(time.RFC3339), grantID, userID)
	return oneRow(res, err, errOAuthConnectionNotFound)
}

var errOAuthConnectionNotFound = errors.New("no such connection")

// connectionOwner is the person whose connections a caller manages: a
// person, never an agent (an agent acting through a connection must not
// be able to see or end the others).
func connectionOwner(w http.ResponseWriter, r *http.Request) (string, bool) {
	claims, _ := r.Context().Value("claims").(*jwt.MapClaims)
	if claims == nil {
		writeError(w, http.StatusUnauthorized, "sign in to manage your connected apps")
		return "", false
	}
	if isAgentClaims(claims) {
		writeError(w, http.StatusForbidden, "an agent cannot manage connected apps; a person does")
		return "", false
	}
	userID, _ := (*claims)["sub"].(string)
	return userID, userID != ""
}

// oauthConnections serves GET /api/oauth/connections: the caller's
// connected apps.
func oauthConnections(us *UserStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		userID, ok := connectionOwner(w, r)
		if !ok {
			return
		}
		list, err := us.ListOAuthConnections(userID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "could not list your connected apps")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"connections": list})
	}
}

// oauthDisconnect serves DELETE /api/oauth/connections/{id}: the app's
// tokens stop working at once.
func oauthDisconnect(us *UserStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		userID, ok := connectionOwner(w, r)
		if !ok {
			return
		}
		id := chi.URLParam(r, "id")
		if err := us.RevokeOAuthConnection(userID, id); err != nil {
			if errors.Is(err, errOAuthConnectionNotFound) {
				writeError(w, http.StatusNotFound, "no such connection")
				return
			}
			writeError(w, http.StatusInternalServerError, "could not disconnect the app")
			return
		}
		AuditLog(r, "oauth_disconnect", "oauth_grant", id, nil, nil)
		writeJSON(w, http.StatusOK, map[string]any{"disconnected": true})
	}
}
