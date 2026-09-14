package store

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/Tnsor-Labs/brokoli/models"
)

// Incident ownership on alerts (brokoli-ee#242).
//
// The alert inbox had read and dismissed, both stored on the alert row
// and therefore shared by the whole organization: one person marking an
// alert read marked it read for everyone, and nobody could say "I am on
// this".
//
// Two changes, and they are deliberately different shapes.
//
// Assignment, acknowledgement and resolution are properties of the
// INCIDENT: exactly one person owns a failure at a time, and the whole
// organization should see the same answer. Those are columns on the
// alert.
//
// Read state is a property of the PERSON. It goes in its own table, so
// two people reading the same alert do not overwrite each other.

// migrateAlertIncidents is called from both dialects. Written once so a
// column cannot reach one path and not the other, which is how the
// enterprise invite feature shipped broken on Postgres.
func migrateAlertIncidents(db *sql.DB, dialect string) {
	ifNotExists := ""
	timestamp := "TEXT"
	if dialect == "postgres" {
		// SQLite has no ADD COLUMN IF NOT EXISTS and errors on a
		// duplicate, which is fine to ignore; Postgres has it and errors
		// on other things, which is not.
		ifNotExists = "IF NOT EXISTS "
		timestamp = "TIMESTAMPTZ"
	}
	for _, col := range []struct{ name, typ string }{
		{"assignee_user_id", "TEXT NOT NULL DEFAULT ''"},
		{"acknowledged_at", timestamp},
		{"acknowledged_by", "TEXT NOT NULL DEFAULT ''"},
		{"resolved_at", timestamp},
		{"resolved_by", "TEXT NOT NULL DEFAULT ''"},
	} {
		_, _ = db.Exec(fmt.Sprintf(`ALTER TABLE alerts ADD COLUMN %s%s %s`, ifNotExists, col.name, col.typ))
	}
	_, _ = db.Exec(`CREATE INDEX IF NOT EXISTS idx_alerts_assignee ON alerts(assignee_user_id)`)

	// Read state, per person. The alert's own read_at column stays: it is
	// what every alert written before this has, and dropping it would
	// mark the entire backlog unread for everyone at once.
	_, _ = db.Exec(`CREATE TABLE IF NOT EXISTS alert_reads (
		alert_id TEXT NOT NULL,
		user_id TEXT NOT NULL,
		read_at ` + timestamp + ` NOT NULL,
		PRIMARY KEY (alert_id, user_id)
	)`)
	_, _ = db.Exec(`CREATE INDEX IF NOT EXISTS idx_alert_reads_user ON alert_reads(user_id)`)
}

// ErrAlertNotFound is returned when an alert does not exist in the
// caller's organization. It does not distinguish "no such alert" from
// "belongs to another tenant": telling them apart would confirm the
// existence of another organization's alert.
var ErrAlertNotFound = errors.New("alert not found")

// AlertState is the incident lifecycle a caller can filter on.
type AlertState string

const (
	AlertStateOpen         AlertState = "open"
	AlertStateAcknowledged AlertState = "acknowledged"
	AlertStateResolved     AlertState = "resolved"
)

// ValidAlertState reports whether a filter value is one this build
// understands. An unrecognised value is refused rather than ignored: a
// filter that silently does nothing returns every alert, and the caller
// reads that as "these are the open ones".
func ValidAlertState(s AlertState) bool {
	switch s {
	case AlertStateOpen, AlertStateAcknowledged, AlertStateResolved:
		return true
	}
	return false
}

// AlertQuery is how a caller narrows the inbox.
type AlertQuery struct {
	OrgID string
	// UserID is who is asking. It decides read state, and it is what
	// AssigneeIsCaller resolves to.
	UserID string
	// State filters by incident lifecycle; empty means every state.
	State AlertState
	// AssigneeIsCaller restricts to alerts assigned to UserID. There is
	// deliberately no "assignee is this other person" option: that is a
	// different question with a different access rule.
	AssigneeIsCaller bool
	// AssigneeUserIDs restricts to a set of people, used by enterprise to
	// answer "this team's incidents" after resolving the team's members.
	// Empty means no restriction.
	AssigneeUserIDs []string
	UnreadOnly      bool
	Limit           int
}

