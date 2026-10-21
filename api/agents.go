package api

// Agent identities (ADR-046 phase 2).
//
// An agent is a user of kind "agent": it has a name, an owner (the person
// who answers for it), a role, and tokens. It has no password and cannot
// sign in. Its tokens are opaque ("bka_..."), stored only as a hash,
// expire, can be revoked one by one, and record when they were last used.
//
// A request with an agent token gets claims like a person's, plus what
// makes it an agent: kind, owner, the owner's current role, the token. Its
// permissions are its role's, intersected with its owner's (so an agent is
// never above the person answering for it, and narrows when they do), and
// never include reading secrets or managing users and roles, whatever the
// role says.

import (
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/Tnsor-Labs/brokoli/models"
	"github.com/golang-jwt/jwt/v5"
)

// AgentIdentitiesEnabled turns agent identities on (the default). A
// distribution whose tenancy agents do not yet take part in sets it to
// false before serving: then /api/agents answers 404 and no agent token
// authenticates. An agent belongs to no organization in core, and a
// multi-tenant distribution must not let one act until it binds agents to
// one.
var AgentIdentitiesEnabled = true

// AgentTokenPrefix starts every agent token, so a token is recognizable in
// a log or a leaked file, and the middleware knows to look it up.
const AgentTokenPrefix = "bka_"

// AgentRole is the built-in role an agent gets unless given another.
const AgentRole = "agent"

// Kinds of user.
const (
	UserKindPerson = "person"
	UserKindAgent  = "agent"
)

// agentNeverPermissions are withheld from every agent, whatever its role:
// an agent must not read a secret, nor create or empower identities.
var agentNeverPermissions = map[models.Permission]bool{
	models.PermVariablesViewSecret:     true,
	models.PermSettingsManageUsers:     true,
	models.PermSettingsManageRoles:     true,
	models.PermWorkspacesManageMembers: true,
	models.PermWorkspacesManageTokens:  true,
}

// Token lifetimes, in days.
const (
	defaultAgentTokenDays = 90
	maxAgentTokenDays     = 365
)

var agentNamePattern = regexp.MustCompile(`^[a-z][a-z0-9-]{1,47}$`)

// Errors the handlers turn into answers.
var (
	ErrAgentNotFound = errors.New("agent not found")
	ErrTokenNotFound = errors.New("token not found")
)

// Agent is an agent identity.
type Agent struct {
	ID          string     `json:"id"`
	Name        string     `json:"name"`
	Description string     `json:"description,omitempty"`
	Role        string     `json:"role"`
	OwnerID     string     `json:"owner_id"`
	OwnerName   string     `json:"owner_name,omitempty"`
	Disabled    bool       `json:"disabled"`
	CreatedAt   time.Time  `json:"created_at"`
	LastUsedAt  *time.Time `json:"last_used_at,omitempty"`
}

// AgentToken is a token's record; the token itself is never stored.
type AgentToken struct {
	ID         string     `json:"id"`
	AgentID    string     `json:"agent_id"`
	Name       string     `json:"name"`
	Prefix     string     `json:"prefix"` // the first characters, to recognize it
	OnBehalfOf bool       `json:"on_behalf_of_owner"`
	CreatedBy  string     `json:"created_by"`
	CreatedAt  time.Time  `json:"created_at"`
	ExpiresAt  time.Time  `json:"expires_at"`
	LastUsedAt *time.Time `json:"last_used_at,omitempty"`
	RevokedAt  *time.Time `json:"revoked_at,omitempty"`
}

func (us *UserStore) migrateAgents() error {
	// Additive columns; "already exists" is the normal case on every boot.
	_, _ = us.db.Exec(`ALTER TABLE users ADD COLUMN kind TEXT NOT NULL DEFAULT 'person'`)
	_, _ = us.db.Exec(`ALTER TABLE users ADD COLUMN owner_id TEXT NOT NULL DEFAULT ''`)
	_, _ = us.db.Exec(`ALTER TABLE users ADD COLUMN description TEXT NOT NULL DEFAULT ''`)
	_, _ = us.db.Exec(`ALTER TABLE users ADD COLUMN disabled_at TEXT NOT NULL DEFAULT ''`)
	if _, err := us.db.Exec(`CREATE TABLE IF NOT EXISTS agent_tokens (
		id TEXT PRIMARY KEY,
		agent_id TEXT NOT NULL,
		name TEXT NOT NULL,
		token_hash TEXT NOT NULL UNIQUE,
		prefix TEXT NOT NULL,
		on_behalf_of INTEGER NOT NULL DEFAULT 0,
		created_by TEXT NOT NULL,
		created_at TEXT NOT NULL,
		expires_at TEXT NOT NULL,
		last_used_at TEXT NOT NULL DEFAULT '',
		revoked_at TEXT NOT NULL DEFAULT ''
	)`); err != nil {
		return fmt.Errorf("create agent_tokens: %w", err)
	}
	_, err := us.db.Exec(`CREATE INDEX IF NOT EXISTS idx_agent_tokens_agent ON agent_tokens (agent_id)`)
	return err
}

func hashAgentToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

func parseStoredTime(s string) *time.Time {
	if s == "" {
		return nil
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return nil
	}
	return &t
}

// CreateAgent makes an agent owned by ownerID.
func (us *UserStore) CreateAgent(name, description, role, ownerID string) (*Agent, error) {
	if !agentNamePattern.MatchString(name) {
		return nil, fmt.Errorf("an agent's name is 2 to 48 lowercase letters, digits and dashes, starting with a letter")
	}
	id := generateID()
	now := time.Now().UTC()
	// "!" is not a bcrypt hash: no password can ever match it.
	_, err := us.db.Exec(us.q(`INSERT INTO users (id, username, password_hash, role, created_at, kind, owner_id, description)
		VALUES (?, ?, '!', ?, ?, ?, ?, ?)`), id, name, role, now.Format(time.RFC3339), UserKindAgent, ownerID, description)
	if err != nil {
		msg := strings.ToLower(err.Error())
		if strings.Contains(msg, "unique") || strings.Contains(msg, "duplicate") {
			return nil, ErrUserExists
		}
		return nil, fmt.Errorf("create agent: %w", err)
	}
	return us.GetAgent(id)
}

const agentColumns = `u.id, u.username, u.description, u.role, u.owner_id, COALESCE(o.username, ''), u.disabled_at, u.created_at,
	COALESCE((SELECT MAX(t.last_used_at) FROM agent_tokens t WHERE t.agent_id = u.id), '')`

func scanAgent(row interface{ Scan(...any) error }) (*Agent, error) {
	var a Agent
	var disabled, created, lastUsed string
	if err := row.Scan(&a.ID, &a.Name, &a.Description, &a.Role, &a.OwnerID, &a.OwnerName, &disabled, &created, &lastUsed); err != nil {
		return nil, err
	}
	a.Disabled = disabled != ""
	a.CreatedAt, _ = time.Parse(time.RFC3339, created)
	a.LastUsedAt = parseStoredTime(lastUsed)
	return &a, nil
}

// GetAgent returns an agent, or ErrAgentNotFound.
func (us *UserStore) GetAgent(id string) (*Agent, error) {
	a, err := scanAgent(us.db.QueryRow(us.q(`SELECT `+agentColumns+` FROM users u LEFT JOIN users o ON o.id = u.owner_id
		WHERE u.id = ? AND u.kind = ?`), id, UserKindAgent))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrAgentNotFound
	}
	return a, err
}

// ListAgents returns every agent, or those ownerID owns when it is set.
func (us *UserStore) ListAgents(ownerID string) ([]Agent, error) {
	query := `SELECT ` + agentColumns + ` FROM users u LEFT JOIN users o ON o.id = u.owner_id WHERE u.kind = ?`
	args := []any{UserKindAgent}
	if ownerID != "" {
		query += ` AND u.owner_id = ?`
		args = append(args, ownerID)
	}
	rows, err := us.db.Query(us.q(query+` ORDER BY u.created_at`), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Agent{}
	for rows.Next() {
		a, err := scanAgent(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *a)
	}
	return out, rows.Err()
}

// UpdateAgent sets an agent's role and description.
func (us *UserStore) UpdateAgent(id, role, description string) error {
	res, err := us.db.Exec(us.q(`UPDATE users SET role = ?, description = ? WHERE id = ? AND kind = ?`), role, description, id, UserKindAgent)
	return oneRow(res, err, ErrAgentNotFound)
}

// DisableAgent stops an agent and revokes every token it has. The agent is
// kept, so what it did stays attributed to it.
func (us *UserStore) DisableAgent(id string) error {
	now := time.Now().UTC().Format(time.RFC3339)
	res, err := us.db.Exec(us.q(`UPDATE users SET disabled_at = ? WHERE id = ? AND kind = ? AND disabled_at = ''`), now, id, UserKindAgent)
	if err := oneRow(res, err, ErrAgentNotFound); err != nil {
		if _, getErr := us.GetAgent(id); getErr != nil {
			return ErrAgentNotFound
		}
	}
	if _, err = us.db.Exec(us.q(`UPDATE agent_tokens SET revoked_at = ? WHERE agent_id = ? AND revoked_at = ''`), now, id); err != nil {
		return err
	}
	// Connections made to act as the agent end with it.
	_, err = us.db.Exec(us.q(`UPDATE oauth_grants SET revoked_at = ? WHERE agent_id = ? AND revoked_at = ''`), now, id)
	return err
}

func oneRow(res sql.Result, err error, none error) error {
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return none
	}
	return nil
}

