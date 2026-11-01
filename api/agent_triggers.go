package api

import (
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/Tnsor-Labs/brokoli/crypto"
	"github.com/Tnsor-Labs/brokoli/store"
)

// Agent triggers (ADR-047): an agent identity subscribed to run events.
// This file is the record of them; delivering the events is ADR-047's
// dispatcher, on the scheduler's leader.

// The events a trigger may subscribe to.
const (
	TriggerEventRunFailed    = "run.failed"
	TriggerEventRunBlocked   = "run.blocked"
	TriggerEventRunRecovered = "run.recovered"
)

// TriggerEventType is an event a trigger may subscribe to, with what it
// means, for whoever chooses (the UI lists these).
type TriggerEventType struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	// SuggestedTool is the MCP tool an agent should start with; for an
	// added type it defaults to get_pipeline.
	SuggestedTool string `json:"suggested_tool,omitempty"`
}

var (
	triggerEventsMu   sync.RWMutex
	triggerEventTypes = []TriggerEventType{
		{TriggerEventRunFailed, "A run failed, including when a quality check or contract gate stopped it.", "explain_run"},
		{TriggerEventRunBlocked, "A run did not start because its dependencies were not satisfied.", "get_lineage"},
		{TriggerEventRunRecovered, "A run succeeded after the previous run of the same pipeline did not.", "list_runs"},
	}
	triggerEventName = regexp.MustCompile(`^[a-z][a-z0-9_]*(\.[a-z][a-z0-9_]*)+$`)
)

// RegisterTriggerEvent adds an event type that a distribution raises
// (ADR-047 section 2), so triggers may subscribe to it. Call it while the
// program starts, as mcpserver.Register is called; a name that is not
// dotted lower case, or is already taken, is a programming error.
func RegisterTriggerEvent(t TriggerEventType) {
	if !triggerEventName.MatchString(t.Name) || t.Description == "" {
		panic(fmt.Sprintf("agent triggers: event type %q needs a dotted lower-case name and a description", t.Name))
	}
	triggerEventsMu.Lock()
	defer triggerEventsMu.Unlock()
	for _, e := range triggerEventTypes {
		if e.Name == t.Name {
			panic(fmt.Sprintf("agent triggers: event type %q is registered twice", t.Name))
		}
	}
	triggerEventTypes = append(triggerEventTypes, t)
}

// TriggerEventTypes is every event type a trigger may subscribe to, the
// built-in ones first.
func TriggerEventTypes() []TriggerEventType {
	triggerEventsMu.RLock()
	defer triggerEventsMu.RUnlock()
	return append([]TriggerEventType(nil), triggerEventTypes...)
}

func triggerEventNames() []string {
	types := TriggerEventTypes()
	out := make([]string, len(types))
	for i, t := range types {
		out[i] = t.Name
	}
	return out
}

const (
	// defaultTriggerCooldown is how long a trigger stays quiet about a
	// pipeline after it delivered an event of the same type about it
	// (ADR-047 section 1): a failure does not hide the recovery after it.
	defaultTriggerCooldown = 30 * time.Minute
	maxTriggerCooldown     = 7 * 24 * time.Hour
	// previousSecretOverlap is how long a rotated-out signing secret keeps
	// signing alongside the new one, so a receiver can switch without
	// dropping deliveries.
	previousSecretOverlap = 24 * time.Hour
	// triggerSecretPrefix marks a Standard Webhooks signing secret; the
	// key is the base64 after it.
	triggerSecretPrefix = "whsec_"
)

// ErrTriggerNotFound is a trigger that does not exist, or is not the
// caller's to see.
var ErrTriggerNotFound = errors.New("trigger not found")

// ErrNoSecret is a feed trigger asked for a signing secret: nothing it
// delivers is signed.
var ErrNoSecret = errors.New("a feed trigger has no signing secret: its agent reads the feed with its own token")

// How a trigger delivers.
const (
	DeliveryWebhook = "webhook"
	DeliveryFeed    = "feed"
)

