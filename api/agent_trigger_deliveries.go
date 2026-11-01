package api

import (
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"time"
)

// Deliveries (ADR-047 section 3): one row per event matched to a trigger,
// sent at least once, every attempt kept. Timestamps here are compared as
// text ("is it due", "is it within the cooldown"), so they are written at
// a fixed width: RFC3339Nano drops trailing zeros, and "...:05Z" would
// sort after "...:05.5Z".
const triggerTimeFormat = "2006-01-02T15:04:05.000000000Z"

func fixedTime(t time.Time) string { return t.UTC().Format(triggerTimeFormat) }

func parseFixedTime(s string) time.Time {
	t, _ := time.Parse(triggerTimeFormat, s)
	return t
}

// newDeliveryID is a delivery id that sorts in the order deliveries are
// made: deliveries recorded in one matching pass share a timestamp, and
// the feed reads by (timestamp, id), so a random id could put a later
// delivery before a cursor an agent already passed, where it would never
// be read.
func newDeliveryID() string {
	for {
		last := lastDeliveryNano.Load()
		next := time.Now().UnixNano()
		if next <= last {
			next = last + 1
		}
		if lastDeliveryNano.CompareAndSwap(last, next) {
			return fmt.Sprintf("dlv_%016x%s", next, generateID()[:12])
		}
	}
}

var lastDeliveryNano atomic.Int64

// Delivery states.
const (
	DeliveryPending   = "pending"
	DeliveryDelivered = "delivered"
	DeliveryFailed    = "failed"
	DeliverySkipped   = "skipped"
	// DeliveryExpired is a feed delivery its agent did not read in time.
	DeliveryExpired = "expired"
)

// feedRetained is how long a feed delivery waits to be read.
const feedRetained = 7 * 24 * time.Hour

// ErrDeliveryNotFound is a delivery that does not exist, or is not the
// trigger's.
var ErrDeliveryNotFound = errors.New("delivery not found")

// TriggerDelivery is one event on its way to an agent.
type TriggerDelivery struct {
	ID          string `json:"id"`
	TriggerID   string `json:"trigger_id"`
	AgentID     string `json:"agent_id"`
	WorkspaceID string `json:"workspace_id"`
	PipelineID  string `json:"pipeline_id"`
	RunID       string `json:"run_id"`
	Event       string `json:"event"`
	Status      string `json:"status"`
	Attempts    int    `json:"attempts"`
	LastStatus  int    `json:"last_status,omitempty"`
	LastError   string `json:"last_error,omitempty"`
	// Reason is why a delivery was skipped or given up on.
	Reason      string            `json:"reason,omitempty"`
	CreatedAt   time.Time         `json:"created_at"`
	NextAttempt *time.Time        `json:"next_attempt_at,omitempty"`
	FinishedAt  *time.Time        `json:"finished_at,omitempty"`
	History     []DeliveryAttempt `json:"attempts_log,omitempty"`

	payload     string
	windowStart time.Time
}

// DeliveryAttempt is one try at sending a delivery.
type DeliveryAttempt struct {
	At         time.Time `json:"at"`
	StatusCode int       `json:"status_code,omitempty"`
	DurationMs int64     `json:"duration_ms"`
	Error      string    `json:"error,omitempty"`
	Response   string    `json:"response,omitempty"`
}