// CreateAgentToken issues a token for an agent, valid for days. The token
// is returned once and stored only as its hash.
func (us *UserStore) CreateAgentToken(agentID, name, createdBy string, days int, onBehalfOf bool) (string, *AgentToken, error) {
	if days <= 0 {
		days = defaultAgentTokenDays
	}
	if days > maxAgentTokenDays {
		return "", nil, fmt.Errorf("a token lasts at most %d days", maxAgentTokenDays)
	}
	name = strings.TrimSpace(name)
	if name == "" || len(name) > 80 {
		return "", nil, fmt.Errorf("give the token a name (up to 80 characters) to recognize it by")
	}
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", nil, err
	}
	token := AgentTokenPrefix + hex.EncodeToString(buf)
	now := time.Now().UTC()
	t := &AgentToken{ID: generateID(), AgentID: agentID, Name: name, Prefix: token[:len(AgentTokenPrefix)+6],
		OnBehalfOf: onBehalfOf, CreatedBy: createdBy, CreatedAt: now, ExpiresAt: now.Add(time.Duration(days) * 24 * time.Hour)}
	behalf := 0
	if onBehalfOf {
		behalf = 1
	}
	if _, err := us.db.Exec(us.q(`INSERT INTO agent_tokens (id, agent_id, name, token_hash, prefix, on_behalf_of, created_by, created_at, expires_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`), t.ID, agentID, name, hashAgentToken(token), t.Prefix, behalf, createdBy,
		now.Format(time.RFC3339), t.ExpiresAt.Format(time.RFC3339)); err != nil {
		return "", nil, fmt.Errorf("record the token: %w", err)
	}
	return token, t, nil
}

