package store

import (
	"fmt"
	"path/filepath"
	"sort"
	"testing"
	"time"

	"github.com/Tnsor-Labs/brokoli/models"
)

func TestFormatTimeSortsAsText(t *testing.T) {
	base := time.Date(2026, 10, 28, 10, 0, 5, 0, time.UTC)
	times := []time.Time{
		base,
		base.Add(100 * time.Millisecond),
		base.Add(123 * time.Millisecond),
		base.Add(time.Second),
		// Same instant as base+2s, in another zone: must render as UTC.
		base.Add(2 * time.Second).In(time.FixedZone("CAT", 2*3600)),
	}
	var text []string
	for _, tm := range times {
		text = append(text, formatTime(tm))
	}
	if !sort.StringsAreSorted(text) {
		t.Fatalf("formatted timestamps do not sort as text in time order: %q", text)
	}
	for _, s := range text {
		if len(s) != len("2006-01-02T15:04:05.000000000Z") || s[len(s)-1] != 'Z' {
			t.Errorf("%q is not fixed-width UTC", s)
		}
	}
	// The bug this replaces: RFC3339Nano trims zeros and sorts wrongly.
	a, b := base.Add(100*time.Millisecond).Format(time.RFC3339Nano), base.Add(123*time.Millisecond).Format(time.RFC3339Nano)
	if a < b {
		t.Fatalf("control: expected RFC3339Nano to misorder %q and %q", a, b)
	}
}

func TestParseTimeReadsEveryShapeTheStoreWrote(t *testing.T) {
	want := time.Date(2026, 10, 28, 9, 30, 15, 120000000, time.UTC)
	for _, s := range []string{
		formatTime(want),
		want.Format(time.RFC3339Nano),
		want.In(time.FixedZone("CAT", 2*3600)).Format(time.RFC3339Nano),
		"2026-10-28 09:30:15.12 +0000 UTC",                 // time.Time handed to the driver
		"2026-10-28 11:30:15.12 +0200 CAT m=+12.000000001", // ... with a monotonic reading
		"2026-10-28 09:30:15.12",                           // datetime('now') shape, UTC
		"2026-10-28T09:30:15.12",
	} {
		got, err := parseTime(s)
		if err != nil {
			t.Errorf("parseTime(%q): %v", s, err)
			continue
		}
		if !got.Equal(want) || got.Location() != time.UTC {
			t.Errorf("parseTime(%q) = %v, want %v in UTC", s, got, want)
		}
	}
	if _, err := parseTime("yesterday"); err == nil {
		t.Error("parseTime accepted garbage")
	}
}

// TestTimestampMigrationRewritesOlderDatabases fills a database the way
// older releases did, rolls it back to schema version 1, and reopens it.
func TestTimestampMigrationRewritesOlderDatabases(t *testing.T) {
	// Four log rows in batches of two: the loop has to come back for more.
	defer func(n int) { rewriteBatch = n }(rewriteBatch)
	rewriteBatch = 2
	path := filepath.Join(t.TempDir(), "old.db")
	s, err := NewSQLiteStore(path)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	if err := s.CreatePipeline(&models.Pipeline{ID: "p1", Name: "P", Nodes: []models.Node{}, Edges: []models.Edge{}, CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateRun(&models.Run{ID: "r1", PipelineID: "p1", Status: models.RunStatusSuccess}); err != nil {
		t.Fatal(err)
	}
	legacy := []string{
		// Log lines as RFC3339Nano wrote them, inserted out of order.
		`INSERT INTO logs (run_id, node_id, level, message, timestamp) VALUES ('r1', 'n', 'info', 'third', '2026-10-28T10:00:05.123Z')`,
		`INSERT INTO logs (run_id, node_id, level, message, timestamp) VALUES ('r1', 'n', 'info', 'first', '2026-10-28T10:00:05Z')`,
		`INSERT INTO logs (run_id, node_id, level, message, timestamp) VALUES ('r1', 'n', 'info', 'second', '2026-10-28T10:00:05.1Z')`,
		`INSERT INTO logs (run_id, node_id, level, message, timestamp) VALUES ('r1', 'n', 'info', 'fourth', '2026-10-28T12:00:06+02:00')`,
		// The default workspace as 005_workspaces.sql inserted it.
		`UPDATE workspaces SET created_at = '2026-10-28 09:00:00', updated_at = datetime('now') WHERE id = 'default'`,
		// A time.Time handed to the driver.
		`INSERT INTO run_provenance (run_id, node_id, facts, recorded_at) VALUES ('r1', 'n', '{}', '2026-10-28 10:00:07.5 +0000 UTC m=+3.1')`,
		// Something no layout reads: left alone.
		`UPDATE runs SET finished_at = 'not a time' WHERE id = 'r1'`,
		`DELETE FROM schema_migrations WHERE version = 2`,
	}
	for _, q := range legacy {
		if _, err := s.db.Exec(q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	// Control: before the migration the log order is wrong.
	if got := logMessages(t, s); got == "first second third fourth" {
		t.Fatalf("control: legacy timestamps already sort correctly (%s); the test proves nothing", got)
	}
	s.Close()

	s, err = NewSQLiteStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	if got := logMessages(t, s); got != "first second third fourth" {
		t.Errorf("logs after migration: %s", got)
	}
	ws, err := s.GetWorkspace("default")
	if err != nil {
		t.Fatal(err)
	}
	if want := time.Date(2026, 10, 28, 9, 0, 0, 0, time.UTC); !ws.CreatedAt.Equal(want) {
		t.Errorf("default workspace CreatedAt = %v, want %v", ws.CreatedAt, want)
	}
	prov, err := s.GetRunProvenance("r1")
	if err != nil {
		t.Fatal(err)
	}
	if want := time.Date(2026, 10, 28, 10, 0, 7, 500000000, time.UTC); len(prov) != 1 || !prov[0].RecordedAt.Equal(want) {
		t.Errorf("provenance = %+v, want one record at %v", prov, want)
	}
	var finished string
	if err := s.db.QueryRow(`SELECT finished_at FROM runs WHERE id = 'r1'`).Scan(&finished); err != nil || finished != "not a time" {
		t.Errorf("unreadable finished_at = %q (%v), want it left as it was", finished, err)
	}
	for _, bad := range nonCanonicalTimestamps(t, s) {
		if bad != "runs.finished_at=not a time" {
			t.Errorf("not rewritten: %s", bad)
		}
	}
}

func logMessages(t *testing.T, s *SQLiteStore) string {
	t.Helper()
	logs, err := s.GetLogs("r1")
	if err != nil {
		t.Fatal(err)
	}
	out := ""
	for i, l := range logs {
		if i > 0 {
			out += " "
		}
		out += l.Message
	}
	return out
}

// nonCanonicalTimestamps lists every stored timestamp formatTime did not
// write, as table.column=value.
func nonCanonicalTimestamps(t *testing.T, s *SQLiteStore) []string {
	t.Helper()
	var bad []string
	for _, tc := range sqliteTimestampColumns {
		for _, col := range tc.columns {
			rows, err := s.db.Query(fmt.Sprintf(`SELECT %s FROM %s WHERE %s IS NOT NULL AND %s != '' AND NOT %s GLOB '%s'`,
				col, tc.table, col, col, col, canonicalTimeGlob))
			if err != nil {
				continue // table not created on this database
			}
			for rows.Next() {
				var v string
				if err := rows.Scan(&v); err != nil {
					t.Fatal(err)
				}
				bad = append(bad, tc.table+"."+col+"="+v)
			}
			rows.Close()
		}
	}
	return bad
}
