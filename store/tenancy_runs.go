package store

import (
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/Tnsor-Labs/brokoli/pkg/tenancy"
)

// purgeRunsIn deletes the runs older than days that the scope lists (the
// current workspace's) and returns their ids. Purge by organization
// deleted every workspace's run history for an admin of any one of them
// (ADR-049). The ids are selected first and exactly those are deleted, so
// the files removed afterwards match the rows removed.
func purgeRunsIn(db *sql.DB, dialect string, scope tenancy.Scope, days int) ([]string, error) {
	filter, args, err := scope.ListFilter("runs", "r")
	if err != nil {
		return nil, err
	}
	cutoff := alertTimeArg(dialect, time.Now().AddDate(0, 0, -days))
	ids, err := queryRunIDs(db, rewritePlaceholders(dialect,
		`SELECT r.id FROM runs r WHERE r.started_at < ? AND r.started_at IS NOT NULL AND `+filter),
		append([]any{cutoff}, args...)...)
	if err != nil {
		return nil, err
	}
	const chunk = 500
	for start := 0; start < len(ids); start += chunk {
		end := min(start+chunk, len(ids))
		batch := ids[start:end]
		params := make([]any, len(batch))
		for i, id := range batch {
			params[i] = id
		}
		query := `DELETE FROM runs WHERE id IN (?` + strings.Repeat(",?", len(batch)-1) + `)`
		if _, err := db.Exec(rewritePlaceholders(dialect, query), params...); err != nil {
			return ids[:start], fmt.Errorf("purge runs: %w", err)
		}
	}
	return ids, nil
}

func (s *SQLiteStore) PurgeRunsIn(scope tenancy.Scope, days int) ([]string, error) {
	return purgeRunsIn(s.db, "sqlite", scope, days)
}

func (s *PostgresStore) PurgeRunsIn(scope tenancy.Scope, days int) ([]string, error) {
	return purgeRunsIn(s.db, "postgres", scope, days)
}

// runCalendarIn is GetRunCalendarByOrg kept to the runs the scope lists:
// the current workspace's runs per day, not the organization's.
func runCalendarIn(db *sql.DB, dialect string, scope tenancy.Scope, days int) ([]CalendarDay, error) {
	filter, args, err := scope.ListFilter("runs", "r")
	if err != nil {
		return nil, err
	}
	day, start := "substr(r.started_at, 1, 10)", any(CalendarWindowStart(days))
	if dialect == "postgres" {
		day, start = "to_char(r.started_at AT TIME ZONE 'UTC', 'YYYY-MM-DD')", CalendarWindowStartTime(days)
	}
	rows, err := db.Query(rewritePlaceholders(dialect, `SELECT `+day+` as day,
		COUNT(*) as total,
		SUM(CASE WHEN r.status = 'success' THEN 1 ELSE 0 END) as success,
		SUM(CASE WHEN r.status = 'failed' THEN 1 ELSE 0 END) as failed,
		SUM(CASE WHEN r.status = 'running' THEN 1 ELSE 0 END) as running
	 FROM runs r WHERE r.started_at >= ? AND `+filter+` GROUP BY day ORDER BY day`), append([]any{start}, args...)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []CalendarDay
	for rows.Next() {
		var d CalendarDay
		if err := rows.Scan(&d.Date, &d.Total, &d.Success, &d.Failed, &d.Running); err != nil {
			return nil, err
		}
		result = append(result, d)
	}
	return result, rows.Err()
}

func (s *SQLiteStore) GetRunCalendarIn(scope tenancy.Scope, days int) ([]CalendarDay, error) {
	return runCalendarIn(s.db, "sqlite", scope, days)
}

func (s *PostgresStore) GetRunCalendarIn(scope tenancy.Scope, days int) ([]CalendarDay, error) {
	return runCalendarIn(s.db, "postgres", scope, days)
}
