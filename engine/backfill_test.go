package engine

import (
	"os"
	"path/filepath"
	"sort"
	"testing"
	"time"

	"github.com/Tnsor-Labs/brokoli/models"
	"github.com/Tnsor-Labs/brokoli/store"
)

// ADR-028 phase 3 (#397): backfill enumerates the pipeline's own schedule
// and dispatches one interval-stamped run per slice, oldest first.

func backfillTestStore(t *testing.T, dir string) *store.SQLiteStore {
	t.Helper()
	st, err := store.NewSQLiteStore(filepath.Join(dir, "bf.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

func TestEnumerateIntervals(t *testing.T) {
	hourly, err := scheduleFor("0 * * * *", "")
	if err != nil {
		t.Fatal(err)
	}
	day := func(h int) time.Time { return time.Date(2026, 8, 20, h, 0, 0, 0, time.UTC) }

	// Whole hours: [6,7) [7,8) [8,9) -- three complete intervals, the tick
	// AT the range start included, nothing dangling past the end.
	got, err := enumerateIntervals(hourly, day(6), day(9))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("6..9 hourly enumerated %d intervals, want 3: %v", len(got), got)
	}
	for i, iv := range got {
		if !iv[0].Equal(day(6+i)) || !iv[1].Equal(day(7+i)) {
			t.Errorf("interval %d = %v..%v, want %v..%v", i, iv[0], iv[1], day(6+i), day(7+i))
		}
	}

	// A range narrower than one interval holds no COMPLETE interval.
	got, err = enumerateIntervals(hourly, day(6).Add(10*time.Minute), day(6).Add(50*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Errorf("sub-interval range enumerated %v, want none", got)
	}

	// The cap refuses, it does not truncate.
	old := backfillMaxIntervals
	backfillMaxIntervals = 2
	t.Cleanup(func() { backfillMaxIntervals = old })
	if _, err := enumerateIntervals(hourly, day(0), day(9)); err == nil {
		t.Error("a range past the cap must be refused outright")
	}
}

func TestBackfillRefusals(t *testing.T) {
	dir := t.TempDir()
	st := backfillTestStore(t, dir)
	eng := drainEngineOnCleanup(t, NewEngine(st))

	mk := func(id, schedule string, cfg map[string]interface{}) {
		t.Helper()
		p := &models.Pipeline{
			ID: id, Name: id, Enabled: true, Schedule: schedule,
			Nodes: []models.Node{{ID: "src", Type: models.NodeTypeSourceFile, Name: "S",
				Config: cfg}},
			CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
		}
		if err := st.CreatePipeline(p); err != nil {
			t.Fatal(err)
		}
	}
	start := time.Date(2026, 8, 20, 0, 0, 0, 0, time.UTC)
	end := start.Add(3 * time.Hour)

	// No schedule: no interval grid to walk.
	mk("nosched", "", map[string]interface{}{"path": "x-${interval.start}.csv", "format": "csv"})
	if _, err := eng.Backfill("nosched", BackfillRequest{Start: start, End: end}); err == nil {
		t.Error("a schedule-less pipeline must be refused")
	}

	// Empty range.
	mk("sched", "0 * * * *", map[string]interface{}{"path": "x-${interval.start}.csv", "format": "csv"})
	if _, err := eng.Backfill("sched", BackfillRequest{Start: end, End: start}); err == nil {
		t.Error("an inverted range must be refused")
	}

	// Never references the interval (or the grandfathered ${param.date}):
	// refused without force, accepted with it.
	mk("noref", "0 * * * *", map[string]interface{}{"path": "static.csv", "format": "csv"})
	if _, err := eng.Backfill("noref", BackfillRequest{Start: start, End: end}); err == nil {
		t.Error("a pipeline with no slice scoping must be refused without force")
	}
	if _, err := eng.Backfill("noref", BackfillRequest{Start: start, End: end, Force: true}); err != nil {
		t.Errorf("force must override the reference check: %v", err)
	}

	// The pre-ADR-028 convention counts as slice-scoped.
	mk("legacy", "0 * * * *", map[string]interface{}{"path": "x-${param.date}.csv", "format": "csv"})
	if _, err := eng.Backfill("legacy", BackfillRequest{Start: start, End: end}); err != nil {
		t.Errorf("${param.date} pipelines are slice-scoped and must not need force: %v", err)
	}
}

// The end-to-end shape: three hourly slices land as three backfill-triggered
// runs, each writing its own interval's file, dispatched oldest first.
func TestBackfillRunsEachIntervalOldestFirst(t *testing.T) {
	dir := t.TempDir()
	st := backfillTestStore(t, dir)
	eng := drainEngineOnCleanup(t, NewEngine(st))

	csv := filepath.Join(dir, "in.csv")
	if err := os.WriteFile(csv, []byte("id\n1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	p := &models.Pipeline{
		ID: "bf", Name: "bf", Enabled: true, Schedule: "0 * * * *",
		Nodes: []models.Node{
			{ID: "src", Type: models.NodeTypeSourceFile, Name: "S",
				Config: map[string]interface{}{"path": csv, "format": "csv"}},
			{ID: "sink", Type: models.NodeTypeSinkFile, Name: "K",
				Config: map[string]interface{}{
					"path":   filepath.Join(dir, "out-${interval.start}.csv"),
					"format": "csv"}},
		},
		Edges:     []models.Edge{{From: "src", To: "sink"}},
		CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}
	if err := st.CreatePipeline(p); err != nil {
		t.Fatal(err)
	}

	start := time.Date(2026, 8, 20, 6, 0, 0, 0, time.UTC)
	plan, err := eng.Backfill("bf", BackfillRequest{Start: start, End: start.Add(3 * time.Hour)})
	if err != nil {
		t.Fatalf("backfill: %v", err)
	}
	if plan.Intervals != 3 || plan.Concurrency != 1 {
		t.Fatalf("plan = %+v, want 3 intervals at concurrency 1", plan)
	}
	if !plan.First.Equal(start) || !plan.Last.Equal(start.Add(3*time.Hour)) {
		t.Errorf("plan bounds %v..%v, want %v..%v", plan.First, plan.Last, start, start.Add(3*time.Hour))
	}

	// The dispatch goroutine is engine-lifetime work; Close (via the drain
	// helper) would also join it, but the assertions need it done NOW.
	eng.bg.Wait()

	for h := 6; h < 9; h++ {
		want := filepath.Join(dir,
			"out-"+time.Date(2026, 8, 20, h, 0, 0, 0, time.UTC).Format(time.RFC3339)+".csv")
		if _, err := os.Stat(want); err != nil {
			t.Errorf("slice %02d:00 wrote no file at %s", h, want)
		}
	}

	runs, err := st.ListRunsByPipeline("bf", 10)
	if err != nil {
		t.Fatal(err)
	}
	var bf []models.Run
	for _, r := range runs {
		if r.Trigger == models.RunTriggerBackfill {
			bf = append(bf, r)
		}
	}
	if len(bf) != 3 {
		t.Fatalf("found %d backfill runs, want 3", len(bf))
	}
	// Oldest interval dispatched first: sequential dispatch means start
	// order equals interval order.
	sort.Slice(bf, func(i, j int) bool {
		if bf[i].StartedAt == nil || bf[j].StartedAt == nil {
			t.Fatal("a dispatched run must have a start time")
		}
		return bf[i].StartedAt.Before(*bf[j].StartedAt)
	})
	for i, r := range bf {
		wantStart := start.Add(time.Duration(i) * time.Hour)
		if r.DataIntervalStart == nil || !r.DataIntervalStart.Equal(wantStart) {
			t.Errorf("run %d covers %v, want %v -- backfill must go oldest first",
				i, r.DataIntervalStart, wantStart)
		}
		// The grandfather clause: every backfill run still carries the
		// pre-ADR-028 date param, scoped to its own slice.
		if got := r.Params["date"]; got != wantStart.Format("2006-01-02") {
			t.Errorf("run %d params[date] = %q, want %q", i, got, wantStart.Format("2006-01-02"))
		}
	}
}