// AgentTrigger subscribes an agent to run events in one workspace. The
// signing secret is never part of it: it is shown once, when it is made.
type AgentTrigger struct {
	ID          string `json:"id"`
	AgentID     string `json:"agent_id"`
	WorkspaceID string `json:"workspace_id"`
	// PipelineID is the one pipeline it watches; empty watches every
	// pipeline in the workspace the agent may see.
	PipelineID string   `json:"pipeline_id,omitempty"`
	Events     []string `json:"events"`
	// Delivery is how events reach the agent: "webhook", posted to URL and
	// signed, or "feed", which the agent reads with its own token
	// (GET /api/agents/me/events) and which has no URL and no secret.
	Delivery        string     `json:"delivery"`
	URL             string     `json:"url"`
	CooldownSeconds int        `json:"cooldown_seconds"`
	Enabled         bool       `json:"enabled"`
	CreatedBy       string     `json:"created_by"`
	CreatedAt       time.Time  `json:"created_at"`
	UpdatedAt       time.Time  `json:"updated_at"`
	SecretRotatedAt *time.Time `json:"secret_rotated_at,omitempty"`
}

// Watches reports whether the trigger wants event for pipelineID.
func (t *AgentTrigger) Watches(event, pipelineID string) bool {
	if !t.Enabled || (t.PipelineID != "" && t.PipelineID != pipelineID) {
		return false
	}
	for _, e := range t.Events {
		if e == event {
			return true
		}
	}
	return false
}

// TriggerStore keeps triggers next to the agents they belong to.
type TriggerStore struct {
	db      *sql.DB
	dialect string
	crypto  *crypto.Config
}