// stateClause renders the SQL for a lifecycle state.
//
// Open means neither acknowledged nor resolved. Acknowledged means
// acknowledged and not yet resolved -- an incident somebody is on, which
// is what the word is for; leaving resolved ones in it would make the
// "who is working on what" list grow for ever.
func stateClause(s AlertState) string {
	switch s {
	case AlertStateOpen:
		return " AND acknowledged_at IS NULL AND resolved_at IS NULL"
	case AlertStateAcknowledged:
		return " AND acknowledged_at IS NOT NULL AND resolved_at IS NULL"
	case AlertStateResolved:
		return " AND resolved_at IS NOT NULL"
	}
	return ""
}

const alertIncidentColumns = `id, org_id, kind, severity, title, body, pipeline_id, pipeline_name, run_id,
	created_at, read_at, dismissed_at, assignee_user_id, acknowledged_at, acknowledged_by, resolved_at, resolved_by`

// queryAlerts is the shared read for both dialects.
func queryAlerts(db *sql.DB, dialect string, q AlertQuery) ([]models.Alert, error) {
	ph := nextPlaceholder(dialect)
	query := `SELECT ` + alertIncidentColumns + ` FROM alerts WHERE org_id = ` + ph() + ` AND dismissed_at IS NULL`
	args := []interface{}{q.OrgID}

	query += stateClause(q.State)

	if q.AssigneeIsCaller {
		query += " AND assignee_user_id = " + ph()
		args = append(args, q.UserID)
	}
	if len(q.AssigneeUserIDs) > 0 {
		marks := make([]string, len(q.AssigneeUserIDs))
		for i, id := range q.AssigneeUserIDs {
			marks[i] = ph()
			args = append(args, id)
		}
		query += " AND assignee_user_id IN (" + strings.Join(marks, ",") + ")"
	}
	query += " ORDER BY created_at DESC"
	if q.Limit > 0 {
		query += " LIMIT " + ph()
		args = append(args, q.Limit)
	}

	rows, err := db.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("list alerts: %w", err)
	}
	defer rows.Close()

	alerts := []models.Alert{}
	ids := []string{}
	for rows.Next() {
		a, err := scanIncidentAlert(rows, dialect)
		if err != nil {
			return nil, err
		}
		alerts = append(alerts, *a)
		ids = append(ids, a.ID)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// Read state, per person. One rule: this person's own row if there is
	// one, otherwise whatever the shared column says.
	//
	// The fallback is what makes the upgrade safe. MarkRead writes only
	// the per-person row now, so the shared column is only ever set by
	// marks made before this feature existed -- and those should stay
	// read for everyone, rather than a whole backlog turning unread on
	// the day of an upgrade.
	read, err := alertsReadBy(db, dialect, q.UserID, ids)
	if err != nil {
		return nil, err
	}
	out := alerts[:0]
	for i := range alerts {
		if t, ok := read[alerts[i].ID]; ok {
			readAt := t
			alerts[i].ReadAt = &readAt
		}
		if q.UnreadOnly && alerts[i].ReadAt != nil {
			continue
		}
		out = append(out, alerts[i])
	}
	return out, nil
}

