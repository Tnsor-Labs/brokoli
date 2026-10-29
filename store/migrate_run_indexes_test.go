package store

import (
	"database/sql"
	"os"
	"strings"
	"testing"
	"time"
)

func TestSQLiteRunIndexes(t *testing.T) {
	s := newTestStore(t)
	checkRunIndexes(t, s.db, `SELECT name FROM sqlite_master WHERE type = 'index' AND tbl_name IN ('runs', 'node_runs', 'run_events')`)

	// The point of the index: the purge listing reaches it.
	var plan []string
	rows, err := s.db.Query(`EXPLAIN QUERY PLAN SELECT id FROM runs WHERE started_at < ? AND started_at IS NOT NULL`, formatTime(time.Now()))
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var id, parent, unused int
		var detail string
		if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
			t.Fatal(err)
		}
		plan = append(plan, detail)
	}
	if got := strings.Join(plan, " | "); !strings.Contains(got, "idx_runs_started_at") {
		t.Errorf("purge listing plan does not use idx_runs_started_at: %s", got)
	}
}

func TestPostgresRunIndexes(t *testing.T) {
	url := os.Getenv("BROKOLI_TEST_POSTGRES_URL")
	if url == "" {
		t.Skip("BROKOLI_TEST_POSTGRES_URL not set")
	}
	s, err := NewPostgresStore(url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	checkRunIndexes(t, s.db, `SELECT indexname FROM pg_indexes WHERE schemaname = current_schema() AND tablename IN ('runs', 'node_runs', 'run_events')`)
}

func checkRunIndexes(t *testing.T, db *sql.DB, query string) {
	t.Helper()
	rows, err := db.Query(query)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	have := map[string]bool{}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatal(err)
		}
		have[name] = true
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if !have["idx_runs_started_at"] {
		t.Error("idx_runs_started_at is missing")
	}
	for _, redundant := range []string{"idx_node_runs_run", "idx_run_events_run"} {
		if have[redundant] {
			t.Errorf("%s is still there", redundant)
		}
	}
	// The indexes that make the dropped ones redundant must remain.
	for _, kept := range []string{"idx_node_runs_run_status", "idx_run_events_run_node"} {
		if !have[kept] {
			t.Errorf("%s is missing", kept)
		}
	}
}
