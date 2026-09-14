package store

import (
	"database/sql"
	"fmt"
	"strings"

	"github.com/Tnsor-Labs/brokoli/models"
)

// Who started a run (#241).
//
// Kept in its own table rather than as columns on `runs`.
//
// The run row is read through seventeen SELECT lists and two shared
// scanners across the two dialects, all positional. Widening it means
// editing every one of them in step, and a single list left un-widened
// scans the next column into the wrong field -- a silent wrong answer,
// not a failure. Attribution is also read in three places, not
// everywhere a run is read, so the join is paid where it is wanted
// instead of on every run query in the product.
//
// The trade is one extra query on the paths that show attribution. They
// batch by run id, so it is one query per page rather than one per run.

// createRunAttributionTable is called from both dialects' migrations.
// Written once so a column cannot be added to one path and not the
// other; that is what broke the enterprise invite feature on Postgres.
func createRunAttributionTable(db *sql.DB, dialect string) {
	// run_id is the primary key: one attribution per run, and a re-write
	// replaces rather than accumulates.
	//
	// No foreign key to runs. Purging old runs is a plain DELETE that
	// does not know about this table, and a constraint would either make
	// the purge fail or silently take rows with it depending on the
	// dialect. Orphan rows are harmless -- nothing reads an attribution
	// except by a run id it already holds -- and are removed alongside
	// the runs they belong to by DeleteRunAttribution.
	_, _ = db.Exec(`CREATE TABLE IF NOT EXISTS run_attribution (
		run_id TEXT PRIMARY KEY,
		kind TEXT NOT NULL,
		user_id TEXT NOT NULL DEFAULT '',
		user_name TEXT NOT NULL DEFAULT '',
		token_name TEXT NOT NULL DEFAULT ''
	)`)
	_, _ = db.Exec(`CREATE INDEX IF NOT EXISTS idx_run_attribution_user ON run_attribution(user_id)`)
	_ = dialect
}

// setRunAttribution records what started a run, replacing any previous
// value.
func setRunAttribution(db *sql.DB, dialect, runID string, a *models.RunAttribution) error {
	if a == nil || a.Kind == "" {
		return nil
	}
	if !models.ValidRunTriggerKind(a.Kind) {
		// Refused rather than stored. A kind nothing renders shows as a
		// blank in the UI, which looks exactly like an attribution that
		// was never recorded.
		return fmt.Errorf("set run attribution %s: unknown kind %q", runID, a.Kind)
	}
	query := rewritePlaceholders(dialect,
		`INSERT INTO run_attribution (run_id, kind, user_id, user_name, token_name) VALUES (?,?,?,?,?)
		 ON CONFLICT(run_id) DO UPDATE SET kind=excluded.kind, user_id=excluded.user_id,
		   user_name=excluded.user_name, token_name=excluded.token_name`)
	if _, err := db.Exec(query, runID, string(a.Kind), a.UserID, a.UserName, a.TokenName); err != nil {
		return fmt.Errorf("set run attribution %s: %w", runID, err)
	}
	return nil
}

// getRunAttribution reads attribution for a set of runs, keyed by run id.
// Runs with no record are absent from the map: absence means "not
// recorded", which is every run created before this existed, and is not
// the same as "started by nobody".
func getRunAttribution(db *sql.DB, dialect string, runIDs []string) (map[string]models.RunAttribution, error) {
	out := map[string]models.RunAttribution{}
	if len(runIDs) == 0 {
		return out, nil
	}
	placeholders := make([]string, len(runIDs))
	args := make([]interface{}, len(runIDs))
	for i, id := range runIDs {
		placeholders[i] = "?"
		args[i] = id
	}
	query := rewritePlaceholders(dialect,
		`SELECT run_id, kind, user_id, user_name, token_name FROM run_attribution WHERE run_id IN (`+
			strings.Join(placeholders, ",")+`)`)
	rows, err := db.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("get run attribution: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var runID string
		var a models.RunAttribution
		var kind string
		if err := rows.Scan(&runID, &kind, &a.UserID, &a.UserName, &a.TokenName); err != nil {
			return nil, err
		}
		a.Kind = models.RunTriggerKind(kind)
		out[runID] = a
	}
	return out, rows.Err()
}

// listRunIDsStartedBy returns run ids a person started, newest first.
// Ordered by run id, which is a UUIDv7 and therefore in creation order,
// so this needs no join back to runs.
func listRunIDsStartedBy(db *sql.DB, dialect, userID string, limit int) ([]string, error) {
	if userID == "" {
		return nil, nil
	}
	query := rewritePlaceholders(dialect,
		`SELECT run_id FROM run_attribution WHERE user_id = ? ORDER BY run_id DESC LIMIT ?`)
	rows, err := db.Query(query, userID, limit)
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

// deleteRunAttribution removes records for runs that no longer exist,
// called by the purge that deletes them.
func deleteRunAttribution(db *sql.DB, dialect string, runIDs []string) error {
	if len(runIDs) == 0 {
		return nil
	}
	placeholders := make([]string, len(runIDs))
	args := make([]interface{}, len(runIDs))
	for i, id := range runIDs {
		placeholders[i] = "?"
		args[i] = id
	}
	query := rewritePlaceholders(dialect,
		`DELETE FROM run_attribution WHERE run_id IN (`+strings.Join(placeholders, ",")+`)`)
	if _, err := db.Exec(query, args...); err != nil {
		return fmt.Errorf("delete run attribution: %w", err)
	}
	return nil
}

// rewritePlaceholders turns ? into $N for Postgres. The core stores keep
// their dialects in separate files, so this is the one place these
// shared statements need it.
func rewritePlaceholders(dialect, query string) string {
	if dialect != "postgres" {
		return query
	}
	var b strings.Builder
	n := 1
	for i := 0; i < len(query); i++ {
		if query[i] == '?' {
			b.WriteString(fmt.Sprintf("$%d", n))
			n++
			continue
		}
		b.WriteByte(query[i])
	}
	return b.String()
}

// ── Store implementations ───────────────────────────────────

func (s *SQLiteStore) SetRunAttribution(runID string, a *models.RunAttribution) error {
	return setRunAttribution(s.db, "sqlite", runID, a)
}

func (s *SQLiteStore) GetRunAttribution(runIDs []string) (map[string]models.RunAttribution, error) {
	return getRunAttribution(s.db, "sqlite", runIDs)
}

func (s *SQLiteStore) ListRunIDsStartedBy(userID string, limit int) ([]string, error) {
	return listRunIDsStartedBy(s.db, "sqlite", userID, limit)
}

func (s *SQLiteStore) DeleteRunAttribution(runIDs []string) error {
	return deleteRunAttribution(s.db, "sqlite", runIDs)
}

func (s *PostgresStore) SetRunAttribution(runID string, a *models.RunAttribution) error {
	return setRunAttribution(s.db, "postgres", runID, a)
}

func (s *PostgresStore) GetRunAttribution(runIDs []string) (map[string]models.RunAttribution, error) {
	return getRunAttribution(s.db, "postgres", runIDs)
}

func (s *PostgresStore) ListRunIDsStartedBy(userID string, limit int) ([]string, error) {
	return listRunIDsStartedBy(s.db, "postgres", userID, limit)
}

func (s *PostgresStore) DeleteRunAttribution(runIDs []string) error {
	return deleteRunAttribution(s.db, "postgres", runIDs)
}