// alertsReadBy returns when this person read each of these alerts.
func alertsReadBy(db *sql.DB, dialect, userID string, alertIDs []string) (map[string]time.Time, error) {
	out := map[string]time.Time{}
	if userID == "" || len(alertIDs) == 0 {
		return out, nil
	}
	ph := nextPlaceholder(dialect)
	marks := make([]string, len(alertIDs))
	args := []interface{}{userID}
	userMark := ph()
	for i, id := range alertIDs {
		marks[i] = ph()
		args = append(args, id)
	}
	rows, err := db.Query(
		`SELECT alert_id, read_at FROM alert_reads WHERE user_id = `+userMark+
			` AND alert_id IN (`+strings.Join(marks, ",")+`)`, args...)
	if err != nil {
		return nil, fmt.Errorf("read alert read-state: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		var readAt interface{}
		if err := rows.Scan(&id, &readAt); err != nil {
			return nil, err
		}
		if t, ok := parseAlertTime(readAt, dialect); ok {
			out[id] = t
		}
	}
	return out, rows.Err()
}

// parseAlertTime reads a timestamp back in whichever shape the dialect
// returns: Postgres hands back a time.Time, SQLite a string.
func parseAlertTime(v interface{}, dialect string) (time.Time, bool) {
	switch t := v.(type) {
	case nil:
		return time.Time{}, false
	case time.Time:
		return t, true
	case string:
		parsed, err := time.Parse(time.RFC3339Nano, t)
		if err != nil {
			return time.Time{}, false
		}
		return parsed, true
	case []byte:
		parsed, err := time.Parse(time.RFC3339Nano, string(t))
		if err != nil {
			return time.Time{}, false
		}
		return parsed, true
	}
	return time.Time{}, false
}

// scanIncidentAlert reads one alert row including its incident fields.
func scanIncidentAlert(sc interface{ Scan(...any) error }, dialect string) (*models.Alert, error) {
	var a models.Alert
	var createdAt, readAt, dismissedAt, ackAt, resolvedAt interface{}
	if err := sc.Scan(&a.ID, &a.OrgID, &a.Kind, &a.Severity, &a.Title, &a.Body,
		&a.PipelineID, &a.PipelineName, &a.RunID,
		&createdAt, &readAt, &dismissedAt,
		&a.AssigneeUserID, &ackAt, &a.AcknowledgedBy, &resolvedAt, &a.ResolvedBy); err != nil {
		return nil, fmt.Errorf("scan alert: %w", err)
	}
	if t, ok := parseAlertTime(createdAt, dialect); ok {
		a.CreatedAt = t
	}
	if t, ok := parseAlertTime(readAt, dialect); ok {
		a.ReadAt = &t
	}
	if t, ok := parseAlertTime(dismissedAt, dialect); ok {
		a.DismissedAt = &t
	}
	if t, ok := parseAlertTime(ackAt, dialect); ok {
		a.AcknowledgedAt = &t
	}
	if t, ok := parseAlertTime(resolvedAt, dialect); ok {
		a.ResolvedAt = &t
	}
	return &a, nil
}

// The three incident writes are spelled out as literal statements, one
// per operation and one per dialect, rather than assembled from
// fragments.
//
// There are only three, and a fixed string per case keeps the SQL
// obviously parameterized -- the same reasoning UserStore.SetProfile
// records for its own three cases. The assembled version read fine and
// still had to be checked by hand to see that nothing from a caller
// reached the statement; these cannot be read any other way.
const (
	sqlAssignAlert   = `UPDATE alerts SET assignee_user_id = ? WHERE id = ? AND org_id = ?`
	sqlAssignAlertPG = `UPDATE alerts SET assignee_user_id = $1 WHERE id = $2 AND org_id = $3`

	// Acknowledging also assigns, when nobody holds it. "I am on this"
	// and "nobody owns this" cannot both be true, and making the caller
	// press two buttons to say one thing is how an incident ends up
	// acknowledged and unowned. An existing assignment is left alone.
	sqlAckAlert = `UPDATE alerts SET acknowledged_at = ?, acknowledged_by = ?,
		assignee_user_id = CASE WHEN assignee_user_id = '' THEN ? ELSE assignee_user_id END
		WHERE id = ? AND org_id = ?`
	sqlAckAlertPG = `UPDATE alerts SET acknowledged_at = $1, acknowledged_by = $2,
		assignee_user_id = CASE WHEN assignee_user_id = '' THEN $3 ELSE assignee_user_id END
		WHERE id = $4 AND org_id = $5`

	sqlResolveAlert   = `UPDATE alerts SET resolved_at = ?, resolved_by = ? WHERE id = ? AND org_id = ?`
	sqlResolveAlertPG = `UPDATE alerts SET resolved_at = $1, resolved_by = $2 WHERE id = $3 AND org_id = $4`
)

// setAlertAssignee assigns or unassigns an alert. An empty userID
// unassigns.
func setAlertAssignee(db *sql.DB, dialect, orgID, alertID, userID string) error {
	query := sqlAssignAlert
	if dialect == "postgres" {
		query = sqlAssignAlertPG
	}
	return execAlertUpdate(db, alertID, query, userID, alertID, orgID)
}

// acknowledgeAlert records that somebody is on it.
func acknowledgeAlert(db *sql.DB, dialect, orgID, alertID, userID string, at time.Time) error {
	query := sqlAckAlert
	if dialect == "postgres" {
		query = sqlAckAlertPG
	}
	return execAlertUpdate(db, alertID, query,
		alertTimeArg(dialect, at), userID, userID, alertID, orgID)
}

// resolveAlert marks an incident dealt with.
func resolveAlert(db *sql.DB, dialect, orgID, alertID, userID string, at time.Time) error {
	query := sqlResolveAlert
	if dialect == "postgres" {
		query = sqlResolveAlertPG
	}
	return execAlertUpdate(db, alertID, query,
		alertTimeArg(dialect, at), userID, alertID, orgID)
}

// execAlertUpdate runs one incident write.
//
// A write that matches no row means the alert does not exist or belongs
// to another tenant. Both are reported the same way, and as an error
// rather than a silent success: telling the caller "assigned" when
// nothing was assigned is the failure this returns instead of.
func execAlertUpdate(db *sql.DB, alertID, query string, args ...interface{}) error {
	res, err := db.Exec(query, args...)
	if err != nil {
		return fmt.Errorf("update alert %s: %w", alertID, err)
	}
	if affected, err := res.RowsAffected(); err == nil && affected == 0 {
		return ErrAlertNotFound
	}
	return nil
}

const (
	sqlAlertInOrg   = `SELECT id FROM alerts WHERE id = ? AND org_id = ?`
	sqlAlertInOrgPG = `SELECT id FROM alerts WHERE id = $1 AND org_id = $2`

	sqlMarkAlertRead = `INSERT INTO alert_reads (alert_id, user_id, read_at) VALUES (?, ?, ?)
		ON CONFLICT(alert_id, user_id) DO UPDATE SET read_at = excluded.read_at`
	sqlMarkAlertReadPG = `INSERT INTO alert_reads (alert_id, user_id, read_at) VALUES ($1, $2, $3)
		ON CONFLICT(alert_id, user_id) DO UPDATE SET read_at = excluded.read_at`

	sqlMarkAllAlertsRead = `INSERT INTO alert_reads (alert_id, user_id, read_at)
		SELECT id, ?, ? FROM alerts WHERE org_id = ? AND dismissed_at IS NULL
		ON CONFLICT(alert_id, user_id) DO UPDATE SET read_at = excluded.read_at`
	sqlMarkAllAlertsReadPG = `INSERT INTO alert_reads (alert_id, user_id, read_at)
		SELECT id, $1, $2 FROM alerts WHERE org_id = $3 AND dismissed_at IS NULL
		ON CONFLICT(alert_id, user_id) DO UPDATE SET read_at = excluded.read_at`

	sqlCountUnreadAlerts = `SELECT COUNT(*) FROM alerts a
		WHERE a.org_id = ? AND a.dismissed_at IS NULL AND a.read_at IS NULL
		AND NOT EXISTS (SELECT 1 FROM alert_reads r WHERE r.alert_id = a.id AND r.user_id = ?)`
	sqlCountUnreadAlertsPG = `SELECT COUNT(*) FROM alerts a
		WHERE a.org_id = $1 AND a.dismissed_at IS NULL AND a.read_at IS NULL
		AND NOT EXISTS (SELECT 1 FROM alert_reads r WHERE r.alert_id = a.id AND r.user_id = $2)`
)

// markAlertReadBy records that one person read one alert.
func markAlertReadBy(db *sql.DB, dialect, orgID, alertID, userID string, at time.Time) error {
	if userID == "" {
		return fmt.Errorf("mark alert read: no caller identity")
	}
	// Scoped through the alert, so a caller cannot mark another tenant's
	// alert read and learn that its id exists.
	lookup := sqlAlertInOrg
	insert := sqlMarkAlertRead
	if dialect == "postgres" {
		lookup, insert = sqlAlertInOrgPG, sqlMarkAlertReadPG
	}
	var found string
	if err := db.QueryRow(lookup, alertID, orgID).Scan(&found); err != nil {
		return ErrAlertNotFound
	}
	if _, err := db.Exec(insert, alertID, userID, alertTimeArg(dialect, at)); err != nil {
		return fmt.Errorf("mark alert %s read: %w", alertID, err)
	}
	return nil
}

// markAllAlertsReadBy marks every undismissed alert in the org read for
// one person.
func markAllAlertsReadBy(db *sql.DB, dialect, orgID, userID string, at time.Time) error {
	if userID == "" {
		return fmt.Errorf("mark alerts read: no caller identity")
	}
	query := sqlMarkAllAlertsRead
	if dialect == "postgres" {
		query = sqlMarkAllAlertsReadPG
	}
	if _, err := db.Exec(query, userID, alertTimeArg(dialect, at), orgID); err != nil {
		return fmt.Errorf("mark all alerts read: %w", err)
	}
	return nil
}

// countUnreadAlertsFor counts what this person has not read.
func countUnreadAlertsFor(db *sql.DB, dialect, orgID, userID string) (int, error) {
	query := sqlCountUnreadAlerts
	if dialect == "postgres" {
		query = sqlCountUnreadAlertsPG
	}
	var n int
	if err := db.QueryRow(query, orgID, userID).Scan(&n); err != nil {
		return 0, fmt.Errorf("count unread alerts: %w", err)
	}
	return n, nil
}

// alertTimeArg renders a timestamp for whichever column type the dialect
// uses: TIMESTAMPTZ on Postgres, RFC 3339 text on SQLite.
func alertTimeArg(dialect string, t time.Time) interface{} {
	if dialect == "postgres" {
		return t.UTC()
	}
	return t.UTC().Format(time.RFC3339Nano)
}

// ── Store implementations ───────────────────────────────────

func (s *SQLiteStore) QueryAlerts(q AlertQuery) ([]models.Alert, error) {
	return queryAlerts(s.db, "sqlite", q)
}

func (s *SQLiteStore) CountUnreadAlertsFor(orgID, userID string) (int, error) {
	return countUnreadAlertsFor(s.db, "sqlite", orgID, userID)
}

func (s *SQLiteStore) MarkAlertReadBy(orgID, alertID, userID string) error {
	return markAlertReadBy(s.db, "sqlite", orgID, alertID, userID, time.Now().UTC())
}

func (s *SQLiteStore) MarkAllAlertsReadBy(orgID, userID string) error {
	return markAllAlertsReadBy(s.db, "sqlite", orgID, userID, time.Now().UTC())
}

func (s *SQLiteStore) SetAlertAssignee(orgID, alertID, userID string) error {
	return setAlertAssignee(s.db, "sqlite", orgID, alertID, userID)
}

func (s *SQLiteStore) AcknowledgeAlert(orgID, alertID, userID string) error {
	return acknowledgeAlert(s.db, "sqlite", orgID, alertID, userID, time.Now().UTC())
}

func (s *SQLiteStore) ResolveAlert(orgID, alertID, userID string) error {
	return resolveAlert(s.db, "sqlite", orgID, alertID, userID, time.Now().UTC())
}

func (s *PostgresStore) QueryAlerts(q AlertQuery) ([]models.Alert, error) {
	return queryAlerts(s.db, "postgres", q)
}

func (s *PostgresStore) CountUnreadAlertsFor(orgID, userID string) (int, error) {
	return countUnreadAlertsFor(s.db, "postgres", orgID, userID)
}

func (s *PostgresStore) MarkAlertReadBy(orgID, alertID, userID string) error {
	return markAlertReadBy(s.db, "postgres", orgID, alertID, userID, time.Now().UTC())
}

func (s *PostgresStore) MarkAllAlertsReadBy(orgID, userID string) error {
	return markAllAlertsReadBy(s.db, "postgres", orgID, userID, time.Now().UTC())
}

func (s *PostgresStore) SetAlertAssignee(orgID, alertID, userID string) error {
	return setAlertAssignee(s.db, "postgres", orgID, alertID, userID)
}

func (s *PostgresStore) AcknowledgeAlert(orgID, alertID, userID string) error {
	return acknowledgeAlert(s.db, "postgres", orgID, alertID, userID, time.Now().UTC())
}

func (s *PostgresStore) ResolveAlert(orgID, alertID, userID string) error {
	return resolveAlert(s.db, "postgres", orgID, alertID, userID, time.Now().UTC())
}
