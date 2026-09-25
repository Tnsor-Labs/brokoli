package store

import (
	"database/sql"
	"fmt"
	"time"
)

// Dashboard aggregates, counted in the database (#608).
//
// The dashboard used to load up to 200 runs per pipeline and count them in
// Go. That bounded memory and it also bounded truth: a pipeline on a
// one-minute schedule produces 1,440 runs a day and reported 200, and the
// seven-day trend series had those same 200 rows to spread across seven
// days, so the older days emptied and the chart showed a decline that was
// an artifact of the cap.
//
// These count rows where they live. None of them returns run records.

// RunAggregate is one group of runs. Day is empty when the query does not
// group by day, PipelineID when it does not group by pipeline.
type RunAggregate struct {
	Day        string
	PipelineID string
	Status     string
	Count      int
}

// RunScope selects which runs an aggregate covers. It mirrors how the API
// resolves visibility: an organization when there is one, otherwise the
// workspace, which is community mode. An empty scope counts everything,
// which is what a single-tenant install is.
//
// Runs carry org_id directly. They have no workspace of their own, so a
// workspace scope reaches them through their pipeline.
type RunScope struct {
	OrgID       string
	WorkspaceID string
}

// dayBucketExpr returns SQL bucketing started_at into a calendar day at a
// fixed offset from UTC.
//
// The offset is minutes, not a zone name, because a zone name means
// different offsets on different days and the two dialects do not agree
// with Go about which. Minutes is one number both sides read the same way.
// It is an int, so formatting it into the statement cannot inject
// anything; every value that comes from a caller is still bound.
//
// Its limitation, stated rather than hidden: one offset is applied to the
// whole window, so a day on the far side of a DST change is bucketed with
// today's offset. For a seven-day series that misplaces at most the runs
// in one hour of one day per year, which is a better trade than depending
// on each dialect's zone database agreeing with Go's.
func dayBucketExpr(dialect string, offsetMinutes int) string {
	if dialect == "postgres" {
		// AT TIME ZONE 'UTC' first, so the answer does not depend on the
		// session's TimeZone setting.
		return fmt.Sprintf(
			"to_char((started_at AT TIME ZONE 'UTC') + make_interval(mins => %d), 'YYYY-MM-DD')",
			offsetMinutes)
	}
	// SQLite stores started_at as RFC 3339 text.
	return fmt.Sprintf("substr(datetime(started_at, '%+d minutes'), 1, 10)", offsetMinutes)
}

// nextPlaceholder returns a generator for a dialect's bind markers.
// Postgres numbers them and the numbering must follow argument order, so
// the marker is taken from here rather than written out at each site --
// which is where an off-by-one crept in the first time this was written.
func nextPlaceholder(dialect string) func() string {
	n := 0
	return func() string {
		n++
		if dialect == "postgres" {
			return fmt.Sprintf("$%d", n)
		}
		return "?"
	}
}

// scopeSQL returns the predicate restricting runs to a scope, plus its
// argument.
//
// It takes the generator rather than a rendered placeholder so that it
// consumes a number only in the branches that emit one. Taking a string
// meant the caller had to spend a placeholder before knowing whether there
// would be a clause to put it in, which on Postgres left a gap: an empty
// scope produced "LIMIT $3" with two arguments bound. SQLite ignores the
// numbering, so the tests here would never have caught it.
func (sc RunScope) scopeSQL(ph func() string) (string, []interface{}) {
	switch {
	case sc.OrgID != "":
		return " AND org_id = " + ph(), []interface{}{sc.OrgID}
	case sc.WorkspaceID != "":
		return " AND pipeline_id IN (SELECT id FROM pipelines WHERE workspace_id = " + ph() + ")",
			[]interface{}{sc.WorkspaceID}
	default:
		return "", nil
	}
}

