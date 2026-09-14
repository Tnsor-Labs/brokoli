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
	// ON DELETE CASCADE, the same as every other per-run table here
	// (node_profiles, execution_attempts and four more). Deleting a run
	// takes its attribution with it, through every path that deletes a
	// run rather than only the ones somebody remembered to wire.
	//
	// The first version of this table had no foreign key, on the stated
	// grounds that a purge "would either make the purge fail or silently
	// take rows with it depending on the dialect", with orphans to be
	// cleared by DeleteRunAttribution instead. Both halves were wrong.
	// Taking the rows with it is precisely what is wanted and what every
	// sibling table already does, SQLite runs with foreign_keys(1) so the
	// two dialects behave identically, and DeleteRunAttribution was
	// called from nowhere in the product -- so the rows were not cleared
	// by anything and outlived their runs permanently.
	_, _ = db.Exec(`CREATE TABLE IF NOT EXISTS run_attribution (
		run_id TEXT PRIMARY KEY,
		kind TEXT NOT NULL,
		user_id TEXT NOT NULL DEFAULT '',
		user_name TEXT NOT NULL DEFAULT '',
		token_name TEXT NOT NULL DEFAULT '',
		FOREIGN KEY (run_id) REFERENCES runs(id) ON DELETE CASCADE
	)`)
	_, _ = db.Exec(`CREATE INDEX IF NOT EXISTS idx_run_attribution_user ON run_attribution(user_id)`)
	addRunAttributionForeignKey(db, dialect)
}

// addRunAttributionForeignKey retrofits the cascade onto a table created
// before it existed.
//
// CREATE TABLE IF NOT EXISTS does nothing to a table that is already
// there, so a deployment that ran any earlier version keeps the
// constraint-free table and keeps accumulating orphans. This is the half
// that fixes those.
//
// Idempotent and best effort, like the rest of this migration path: a
// failure here leaves the old table in place and working, which is worse
// than the fix and much better than a boot that fails.
func addRunAttributionForeignKey(db *sql.DB, dialect string) {
	if dialect == "postgres" {
		// Orphans first: ADD CONSTRAINT validates existing rows and would
		// fail on any row whose run is already gone, which on an old
		// deployment is most of them.
		_, _ = db.Exec(`DELETE FROM run_attribution WHERE run_id NOT IN (SELECT id FROM runs)`)
		// IF NOT EXISTS is not available for ADD CONSTRAINT, so this is
		// allowed to fail when the constraint is already there.
		_, _ = db.Exec(`ALTER TABLE run_attribution
			ADD CONSTRAINT run_attribution_run_id_fkey
			FOREIGN KEY (run_id) REFERENCES runs(id) ON DELETE CASCADE`)
		return
	}

	// SQLite cannot add a constraint to an existing table, so the table
	// is rebuilt. Only when it needs to be: an unconditional rebuild on
	// every boot would rewrite the table forever.
	var fkCount int
	if err := db.QueryRow(`SELECT COUNT(*) FROM pragma_foreign_key_list('run_attribution')`).Scan(&fkCount); err != nil || fkCount > 0 {
		return
	}

	// Nothing references run_attribution, so the rename below cannot
	// redirect another table's constraint and the rebuild is safe with
	// foreign keys left on.
	//
	// The WHERE clause does the orphan cleanup and the migration in one
	// step: a row whose run is gone would violate the new constraint on
	// insert, so it is dropped here rather than deleted in a separate
	// pass that could be interrupted between the two.
	tx, err := db.Begin()
	if err != nil {
		return
	}
	defer func() { _ = tx.Rollback() }()

	for _, stmt := range []string{
		`CREATE TABLE run_attribution_new (
			run_id TEXT PRIMARY KEY,
			kind TEXT NOT NULL,
			user_id TEXT NOT NULL DEFAULT '',
			user_name TEXT NOT NULL DEFAULT '',
			token_name TEXT NOT NULL DEFAULT '',
			FOREIGN KEY (run_id) REFERENCES runs(id) ON DELETE CASCADE
		)`,
		`INSERT INTO run_attribution_new (run_id, kind, user_id, user_name, token_name)
			SELECT run_id, kind, user_id, user_name, token_name FROM run_attribution
			WHERE run_id IN (SELECT id FROM runs)`,
		`DROP TABLE run_attribution`,
		`ALTER TABLE run_attribution_new RENAME TO run_attribution`,
		`CREATE INDEX IF NOT EXISTS idx_run_attribution_user ON run_attribution(user_id)`,
	} {
		if _, err := tx.Exec(stmt); err != nil {
			return
		}
	}
	_ = tx.Commit()
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

// deleteRunAttribution removes attribution for specific runs.
//
// NOT the cleanup path for a purge: the foreign key's ON DELETE CASCADE
// is, and it covers every route that deletes a run rather than the ones
// somebody remembered to call this from. It was documented as the purge's
// cleanup and called from nowhere, which is why orphans accumulated.
//
// Kept for removing attribution without removing the run, which is a
// different operation and the only one this is for.
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
