package store

import (
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/Tnsor-Labs/brokoli/models"
)

func aggregateTestStore(t *testing.T) *SQLiteStore {
	t.Helper()
	s, err := NewSQLiteStore(filepath.Join(t.TempDir(), "agg.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func seedAggRun(t *testing.T, s *SQLiteStore, pipelineID, id string, status models.RunStatus, started time.Time) {
	t.Helper()
	if _, err := s.GetPipeline(pipelineID); err != nil {
		if err := s.CreatePipeline(&models.Pipeline{
			ID: pipelineID, Name: pipelineID, Enabled: true,
			WorkspaceID: models.DefaultWorkspaceID,
			Nodes:       []models.Node{{ID: "s1", Type: models.NodeTypeSourceFile, Name: "src"}},
			CreatedAt:   time.Now().UTC(), UpdatedAt: time.Now().UTC(),
		}); err != nil {
			t.Fatalf("create pipeline: %v", err)
		}
	}
	if err := s.CreateRun(&models.Run{
		ID: id, PipelineID: pipelineID, Status: status, StartedAt: &started,
	}); err != nil {
		t.Fatalf("create run %s: %v", id, err)
	}
}

func totalOf(rows []RunAggregate) int {
	n := 0
	for _, r := range rows {
		n += r.Count
	}
	return n
}

// A run exactly at the window boundary belongs inside it, including when
// it carries a sub-second component.
//
// SQLite compares started_at as text. Runs are stored with RFC3339Nano,
// which omits the fractional part when it is zero, so a row reads either
// "…T10:00:00.123456789Z" or "…T10:00:00Z". Against a boundary formatted
// the same way, the first compares LESS, because '.' (0x2E) sorts below
// 'Z' (0x5A), and the run drops out of its own window. The fix is a
// 19-character boundary with no zone suffix; this is what proves it.
func TestAggregateIncludesASubSecondRunAtTheBoundary(t *testing.T) {
	s := aggregateTestStore(t)
	boundary := time.Now().UTC().Add(-time.Hour).Truncate(time.Second)

	// Exactly on the boundary, with and without a fractional part, plus one
	// clearly inside and one clearly outside.
	seedAggRun(t, s, "p1", "on-boundary-frac", models.RunStatusSuccess, boundary.Add(123456789*time.Nanosecond))
	seedAggRun(t, s, "p1", "on-boundary-whole", models.RunStatusSuccess, boundary)
	seedAggRun(t, s, "p1", "inside", models.RunStatusSuccess, boundary.Add(30*time.Minute))
	seedAggRun(t, s, "p1", "outside", models.RunStatusSuccess, boundary.Add(-time.Second))

	rows, err := s.AggregateRunsByPipelineStatus(boundary, RunScope{})
	if err != nil {
		t.Fatalf("AggregateRunsByPipelineStatus: %v", err)
	}
	if got := totalOf(rows); got != 3 {
		t.Fatalf("counted %d runs, want 3 (both boundary runs plus the one inside, not the one before); rows=%+v", got, rows)
	}

	// The same boundary through the day series.
	rows, err = s.AggregateRunsByDayStatus(boundary, 0, RunScope{})
	if err != nil {
		t.Fatalf("AggregateRunsByDayStatus: %v", err)
	}
	if got := totalOf(rows); got != 3 {
		t.Fatalf("day series counted %d runs, want 3; rows=%+v", got, rows)
	}
}

// The day bucket must follow the requested offset, not UTC.
func TestAggregateByDayHonoursTheOffset(t *testing.T) {
	s := aggregateTestStore(t)
	// 23:30 UTC: the same instant is the next day at +120 minutes and the
	// same day at UTC.
	base := time.Now().UTC().AddDate(0, 0, -1)
	at := time.Date(base.Year(), base.Month(), base.Day(), 23, 30, 0, 0, time.UTC)
	seedAggRun(t, s, "p1", "late", models.RunStatusSuccess, at)

	since := at.Add(-48 * time.Hour)
	utcDay := at.Format("2006-01-02")
	aheadDay := at.Add(2 * time.Hour).Format("2006-01-02")
	if utcDay == aheadDay {
		t.Fatalf("test setup: %s and %s should differ", utcDay, aheadDay)
	}

	for _, tc := range []struct {
		offset int
		want   string
	}{{0, utcDay}, {120, aheadDay}} {
		rows, err := s.AggregateRunsByDayStatus(since, tc.offset, RunScope{})
		if err != nil {
			t.Fatalf("offset %d: %v", tc.offset, err)
		}
		if len(rows) != 1 {
			t.Fatalf("offset %d: %d rows, want 1; rows=%+v", tc.offset, len(rows), rows)
		}
		if rows[0].Day != tc.want {
			t.Errorf("offset %+d: day = %q, want %q", tc.offset, rows[0].Day, tc.want)
		}
	}
}

// Counting must not be bounded the way the old per-pipeline read was.
func TestAggregateCountsEveryRun(t *testing.T) {
	s := aggregateTestStore(t)
	const n = 300
	started := time.Now().UTC().Add(-time.Hour)
	for i := 0; i < n; i++ {
		seedAggRun(t, s, "p1", fmt.Sprintf("r%04d", i), models.RunStatusSuccess, started)
	}
	rows, err := s.AggregateRunsByPipelineStatus(started.Add(-time.Minute), RunScope{})
	if err != nil {
		t.Fatal(err)
	}
	if got := totalOf(rows); got != n {
		t.Fatalf("counted %d, want %d", got, n)
	}
}

// A scope restricts what is counted, in both directions.
func TestAggregateScopeRestrictsToOneOrg(t *testing.T) {
	s := aggregateTestStore(t)
	started := time.Now().UTC().Add(-time.Hour)
	if err := s.CreatePipeline(&models.Pipeline{
		ID: "p1", Name: "p1", Enabled: true, WorkspaceID: models.DefaultWorkspaceID,
		Nodes:     []models.Node{{ID: "s1", Type: models.NodeTypeSourceFile, Name: "src"}},
		CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ id, org string }{{"a1", "org-a"}, {"a2", "org-a"}, {"b1", "org-b"}} {
		if err := s.CreateRun(&models.Run{
			ID: tc.id, PipelineID: "p1", Status: models.RunStatusSuccess,
			StartedAt: &started, OrgID: tc.org,
		}); err != nil {
			t.Fatal(err)
		}
	}
	since := started.Add(-time.Minute)

	rows, err := s.AggregateRunsByPipelineStatus(since, RunScope{OrgID: "org-a"})
	if err != nil {
		t.Fatal(err)
	}
	if got := totalOf(rows); got != 2 {
		t.Errorf("org-a counted %d, want 2", got)
	}
	rows, _ = s.AggregateRunsByPipelineStatus(since, RunScope{OrgID: "org-b"})
	if got := totalOf(rows); got != 1 {
		t.Errorf("org-b counted %d, want 1", got)
	}
	rows, _ = s.AggregateRunsByPipelineStatus(since, RunScope{})
	if got := totalOf(rows); got != 3 {
		t.Errorf("unscoped counted %d, want 3", got)
	}
}

// ListRunIDsByStatus is the authoritative running set, so it must return
// only running runs and must not be scoped away from them by accident.
func TestListRunIDsByStatus(t *testing.T) {
	s := aggregateTestStore(t)
	started := time.Now().UTC().Add(-time.Hour)
	seedAggRun(t, s, "p1", "run-1", models.RunStatusRunning, started)
	seedAggRun(t, s, "p1", "run-2", models.RunStatusRunning, started.Add(time.Minute))
	seedAggRun(t, s, "p1", "done-1", models.RunStatusSuccess, started)

	ids, err := s.ListRunIDsByStatus(string(models.RunStatusRunning), RunScope{}, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 2 {
		t.Fatalf("ids = %v, want the two running runs", ids)
	}
	// Newest first.
	if ids[0] != "run-2" {
		t.Errorf("ids[0] = %q, want run-2 (newest first)", ids[0])
	}
	if limited, _ := s.ListRunIDsByStatus(string(models.RunStatusRunning), RunScope{}, 1); len(limited) != 1 {
		t.Errorf("limit 1 returned %d ids", len(limited))
	}
}
