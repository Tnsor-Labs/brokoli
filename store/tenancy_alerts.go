package store

import (
	"database/sql"
	"fmt"
	"time"

	"github.com/Tnsor-Labs/brokoli/pkg/tenancy"
)

// The alert actions as one request may take them (ADR-049). The store
// methods they wrap check the organization only, so a member of one
// workspace could dismiss, assign or resolve another workspace's alert by
// id. Each action here first asks whether the scope sees the alert, and
// answers ErrAlertNotFound when it does not: the same answer as an alert
// that does not exist.

// markAllAlertsReadIn marks every undismissed alert the scope lists read
// for one person: the current workspace's, not the organization's.
func markAllAlertsReadIn(db *sql.DB, dialect string, scope tenancy.Scope, userID string, at time.Time) error {
	if userID == "" {
		return fmt.Errorf("mark alerts read: no caller identity")
	}
	filter, args, err := scope.ListFilter("alerts", "a")
	if err != nil {
		return err
	}
	query := rewritePlaceholders(dialect, `INSERT INTO alert_reads (alert_id, user_id, read_at)
		SELECT a.id, ?, ? FROM alerts a WHERE a.org_id = ? AND a.dismissed_at IS NULL AND `+filter+`
		ON CONFLICT(alert_id, user_id) DO UPDATE SET read_at = excluded.read_at`)
	if _, err := db.Exec(query, append([]any{userID, alertTimeArg(dialect, at), scope.OrgID}, args...)...); err != nil {
		return fmt.Errorf("mark all alerts read: %w", err)
	}
	return nil
}

func (s *SQLiteStore) MarkAllAlertsReadIn(scope tenancy.Scope, userID string) error {
	return markAllAlertsReadIn(s.db, "sqlite", scope, userID, time.Now().UTC())
}

func (s *PostgresStore) MarkAllAlertsReadIn(scope tenancy.Scope, userID string) error {
	return markAllAlertsReadIn(s.db, "postgres", scope, userID, time.Now().UTC())
}

// alert answers ErrAlertNotFound unless the scope sees the alert.
func (x ScopedStore) alert(id string) error {
	if !x.Visible("alerts", id) {
		return ErrAlertNotFound
	}
	return nil
}

// MarkAlertRead records that userID read the alert. Without a caller
// identity it sets the alert's shared read flag, which a deployment with
// no identity at all has always had.
func (x ScopedStore) MarkAlertRead(alertID, userID string) error {
	if err := x.alert(alertID); err != nil {
		return err
	}
	if userID == "" {
		return x.st.MarkAlertRead(x.scope.OrgID, alertID)
	}
	return x.st.MarkAlertReadBy(x.scope.OrgID, alertID, userID)
}

// MarkAllAlertsRead marks the current workspace's alerts read for userID.
// Without a caller identity, and only where workspaces are not kept apart,
// it sets the organization's shared read flags as before; with workspaces
// kept apart that would reach into the others, so it is refused.
func (x ScopedStore) MarkAllAlertsRead(userID string) error {
	if !x.scope.Valid() {
		return ErrNotVisible
	}
	if userID != "" {
		return x.st.MarkAllAlertsReadIn(x.scope, userID)
	}
	if x.scope.ByWorkspace() {
		return fmt.Errorf("mark alerts read: no caller identity")
	}
	return x.st.MarkAllAlertsRead(x.scope.OrgID)
}

// CountUnreadAlerts counts what userID has not read, in the current
// workspace. Without a caller identity, and only where workspaces are not
// kept apart, it counts the organization's unread alerts as before.
func (x ScopedStore) CountUnreadAlerts(userID string) (int, error) {
	if userID == "" {
		if !x.scope.Valid() || x.scope.ByWorkspace() {
			return 0, nil
		}
		return x.st.CountUnreadAlerts(x.scope.OrgID)
	}
	return x.st.CountUnreadAlertsIn(x.scope, userID)
}

// DismissAlert dismisses an alert the scope sees.
func (x ScopedStore) DismissAlert(alertID string) error {
	if err := x.alert(alertID); err != nil {
		return err
	}
	return x.st.DismissAlert(x.scope.OrgID, alertID)
}

// SetAlertAssignee sets or clears the owner of an alert the scope sees.
func (x ScopedStore) SetAlertAssignee(alertID, userID string) error {
	if err := x.alert(alertID); err != nil {
		return err
	}
	return x.st.SetAlertAssignee(x.scope.OrgID, alertID, userID)
}

// AcknowledgeAlert records that userID is on an alert the scope sees.
func (x ScopedStore) AcknowledgeAlert(alertID, userID string) error {
	if err := x.alert(alertID); err != nil {
		return err
	}
	return x.st.AcknowledgeAlert(x.scope.OrgID, alertID, userID)
}

// ResolveAlert marks an alert the scope sees dealt with.
func (x ScopedStore) ResolveAlert(alertID, userID string) error {
	if err := x.alert(alertID); err != nil {
		return err
	}
	return x.st.ResolveAlert(x.scope.OrgID, alertID, userID)
}