func createDeliveryTables(db *sql.DB) error {
	for _, stmt := range []string{
		`CREATE TABLE IF NOT EXISTS agent_trigger_deliveries (
			id TEXT PRIMARY KEY,
			trigger_id TEXT NOT NULL,
			agent_id TEXT NOT NULL,
			workspace_id TEXT NOT NULL,
			pipeline_id TEXT NOT NULL,
			run_id TEXT NOT NULL,
			event TEXT NOT NULL,
			payload TEXT NOT NULL,
			status TEXT NOT NULL,
			attempts INTEGER NOT NULL DEFAULT 0,
			next_attempt_at TEXT NOT NULL DEFAULT '',
			window_start TEXT NOT NULL,
			last_status INTEGER NOT NULL DEFAULT 0,
			last_error TEXT NOT NULL DEFAULT '',
			reason TEXT NOT NULL DEFAULT '',
			created_at TEXT NOT NULL,
			finished_at TEXT NOT NULL DEFAULT ''
		)`,
		// One delivery per trigger, run and event, however often the log
		// is re-read.
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_trigger_deliveries_once ON agent_trigger_deliveries (trigger_id, run_id, event)`,
		`CREATE INDEX IF NOT EXISTS idx_trigger_deliveries_due ON agent_trigger_deliveries (status, next_attempt_at)`,
		`CREATE INDEX IF NOT EXISTS idx_trigger_deliveries_recent ON agent_trigger_deliveries (trigger_id, pipeline_id, created_at)`,
		`CREATE INDEX IF NOT EXISTS idx_trigger_deliveries_run ON agent_trigger_deliveries (run_id)`,
		`CREATE TABLE IF NOT EXISTS agent_trigger_attempts (
			id TEXT PRIMARY KEY,
			delivery_id TEXT NOT NULL,
			at TEXT NOT NULL,
			status_code INTEGER NOT NULL DEFAULT 0,
			duration_ms INTEGER NOT NULL DEFAULT 0,
			error TEXT NOT NULL DEFAULT '',
			response TEXT NOT NULL DEFAULT ''
		)`,
		`CREATE INDEX IF NOT EXISTS idx_trigger_attempts_delivery ON agent_trigger_attempts (delivery_id, at)`,
		// Where the matcher has read the run event log to.
		`CREATE TABLE IF NOT EXISTS agent_trigger_cursor (
			name TEXT PRIMARY KEY,
			last_event_id BIGINT NOT NULL
		)`,
	} {
		if _, err := db.Exec(stmt); err != nil {
			return fmt.Errorf("create trigger delivery tables: %w", err)
		}
	}
	return nil
}

const cursorName = "run_events"

// Cursor is the id of the last run event the matcher read.
func (ts *TriggerStore) Cursor() (int64, error) {
	var id int64
	err := ts.db.QueryRow(ts.q(`SELECT last_event_id FROM agent_trigger_cursor WHERE name = ?`), cursorName).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	return id, err
}

// SetCursor moves the cursor forward to id; it never moves back.
func (ts *TriggerStore) SetCursor(id int64) error {
	_, err := ts.db.Exec(ts.q(`INSERT INTO agent_trigger_cursor (name, last_event_id) VALUES (?, ?)
		ON CONFLICT (name) DO UPDATE SET last_event_id = CASE WHEN excluded.last_event_id > agent_trigger_cursor.last_event_id
		THEN excluded.last_event_id ELSE agent_trigger_cursor.last_event_id END`), cursorName, id)
	return err
}

// Enqueue records a delivery unless the trigger already has one for this
// run and event, or delivered the same event about this pipeline within
// cooldown. Test events do not count: a real failure right after someone
// tested the receiver is still delivered. It reports whether it recorded
// one.
func (ts *TriggerStore) Enqueue(d *TriggerDelivery, payload string, cooldown time.Duration, now time.Time) (bool, error) {
	if cooldown > 0 {
		var recent int
		if err := ts.db.QueryRow(ts.q(`SELECT COUNT(*) FROM agent_trigger_deliveries
			WHERE trigger_id = ? AND pipeline_id = ? AND event = ? AND created_at > ? AND run_id <> ?
			AND run_id NOT LIKE 'test-%'`),
			d.TriggerID, d.PipelineID, d.Event, fixedTime(now.Add(-cooldown)), d.RunID).Scan(&recent); err != nil {
			return false, err
		}
		if recent > 0 {
			return false, nil
		}
	}
	res, err := ts.db.Exec(ts.q(`INSERT INTO agent_trigger_deliveries (id, trigger_id, agent_id, workspace_id, pipeline_id, run_id,
		event, payload, status, next_attempt_at, window_start, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?) ON CONFLICT (trigger_id, run_id, event) DO NOTHING`),
		d.ID, d.TriggerID, d.AgentID, d.WorkspaceID, d.PipelineID, d.RunID, d.Event, payload, DeliveryPending,
		fixedTime(now), fixedTime(now), fixedTime(now))
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

const deliveryColumns = `id, trigger_id, agent_id, workspace_id, pipeline_id, run_id, event, payload, status, attempts,
	next_attempt_at, window_start, last_status, last_error, reason, created_at, finished_at`

func scanDelivery(row interface{ Scan(...any) error }) (*TriggerDelivery, error) {
	var d TriggerDelivery
	var next, window, created, finished string
	if err := row.Scan(&d.ID, &d.TriggerID, &d.AgentID, &d.WorkspaceID, &d.PipelineID, &d.RunID, &d.Event, &d.payload,
		&d.Status, &d.Attempts, &next, &window, &d.LastStatus, &d.LastError, &d.Reason, &created, &finished); err != nil {
		return nil, err
	}
	d.CreatedAt, d.windowStart = parseFixedTime(created), parseFixedTime(window)
	if next != "" && d.Status == DeliveryPending {
		t := parseFixedTime(next)
		d.NextAttempt = &t
	}
	if finished != "" {
		t := parseFixedTime(finished)
		d.FinishedAt = &t
	}
	return &d, nil
}

// Due is what is waiting to be sent at now, oldest first. Feed deliveries
// are never sent: they wait for their agent to read them.
func (ts *TriggerStore) Due(now time.Time, limit int) ([]TriggerDelivery, error) {
	rows, err := ts.db.Query(ts.q(`SELECT `+deliveryColumns+` FROM agent_trigger_deliveries
		WHERE status = ? AND next_attempt_at <= ?
		AND trigger_id IN (SELECT id FROM agent_triggers WHERE delivery = ?)
		ORDER BY next_attempt_at, id LIMIT ?`), DeliveryPending, fixedTime(now), DeliveryWebhook, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []TriggerDelivery
	for rows.Next() {
		d, err := scanDelivery(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *d)
	}
	return out, rows.Err()
}

// RecordAttempt keeps one try and moves the delivery to its next state:
// finished when status is not pending, otherwise due again at next.
func (ts *TriggerStore) RecordAttempt(d *TriggerDelivery, a DeliveryAttempt, status, reason string, next time.Time) error {
	if _, err := ts.db.Exec(ts.q(`INSERT INTO agent_trigger_attempts (id, delivery_id, at, status_code, duration_ms, error, response)
		VALUES (?, ?, ?, ?, ?, ?, ?)`), generateID(), d.ID, fixedTime(a.At), a.StatusCode, a.DurationMs, a.Error, a.Response); err != nil {
		return err
	}
	finished, nextAt := "", fixedTime(next)
	if status != DeliveryPending {
		finished, nextAt = fixedTime(a.At), ""
	}
	_, err := ts.db.Exec(ts.q(`UPDATE agent_trigger_deliveries SET status = ?, attempts = attempts + 1, last_status = ?, last_error = ?,
		reason = ?, next_attempt_at = ?, finished_at = ? WHERE id = ?`), status, a.StatusCode, a.Error, reason, nextAt, finished, d.ID)
	return err
}

// Skip finishes a delivery without sending it.
func (ts *TriggerStore) Skip(d *TriggerDelivery, reason string, now time.Time) error {
	_, err := ts.db.Exec(ts.q(`UPDATE agent_trigger_deliveries SET status = ?, reason = ?, next_attempt_at = '', finished_at = ? WHERE id = ?`),
		DeliverySkipped, reason, fixedTime(now), d.ID)
	return err
}

// Deliveries is a trigger's newest deliveries, each with its attempts.
func (ts *TriggerStore) Deliveries(triggerID string, limit int) ([]TriggerDelivery, error) {
	out, err := ts.newestDeliveries(triggerID, limit)
	if err != nil {
		return nil, err
	}
	for i := range out {
		if out[i].History, err = ts.attempts(out[i].ID); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// RunDeliveries is every delivery made about a run, oldest first.
func (ts *TriggerStore) RunDeliveries(runID string) ([]TriggerDelivery, error) {
	rows, err := ts.db.Query(ts.q(`SELECT `+deliveryColumns+` FROM agent_trigger_deliveries WHERE run_id = ?
		ORDER BY created_at, id`), runID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []TriggerDelivery{}
	for rows.Next() {
		d, err := scanDelivery(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *d)
	}
	return out, rows.Err()
}

// newestDeliveries reads the rows and closes them before the attempts are
// read, so one connection is enough.
func (ts *TriggerStore) newestDeliveries(triggerID string, limit int) ([]TriggerDelivery, error) {
	rows, err := ts.db.Query(ts.q(`SELECT `+deliveryColumns+` FROM agent_trigger_deliveries WHERE trigger_id = ?
		ORDER BY created_at DESC, id DESC LIMIT ?`), triggerID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []TriggerDelivery
	for rows.Next() {
		d, err := scanDelivery(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *d)
	}
	return out, rows.Err()
}

func (ts *TriggerStore) attempts(deliveryID string) ([]DeliveryAttempt, error) {
	rows, err := ts.db.Query(ts.q(`SELECT at, status_code, duration_ms, error, response FROM agent_trigger_attempts
		WHERE delivery_id = ? ORDER BY at`), deliveryID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []DeliveryAttempt
	for rows.Next() {
		var a DeliveryAttempt
		var at string
		if err := rows.Scan(&at, &a.StatusCode, &a.DurationMs, &a.Error, &a.Response); err != nil {
			return nil, err
		}
		a.At = parseFixedTime(at)
		out = append(out, a)
	}
	return out, rows.Err()
}

// Redeliver puts a finished delivery back in the queue, due now, with a
// fresh 24 hour window. Its id stays, so a receiver that already handled
// it can tell.
func (ts *TriggerStore) Redeliver(triggerID, id string, now time.Time) error {
	res, err := ts.db.Exec(ts.q(`UPDATE agent_trigger_deliveries SET status = ?, next_attempt_at = ?, window_start = ?, reason = '',
		finished_at = '' WHERE id = ? AND trigger_id = ?`), DeliveryPending, fixedTime(now), fixedTime(now), id, triggerID)
	return oneRow(res, err, ErrDeliveryNotFound)
}

// PruneDeliveries removes finished deliveries, and their attempts, older
// than before.
func (ts *TriggerStore) PruneDeliveries(before time.Time) error {
	cut := fixedTime(before)
	if _, err := ts.db.Exec(ts.q(`DELETE FROM agent_trigger_attempts WHERE delivery_id IN (
		SELECT id FROM agent_trigger_deliveries WHERE status <> ? AND created_at < ?)`), DeliveryPending, cut); err != nil {
		return err
	}
	_, err := ts.db.Exec(ts.q(`DELETE FROM agent_trigger_deliveries WHERE status <> ? AND created_at < ?`), DeliveryPending, cut)
	return err
}

// GetByID is a trigger by id alone, for the dispatcher.
func (ts *TriggerStore) GetByID(id string) (*AgentTrigger, error) {
	t, err := scanTrigger(ts.db.QueryRow(ts.q(`SELECT `+triggerColumns+` FROM agent_triggers WHERE id = ?`), id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrTriggerNotFound
	}
	return t, err
}

// FeedPosition is where a delivery sits in its agent's feed: deliveries
// are read in the order they were recorded.
type FeedPosition struct{ CreatedAt, ID string }

// String is the cursor an agent passes back.
func (p FeedPosition) String() string {
	if p.ID == "" {
		return ""
	}
	return base64.RawURLEncoding.EncodeToString([]byte(p.CreatedAt + "|" + p.ID))
}

// ParseFeedPosition reads a cursor; an empty one is the start of the feed.
func ParseFeedPosition(cursor string) (FeedPosition, error) {
	if cursor == "" {
		return FeedPosition{}, nil
	}
	raw, err := base64.RawURLEncoding.DecodeString(cursor)
	if err != nil {
		return FeedPosition{}, errors.New("not a cursor this server gave out")
	}
	created, id, ok := strings.Cut(string(raw), "|")
	if !ok || id == "" || parseFixedTime(created).IsZero() {
		return FeedPosition{}, errors.New("not a cursor this server gave out")
	}
	return FeedPosition{CreatedAt: created, ID: id}, nil
}

const feedDeliveries = `FROM agent_trigger_deliveries WHERE agent_id = ? AND status = ?
	AND trigger_id IN (SELECT id FROM agent_triggers WHERE delivery = ?)`

// FeedAcknowledge marks agentID's waiting feed deliveries up to and
// including upto as read.
func (ts *TriggerStore) FeedAcknowledge(agentID string, upto FeedPosition, now time.Time) error {
	if upto.ID == "" {
		return nil
	}
	_, err := ts.db.Exec(ts.q(`UPDATE agent_trigger_deliveries SET status = ?, attempts = attempts + 1, next_attempt_at = '',
		finished_at = ? WHERE agent_id = ? AND status = ? AND trigger_id IN (SELECT id FROM agent_triggers WHERE delivery = ?)
		AND (created_at < ? OR (created_at = ? AND id <= ?))`),
		DeliveryDelivered, fixedTime(now), agentID, DeliveryPending, DeliveryFeed, upto.CreatedAt, upto.CreatedAt, upto.ID)
	return err
}

// FeedPending is agentID's waiting feed deliveries after after, oldest
// first.
func (ts *TriggerStore) FeedPending(agentID string, after FeedPosition, limit int) ([]TriggerDelivery, error) {
	query, args := `SELECT `+deliveryColumns+` `+feedDeliveries, []any{agentID, DeliveryPending, DeliveryFeed}
	if after.ID != "" {
		query += ` AND (created_at > ? OR (created_at = ? AND id > ?))`
		args = append(args, after.CreatedAt, after.CreatedAt, after.ID)
	}
	rows, err := ts.db.Query(ts.q(query+` ORDER BY created_at, id LIMIT ?`), append(args, limit)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []TriggerDelivery
	for rows.Next() {
		d, err := scanDelivery(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *d)
	}
	return out, rows.Err()
}

// Position is where d sits in its feed.
func (d *TriggerDelivery) Position() FeedPosition {
	return FeedPosition{CreatedAt: fixedTime(d.CreatedAt), ID: d.ID}
}

// ExpireFeed marks feed deliveries still unread from before before as
// expired.
func (ts *TriggerStore) ExpireFeed(before time.Time) error {
	_, err := ts.db.Exec(ts.q(`UPDATE agent_trigger_deliveries SET status = ?, reason = ?, next_attempt_at = '', finished_at = ?
		WHERE status = ? AND created_at < ? AND trigger_id IN (SELECT id FROM agent_triggers WHERE delivery = ?)`),
		DeliveryExpired, "its agent did not read it within 7 days", fixedTime(time.Now()), DeliveryPending, fixedTime(before), DeliveryFeed)
	return err
}