// ListAgentTokens returns an agent's tokens, newest first; revoked and
// expired ones included, so their history stays visible.
func (us *UserStore) ListAgentTokens(agentID string) ([]AgentToken, error) {
	rows, err := us.db.Query(us.q(`SELECT id, agent_id, name, prefix, on_behalf_of, created_by, created_at, expires_at, last_used_at, revoked_at
		FROM agent_tokens WHERE agent_id = ? ORDER BY created_at DESC, id DESC`), agentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []AgentToken{}
	for rows.Next() {
		var t AgentToken
		var behalf int
		var created, expires, used, revoked string
		if err := rows.Scan(&t.ID, &t.AgentID, &t.Name, &t.Prefix, &behalf, &t.CreatedBy, &created, &expires, &used, &revoked); err != nil {
			return nil, err
		}
		t.OnBehalfOf = behalf == 1
		t.CreatedAt, _ = time.Parse(time.RFC3339, created)
		t.ExpiresAt, _ = time.Parse(time.RFC3339, expires)
		t.LastUsedAt, t.RevokedAt = parseStoredTime(used), parseStoredTime(revoked)
		out = append(out, t)
	}
	return out, rows.Err()
}

// RevokeAgentToken revokes one of an agent's tokens.
func (us *UserStore) RevokeAgentToken(agentID, tokenID string) error {
	res, err := us.db.Exec(us.q(`UPDATE agent_tokens SET revoked_at = ? WHERE id = ? AND agent_id = ? AND revoked_at = ''`),
		time.Now().UTC().Format(time.RFC3339), tokenID, agentID)
	return oneRow(res, err, ErrTokenNotFound)
}

// lastUsedEvery bounds how often a token's last use is written: once a
// minute is enough to answer "is this token in use", without a write on
// every request.
const lastUsedEvery = time.Minute

// ResolveAgentToken returns the claims for an agent token, or false when
// it is not a live token of an enabled agent whose owner still exists.
func (us *UserStore) ResolveAgentToken(token string) (*jwt.MapClaims, bool) {
	if !AgentIdentitiesEnabled || !strings.HasPrefix(token, AgentTokenPrefix) {
		return nil, false
	}
	var tokenID, tokenName, expires, revoked, used string
	var behalf int
	var agentID, agentName, agentRole, ownerID, disabled string
	var ownerName, ownerDisplay, ownerRole, ownerKind, ownerDisabled sql.NullString
	err := us.db.QueryRow(us.q(`SELECT t.id, t.name, t.expires_at, t.revoked_at, t.last_used_at, t.on_behalf_of,
			a.id, a.username, a.role, a.owner_id, a.disabled_at,
			o.username, o.display_name, o.role, o.kind, o.disabled_at
		FROM agent_tokens t
		JOIN users a ON a.id = t.agent_id AND a.kind = 'agent'
		LEFT JOIN users o ON o.id = a.owner_id
		WHERE t.token_hash = ?`), hashAgentToken(token)).Scan(&tokenID, &tokenName, &expires, &revoked, &used, &behalf,
		&agentID, &agentName, &agentRole, &ownerID, &disabled, &ownerName, &ownerDisplay, &ownerRole, &ownerKind, &ownerDisabled)
	if err != nil {
		return nil, false
	}
	now := time.Now().UTC()
	exp, err := time.Parse(time.RFC3339, expires)
	switch {
	case err != nil || !now.Before(exp):
		return nil, false
	case revoked != "" || disabled != "":
		return nil, false
	case !ownerName.Valid || ownerKind.String != UserKindPerson || ownerDisabled.String != "":
		return nil, false // the person answering for it is gone
	}
	if last := parseStoredTime(used); last == nil || now.Sub(*last) >= lastUsedEvery {
		_, _ = us.db.Exec(us.q(`UPDATE agent_tokens SET last_used_at = ? WHERE id = ?`), now.Format(time.RFC3339), tokenID)
	}
	claims := agentClaims(agentID, agentName, agentRole, ownerID, ownerRole.String)
	claims["token_id"] = tokenID
	claims["token_name"] = tokenName
	claims["exp"] = exp.Unix()
	if behalf == 1 {
		name := ownerDisplay.String
		if name == "" {
			name = ownerName.String
		}
		claims["on_behalf_of"] = name
	}
	return &claims, true
}

// agentClaims is who an agent is, for any credential that acts as it.
func agentClaims(agentID, agentName, agentRole, ownerID, ownerRole string) jwt.MapClaims {
	claims := jwt.MapClaims{
		"sub":        agentID,
		"username":   agentName,
		"role":       agentRole,
		"kind":       UserKindAgent,
		"owner_id":   ownerID,
		"owner_role": ownerRole,
	}
	if OrgResolverFunc != nil {
		if orgID := OrgResolverFunc(agentID); orgID != "" {
			claims["org_id"] = orgID
		}
	}
	return claims
}

// isAgentClaims reports whether claims are an agent's.
func isAgentClaims(claims *jwt.MapClaims) bool {
	if claims == nil {
		return false
	}
	kind, _ := (*claims)["kind"].(string)
	return kind == UserKindAgent
}

// agentPermissions narrows perms for an agent: only what its owner also
// holds, and never what no agent may hold.
func agentPermissions(perms, owner []models.Permission) []models.Permission {
	held := make(map[models.Permission]bool, len(owner))
	for _, p := range owner {
		held[p] = true
	}
	out := make([]models.Permission, 0, len(perms))
	for _, p := range perms {
		if held[p] && !agentNeverPermissions[p] {
			out = append(out, p)
		}
	}
	return out
}

// assignableAgentRole reports whether an agent may hold role: any role but
// the two that operate the instance, which come with powers (user and role
// management, every tenant) the permission list does not fully describe.
func assignableAgentRole(role string) bool {
	return role != "" && role != string(RoleAdmin) && role != string(RoleSuperAdmin)
}

// kindOf is a user's kind, or "" when there is no such user.
func (us *UserStore) kindOf(id string) string {
	var kind, disabled string
	if err := us.db.QueryRow(us.q(`SELECT kind, disabled_at FROM users WHERE id = ?`), id).Scan(&kind, &disabled); err != nil {
		return ""
	}
	if disabled != "" {
		return ""
	}
	return kind
}