// NewTriggerStore creates the store and its table. Secrets are encrypted
// with c, the key that protects connection credentials; without a usable
// key a trigger cannot be made.
func NewTriggerStore(db *sql.DB, c *crypto.Config) (*TriggerStore, error) {
	ts := &TriggerStore{db: db, dialect: detectUserStoreDialect(db), crypto: c}
	create := func() error {
		if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS agent_triggers (
		id TEXT PRIMARY KEY,
		agent_id TEXT NOT NULL,
		workspace_id TEXT NOT NULL,
		pipeline_id TEXT NOT NULL DEFAULT '',
		events TEXT NOT NULL,
		delivery TEXT NOT NULL DEFAULT 'webhook',
		url TEXT NOT NULL DEFAULT '',
		secret TEXT NOT NULL DEFAULT '',
		previous_secret TEXT NOT NULL DEFAULT '',
		previous_secret_until TEXT NOT NULL DEFAULT '',
		cooldown_seconds INTEGER NOT NULL,
		enabled INTEGER NOT NULL DEFAULT 1,
		created_by TEXT NOT NULL,
		created_at TEXT NOT NULL,
		updated_at TEXT NOT NULL,
		secret_rotated_at TEXT NOT NULL DEFAULT ''
	)`); err != nil {
			return fmt.Errorf("create agent_triggers: %w", err)
		}
		if _, err := db.Exec(`CREATE INDEX IF NOT EXISTS idx_agent_triggers_agent ON agent_triggers (agent_id)`); err != nil {
			return err
		}
		return createDeliveryTables(db)
	}
	var err error
	if ts.dialect == "postgres" {
		err = store.WithMigrationLock(db, create)
	} else {
		err = create()
	}
	return ts, err
}

func (ts *TriggerStore) q(query string) string { return dialectQuery(ts.dialect, query) }

// newTriggerSecret is a Standard Webhooks signing secret: whsec_ and 32
// random bytes in base64.
func newTriggerSecret() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return triggerSecretPrefix + base64.StdEncoding.EncodeToString(b), nil
}

func (ts *TriggerStore) seal(secret string) (string, error) {
	if ts.crypto == nil || len(ts.crypto.Key) != 32 {
		return "", errors.New("triggers need the server's encryption key, which is not configured")
	}
	return ts.crypto.Encrypt(secret)
}

// Create records t and returns its signing secret: the only time it
// leaves the server. A feed trigger has none: its agent reads the feed
// with its own token, so nothing is signed.
func (ts *TriggerStore) Create(t *AgentTrigger) (string, error) {
	if t.Delivery == "" {
		t.Delivery = DeliveryWebhook
	}
	var secret, sealed string
	if t.Delivery == DeliveryWebhook {
		var err error
		if secret, err = newTriggerSecret(); err != nil {
			return "", err
		}
		if sealed, err = ts.seal(secret); err != nil {
			return "", err
		}
	}
	events, _ := json.Marshal(t.Events)
	now := time.Now().UTC()
	t.ID, t.CreatedAt, t.UpdatedAt = generateID(), now, now
	_, err := ts.db.Exec(ts.q(`INSERT INTO agent_triggers (id, agent_id, workspace_id, pipeline_id, events, delivery, url, secret,
		cooldown_seconds, enabled, created_by, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`),
		t.ID, t.AgentID, t.WorkspaceID, t.PipelineID, string(events), t.Delivery, t.URL, sealed,
		t.CooldownSeconds, boolInt(t.Enabled), t.CreatedBy, now.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano))
	if err != nil {
		return "", err
	}
	return secret, nil
}

const triggerColumns = `id, agent_id, workspace_id, pipeline_id, events, delivery, url, cooldown_seconds, enabled,
	created_by, created_at, updated_at, secret_rotated_at`

func scanTrigger(row interface{ Scan(...any) error }) (*AgentTrigger, error) {
	var t AgentTrigger
	var events, created, updated, rotated string
	var enabled int
	if err := row.Scan(&t.ID, &t.AgentID, &t.WorkspaceID, &t.PipelineID, &events, &t.Delivery, &t.URL,
		&t.CooldownSeconds, &enabled, &t.CreatedBy, &created, &updated, &rotated); err != nil {
		return nil, err
	}
	if err := json.Unmarshal([]byte(events), &t.Events); err != nil {
		return nil, fmt.Errorf("trigger %s events: %w", t.ID, err)
	}
	t.Enabled = enabled != 0
	t.CreatedAt, _ = time.Parse(time.RFC3339Nano, created)
	t.UpdatedAt, _ = time.Parse(time.RFC3339Nano, updated)
	if rotated != "" {
		if at, err := time.Parse(time.RFC3339Nano, rotated); err == nil {
			t.SecretRotatedAt = &at
		}
	}
	return &t, nil
}

// Get is one trigger of agentID.
func (ts *TriggerStore) Get(agentID, id string) (*AgentTrigger, error) {
	t, err := scanTrigger(ts.db.QueryRow(ts.q(`SELECT `+triggerColumns+` FROM agent_triggers WHERE id = ? AND agent_id = ?`), id, agentID))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrTriggerNotFound
	}
	return t, err
}

// List is agentID's triggers in workspaceID, oldest first; an empty
// agentID lists every enabled trigger, for the dispatcher.
func (ts *TriggerStore) List(agentID, workspaceID string) ([]AgentTrigger, error) {
	query, args := `SELECT `+triggerColumns+` FROM agent_triggers WHERE enabled = 1`, []any{}
	if agentID != "" {
		query, args = `SELECT `+triggerColumns+` FROM agent_triggers WHERE agent_id = ? AND workspace_id = ?`, []any{agentID, workspaceID}
	}
	rows, err := ts.db.Query(ts.q(query+` ORDER BY created_at, id`), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []AgentTrigger{}
	for rows.Next() {
		t, err := scanTrigger(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *t)
	}
	return out, rows.Err()
}

// ListWorkspace is every trigger in a workspace, enabled or not.
func (ts *TriggerStore) ListWorkspace(workspaceID string) ([]AgentTrigger, error) {
	all, err := ts.listAll()
	if err != nil {
		return nil, err
	}
	out := []AgentTrigger{}
	for _, t := range all {
		if sameWorkspace(t.WorkspaceID, workspaceID) {
			out = append(out, t)
		}
	}
	return out, nil
}

func (ts *TriggerStore) listAll() ([]AgentTrigger, error) {
	rows, err := ts.db.Query(ts.q(`SELECT ` + triggerColumns + ` FROM agent_triggers ORDER BY created_at, id`))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []AgentTrigger{}
	for rows.Next() {
		t, err := scanTrigger(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *t)
	}
	return out, rows.Err()
}

// Update writes t's events, URL, pipeline, cooldown and enabled flag.
func (ts *TriggerStore) Update(t *AgentTrigger) error {
	events, _ := json.Marshal(t.Events)
	t.UpdatedAt = time.Now().UTC()
	res, err := ts.db.Exec(ts.q(`UPDATE agent_triggers SET pipeline_id = ?, events = ?, url = ?, cooldown_seconds = ?, enabled = ?,
		updated_at = ? WHERE id = ? AND agent_id = ?`),
		t.PipelineID, string(events), t.URL, t.CooldownSeconds, boolInt(t.Enabled), t.UpdatedAt.Format(time.RFC3339Nano), t.ID, t.AgentID)
	return oneRow(res, err, ErrTriggerNotFound)
}

// Delete removes a trigger.
func (ts *TriggerStore) Delete(agentID, id string) error {
	res, err := ts.db.Exec(ts.q(`DELETE FROM agent_triggers WHERE id = ? AND agent_id = ?`), id, agentID)
	return oneRow(res, err, ErrTriggerNotFound)
}

// RotateSecret gives a trigger a new signing secret and returns it. The
// old one keeps signing alongside it for previousSecretOverlap.
func (ts *TriggerStore) RotateSecret(agentID, id string) (string, error) {
	var current, delivery string
	if err := ts.db.QueryRow(ts.q(`SELECT secret, delivery FROM agent_triggers WHERE id = ? AND agent_id = ?`), id, agentID).
		Scan(&current, &delivery); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", ErrTriggerNotFound
		}
		return "", err
	}
	if delivery != DeliveryWebhook {
		return "", ErrNoSecret
	}
	secret, err := newTriggerSecret()
	if err != nil {
		return "", err
	}
	sealed, err := ts.seal(secret)
	if err != nil {
		return "", err
	}
	now := time.Now().UTC()
	res, err := ts.db.Exec(ts.q(`UPDATE agent_triggers SET secret = ?, previous_secret = ?, previous_secret_until = ?,
		secret_rotated_at = ?, updated_at = ? WHERE id = ? AND agent_id = ?`),
		sealed, current, now.Add(previousSecretOverlap).Format(time.RFC3339Nano), now.Format(time.RFC3339Nano),
		now.Format(time.RFC3339Nano), id, agentID)
	if err := oneRow(res, err, ErrTriggerNotFound); err != nil {
		return "", err
	}
	return secret, nil
}

// SigningSecrets is what signs a delivery for trigger id now: its secret,
// then the previous one while the rotation overlap lasts.
func (ts *TriggerStore) SigningSecrets(id string, now time.Time) ([]string, error) {
	var current, previous, until string
	if err := ts.db.QueryRow(ts.q(`SELECT secret, previous_secret, previous_secret_until FROM agent_triggers WHERE id = ?`), id).
		Scan(&current, &previous, &until); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrTriggerNotFound
		}
		return nil, err
	}
	sealed := []string{current}
	if previous != "" {
		if end, err := time.Parse(time.RFC3339Nano, until); err == nil && now.Before(end) {
			sealed = append(sealed, previous)
		}
	}
	out := make([]string, 0, len(sealed))
	for _, s := range sealed {
		plain, err := ts.crypto.Decrypt(s)
		if err != nil {
			return nil, fmt.Errorf("trigger %s: decrypt signing secret: %w", id, err)
		}
		out = append(out, plain)
	}
	return out, nil
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// validTriggerEvents is why events cannot be a trigger's, or "".
func validTriggerEvents(events []string) string {
	if len(events) == 0 {
		return "a trigger needs at least one event: " + strings.Join(triggerEventNames(), ", ")
	}
	known := map[string]bool{}
	for _, n := range triggerEventNames() {
		known[n] = true
	}
	seen := map[string]bool{}
	for _, e := range events {
		if !known[e] {
			return fmt.Sprintf("unknown event %q; a trigger may subscribe to %s", e, strings.Join(triggerEventNames(), ", "))
		}
		if seen[e] {
			return fmt.Sprintf("event %q is listed twice", e)
		}
		seen[e] = true
	}
	return ""
}
