package store

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/Tnsor-Labs/brokoli/models"
	"github.com/Tnsor-Labs/brokoli/pkg/tenancy"
)

// The resources whose tenancy the scoped store enforces (ADR-049). One
// line each: how the row's workspace is found. Phase 1 covers pipelines,
// runs and alerts; the rest move here resource by resource.
func init() {
	tenancy.Register("pipelines", tenancy.Direct("pipelines", "org_id", "workspace_id"))
	tenancy.Register("runs", tenancy.Via("runs", "pipeline_id", "pipelines"))
	// An alert about no pipeline belongs to the organization; one about a
	// pipeline that is gone is invisible, its workspace being unknown.
	tenancy.Register("alerts", tenancy.Via("alerts", "pipeline_id", "pipelines").AllowNull())
	tenancy.Register("dead_letter_queue", tenancy.Via("dead_letter_queue", "pipeline_id", "pipelines"))
}

// listDLQIn is ListDLQByOrg kept to the pipelines the scope lists: the
// current workspace's dead-letter entries.
func listDLQIn(db *sql.DB, dialect string, scope tenancy.Scope, includeResolved bool, limit int) ([]DLQEntry, error) {
	if limit <= 0 {
		limit = 100
	}
	filter, args, err := scope.ListFilter("pipelines", "p")
	if err != nil {
		return nil, err
	}
	resolvedAt, unresolved := "COALESCE(d.resolved_at,'')", " AND d.resolved = 0"
	if dialect == "postgres" {
		resolvedAt, unresolved = "COALESCE(d.resolved_at::text,'')", " AND d.resolved = FALSE"
	}
	query := `SELECT d.id, d.pipeline_id, d.run_id, d.error, d.node_id, d.node_name, d.payload,
		d.created_at, d.resolved, ` + resolvedAt + `, COALESCE(p.name,'')
		FROM dead_letter_queue d JOIN pipelines p ON p.id = d.pipeline_id
		WHERE ` + filter
	if !includeResolved {
		query += unresolved
	}
	query += " ORDER BY d.created_at DESC LIMIT ?"
	rows, err := db.Query(rewritePlaceholders(dialect, query), append(args, limit)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	entries := []DLQEntry{}
	for rows.Next() {
		var e DLQEntry
		if err := rows.Scan(&e.ID, &e.PipelineID, &e.RunID, &e.Error, &e.NodeID, &e.NodeName,
			&e.Payload, &e.CreatedAt, &e.Resolved, &e.ResolvedAt, &e.PipelineName); err != nil {
			return nil, err
		}
		entries = append(entries, e)
	}
	return entries, rows.Err()
}

func (s *SQLiteStore) ListDLQIn(scope tenancy.Scope, includeResolved bool, limit int) ([]DLQEntry, error) {
	return listDLQIn(s.db, "sqlite", scope, includeResolved, limit)
}

func (s *PostgresStore) ListDLQIn(scope tenancy.Scope, includeResolved bool, limit int) ([]DLQEntry, error) {
	return listDLQIn(s.db, "postgres", scope, includeResolved, limit)
}

// ErrNotVisible is what the scoped store answers for a row that does not
// exist or that the scope does not see; the two are deliberately the same
// answer, so a caller cannot learn that an id exists elsewhere.
var ErrNotVisible = errors.New("not found")

// bindFilter numbers a tenancy filter's ? placeholders with ph, so it can
// join a query that numbers its own (Postgres $n).
func bindFilter(clause string, ph func() string) string {
	var b strings.Builder
	for _, r := range clause {
		if r == '?' {
			b.WriteString(ph())
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

// tenancyOwner answers the organization and workspace of one row, from the
// registry's declaration of how to find them.
func tenancyOwner(db *sql.DB, dialect, resource, id string) (string, string, error) {
	query, err := tenancy.OwnerQuery(resource)
	if err != nil {
		return "", "", err
	}
	var org, ws sql.NullString
	err = db.QueryRow(rewritePlaceholders(dialect, query), id).Scan(&org, &ws)
	if errors.Is(err, sql.ErrNoRows) {
		return "", "", ErrNotVisible
	}
	if err != nil {
		return "", "", fmt.Errorf("tenancy owner of %s %s: %w", resource, id, err)
	}
	return org.String, ws.String, nil
}

func (s *SQLiteStore) TenancyOwner(resource, id string) (string, string, error) {
	return tenancyOwner(s.db, "sqlite", resource, id)
}

func (s *PostgresStore) TenancyOwner(resource, id string) (string, string, error) {
	return tenancyOwner(s.db, "postgres", resource, id)
}

// listRunIDsStartedByIn is listRunIDsStartedBy kept to the runs the scope
// lists: the current workspace's.
func listRunIDsStartedByIn(db *sql.DB, dialect string, scope tenancy.Scope, userID string, limit int) ([]string, error) {
	if userID == "" {
		return nil, nil
	}
	filter, args, err := scope.ListFilter("runs", "r")
	if err != nil {
		return nil, err
	}
	query := rewritePlaceholders(dialect, `SELECT a.run_id FROM run_attribution a JOIN runs r ON r.id = a.run_id
		WHERE a.user_id = ? AND `+filter+` ORDER BY a.run_id DESC LIMIT ?`)
	rows, err := db.Query(query, append(append([]any{userID}, args...), limit)...)
	if err != nil {
		return nil, fmt.Errorf("list runs started by %s: %w", userID, err)
	}
	defer rows.Close()
	ids := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

func (s *SQLiteStore) ListRunIDsStartedByIn(scope tenancy.Scope, userID string, limit int) ([]string, error) {
	return listRunIDsStartedByIn(s.db, "sqlite", scope, userID, limit)
}

func (s *PostgresStore) ListRunIDsStartedByIn(scope tenancy.Scope, userID string, limit int) ([]string, error) {
	return listRunIDsStartedByIn(s.db, "postgres", scope, userID, limit)
}

// countUnreadAlertsIn is countUnreadAlertsFor kept to the alerts the scope
// lists.
func countUnreadAlertsIn(db *sql.DB, dialect string, scope tenancy.Scope, userID string) (int, error) {
	filter, args, err := scope.ListFilter("alerts", "a")
	if err != nil {
		return 0, err
	}
	query := rewritePlaceholders(dialect, `SELECT COUNT(*) FROM alerts a
		WHERE a.org_id = ? AND a.dismissed_at IS NULL AND a.read_at IS NULL
		AND NOT EXISTS (SELECT 1 FROM alert_reads r WHERE r.alert_id = a.id AND r.user_id = ?)
		AND `+filter)
	var n int
	if err := db.QueryRow(query, append([]any{scope.OrgID, userID}, args...)...).Scan(&n); err != nil {
		return 0, fmt.Errorf("count unread alerts: %w", err)
	}
	return n, nil
}

func (s *SQLiteStore) CountUnreadAlertsIn(scope tenancy.Scope, userID string) (int, error) {
	return countUnreadAlertsIn(s.db, "sqlite", scope, userID)
}

func (s *PostgresStore) CountUnreadAlertsIn(scope tenancy.Scope, userID string) (int, error) {
	return countUnreadAlertsIn(s.db, "postgres", scope, userID)
}

// ScopedStore is the store as one request may use it (ADR-049): every
// read through it is kept to what the request's scope sees.
type ScopedStore struct {
	st    Store
	scope tenancy.Scope
}

// Scoped binds st to scope.
func Scoped(st Store, scope tenancy.Scope) ScopedStore {
	return ScopedStore{st: st, scope: scope}
}

// Scope is the scope this store is bound to.
func (x ScopedStore) Scope() tenancy.Scope { return x.scope }

// Visible reports whether the row of resource with this id may be fetched
// by id under the scope: it exists, and is in the caller's organization
// and a workspace they belong to.
func (x ScopedStore) Visible(resource, id string) bool {
	if !x.scope.Valid() || id == "" {
		return false
	}
	if !x.scope.Tenanted() {
		return true
	}
	org, ws, err := x.st.TenancyOwner(resource, id)
	if err != nil {
		return false
	}
	return x.scope.Sees(org, ws)
}

// Pipeline is GetPipeline, answering ErrNotVisible for a pipeline the scope
// does not see.
func (x ScopedStore) Pipeline(id string) (*models.Pipeline, error) {
	if !x.Visible("pipelines", id) {
		return nil, ErrNotVisible
	}
	return x.st.GetPipeline(id)
}

// Run is GetRun, answering ErrNotVisible for a run the scope does not see.
func (x ScopedStore) Run(id string) (*models.Run, error) {
	if !x.Visible("runs", id) {
		return nil, ErrNotVisible
	}
	return x.st.GetRun(id)
}

// ListPipelines lists the current workspace's pipelines: the organization's
// in that workspace, or, with no organization (the community edition),
// the workspace's.
func (x ScopedStore) ListPipelines() ([]models.Pipeline, error) {
	if !x.scope.Valid() {
		return []models.Pipeline{}, nil
	}
	if x.scope.OrgID != "" {
		return x.st.ListPipelinesByOrgAndWorkspace(x.scope.OrgID, x.scope.Workspace)
	}
	return x.st.ListPipelinesByWorkspace(x.scope.Workspace)
}

// DLQ lists the current workspace's dead-letter entries.
func (x ScopedStore) DLQ(includeResolved bool, limit int) ([]DLQEntry, error) {
	return x.st.ListDLQIn(x.scope, includeResolved, limit)
}

// RunIDsStartedBy lists the runs userID started, in the current workspace.
func (x ScopedStore) RunIDsStartedBy(userID string, limit int) ([]string, error) {
	return x.st.ListRunIDsStartedByIn(x.scope, userID, limit)
}

// Alerts is QueryAlerts kept to the current workspace's alerts.
func (x ScopedStore) Alerts(q AlertQuery) ([]models.Alert, error) {
	scope := x.scope
	q.Scope = &scope
	return x.st.QueryAlerts(q)
}

// CountUnreadAlerts counts what userID has not read, in the current
// workspace.
func (x ScopedStore) CountUnreadAlerts(userID string) (int, error) {
	return x.st.CountUnreadAlertsIn(x.scope, userID)
}