// sinceArg renders a window start for a dialect.
//
// SQLite compares started_at as text, and the bound value must NOT carry a
// zone suffix. Runs are stored with RFC3339Nano, which writes a fractional
// part only when there is one, so a row reads "…T10:00:00.123456789Z" or
// "…T10:00:00Z". Against a boundary of "…T10:00:00Z" the first compares
// LESS, because '.' (0x2E) sorts below 'Z' (0x5A) -- so a run at the
// boundary with any sub-second component is dropped from its own window.
// Measured, not reasoned about: with the Z, two of three sample rows
// matched; without it, three of three.
//
// A 19-character boundary has no such collision: every stored value shares
// its prefix and is longer, so the comparison falls through to the digits.
func sinceArg(dialect string, since time.Time) interface{} {
	if dialect == "postgres" {
		return since.UTC()
	}
	return since.UTC().Format("2006-01-02T15:04:05")
}

// queryRunAggregates runs one grouped count. groupDay switches between the
// day series and the per-pipeline series; grouping by both would multiply
// rows for no caller, since the day series is a total and the pipeline
// series is a single window.
func queryRunAggregates(db *sql.DB, dialect string, since time.Time, scope RunScope, groupDay bool, offsetMinutes int) ([]RunAggregate, error) {
	dayCol, pipelineCol := "''", "pipeline_id"
	if groupDay {
		dayCol, pipelineCol = dayBucketExpr(dialect, offsetMinutes), "''"
	}
	ph := nextPlaceholder(dialect)
	query := "SELECT " + dayCol + " AS day, " + pipelineCol + " AS pipeline_id, status, COUNT(*) AS n" +
		" FROM runs WHERE started_at IS NOT NULL AND started_at >= " + ph()
	args := []interface{}{sinceArg(dialect, since)}

	clause, scopeArgs := scope.scopeSQL(ph)
	query += clause + " GROUP BY day, pipeline_id, status"
	args = append(args, scopeArgs...)

	rows, err := db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []RunAggregate
	for rows.Next() {
		var a RunAggregate
		if err := rows.Scan(&a.Day, &a.PipelineID, &a.Status, &a.Count); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// queryRunIDsByStatus lists the ids of runs in one status, newest first.
// Used for running_run_ids, which the UI reconciles its live-state store
// against, so it must be the authoritative set rather than a sample.
func queryRunIDsByStatus(db *sql.DB, dialect, status string, scope RunScope, limit int) ([]string, error) {
	ph := nextPlaceholder(dialect)
	query := "SELECT id FROM runs WHERE status = " + ph()
	args := []interface{}{status}

	clause, scopeArgs := scope.scopeSQL(ph)
	query += clause
	args = append(args, scopeArgs...)
	query += " ORDER BY started_at DESC, id DESC LIMIT " + ph()
	args = append(args, limit)

	rows, err := db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	ids := make([]string, 0, 8)
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// ── Store implementations ───────────────────────────────────
//
// Both dialects share the query builders above; only the dialect name and
// the handle differ.

func (s *SQLiteStore) AggregateRunsByPipelineStatus(since time.Time, scope RunScope) ([]RunAggregate, error) {
	return queryRunAggregates(s.db, "sqlite", since, scope, false, 0)
}

func (s *SQLiteStore) AggregateRunsByDayStatus(since time.Time, offsetMinutes int, scope RunScope) ([]RunAggregate, error) {
	return queryRunAggregates(s.db, "sqlite", since, scope, true, offsetMinutes)
}

func (s *SQLiteStore) ListRunIDsByStatus(status string, scope RunScope, limit int) ([]string, error) {
	return queryRunIDsByStatus(s.db, "sqlite", status, scope, limit)
}

func (s *PostgresStore) AggregateRunsByPipelineStatus(since time.Time, scope RunScope) ([]RunAggregate, error) {
	return queryRunAggregates(s.db, "postgres", since, scope, false, 0)
}

func (s *PostgresStore) AggregateRunsByDayStatus(since time.Time, offsetMinutes int, scope RunScope) ([]RunAggregate, error) {
	return queryRunAggregates(s.db, "postgres", since, scope, true, offsetMinutes)
}

func (s *PostgresStore) ListRunIDsByStatus(status string, scope RunScope, limit int) ([]string, error) {
	return queryRunIDsByStatus(s.db, "postgres", status, scope, limit)
}

// OffsetMinutesFor returns t's offset from UTC in minutes, which is what
// AggregateRunsByDayStatus wants for its day boundary.
func OffsetMinutesFor(t time.Time) int {
	_, offsetSeconds := t.Zone()
	return offsetSeconds / 60
}
