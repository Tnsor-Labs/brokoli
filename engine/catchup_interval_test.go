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

// ADR-028 phase 2 (#397): pipelines opted into catchup get one run per
// missed interval, oldest first; the default stays the single-shot pass.

// catchupFixture builds an hourly, catchup-enabled file pipeline whose sink
// names its slice, plus a seed run marking [anchor-1h, anchor) as the last
// processed interval. Returns the anchor (= seed DataIntervalEnd).
func catchupFixture(t *testing.T, st store.Store, dir string, catchup bool, hoursBehind int) (*models.Pipeline, time.Time) {
	t.Helper()
	csv := filepath.Join(dir, "in.csv")
	if err := os.WriteFile(csv, []byte("id\n1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	p := &models.Pipeline{
		ID: "cu", Name: "cu", Enabled: true, Schedule: "0 * * * *", Catchup: catchup,
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
	anchor := time.Now().UTC().Truncate(time.Hour).Add(-time.Duration(hoursBehind) * time.Hour)
	// A run fires at its interval's END (ADR-028), so the seed's StartedAt
	// is the anchor tick itself and it covers the hour before it.
	seedStart, seedAt := anchor.Add(-time.Hour), anchor
	if err := st.CreateRun(&models.Run{
		ID: "seed", PipelineID: p.ID, Status: models.RunStatusSuccess,
		StartedAt: &seedAt, Trigger: models.RunTriggerScheduled,
		DataIntervalStart: &seedStart, DataIntervalEnd: &anchor,
	}); err != nil {
		t.Fatal(err)
	}
	return p, anchor
}

// newRunsOldestFirst returns the post-seed runs sorted by StartedAt.
func newRunsOldestFirst(t *testing.T, st store.Store, pipelineID string) []models.Run {
	t.Helper()
	runs, err := st.ListRunsByPipeline(pipelineID, 100)
	if err != nil {
		t.Fatal(err)
	}
	var out []models.Run
	for _, r := range runs {
		if r.ID != "seed" {
			out = append(out, r)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].StartedAt == nil || out[j].StartedAt == nil {
			t.Fatal("a dispatched run must have a start time")
		}
		return out[i].StartedAt.Before(*out[j].StartedAt)
	})
	return out
}

func TestCatchupPerIntervalDispatchesEachMissedSlice(t *testing.T) {
	dir := t.TempDir()
	st, err := store.NewSQLiteStore(filepath.Join(dir, "cu.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	eng := drainEngineOnCleanup(t, NewEngine(st))
	sched := NewScheduler(eng, st, &fakeLeaderElector{leader: true})

	p, anchor := catchupFixture(t, st, dir, true, 3)
	sched.catchUpMissedRuns([]models.Pipeline{*p})
	eng.bg.Wait()

	// Three whole hours behind means at least three complete intervals
	// (four only if an hour boundary passed mid-test), forming a
	// contiguous chain from the anchor -- the interval the SEED covered is
	// not re-run, and each dispatched slice wrote its own file.
	got := newRunsOldestFirst(t, st, p.ID)
	if len(got) < 3 || len(got) > 4 {
		t.Fatalf("dispatched %d runs, want 3 (4 only across an hour boundary)", len(got))
	}
	for i, r := range got {
		wantStart := anchor.Add(time.Duration(i) * time.Hour)
		if r.DataIntervalStart == nil || !r.DataIntervalStart.Equal(wantStart) {
			t.Fatalf("run %d covers %v, want %v -- the chain must continue from the seed, oldest first",
				i, r.DataIntervalStart, wantStart)
		}
		if r.Trigger != models.RunTriggerScheduled {
			t.Errorf("run %d trigger = %q; catch-up runs ARE the scheduled runs that never fired", i, r.Trigger)
		}
		f := filepath.Join(dir, "out-"+wantStart.Format(time.RFC3339)+".csv")
		if _, err := os.Stat(f); err != nil {
			t.Errorf("slice %v wrote no file at %s", wantStart, f)
		}
	}
}

func TestCatchupCapKeepsTheNewestSlices(t *testing.T) {
	dir := t.TempDir()
	st, err := store.NewSQLiteStore(filepath.Join(dir, "cu.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	eng := drainEngineOnCleanup(t, NewEngine(st))
	sched := NewScheduler(eng, st, &fakeLeaderElector{leader: true})

	old := catchUpMaxIntervals
	catchUpMaxIntervals = 2
	t.Cleanup(func() { catchUpMaxIntervals = old })

	p, anchor := catchupFixture(t, st, dir, true, 5)
	sched.catchUpMissedRuns([]models.Pipeline{*p})
	eng.bg.Wait()

	// Five missed, cap two: exactly two dispatched, and they are the
	// NEWEST two -- a capped catch-up lands the pipeline current and drops
	// the oldest slices (loudly), it does not stay three hours stale.
	got := newRunsOldestFirst(t, st, p.ID)
	if len(got) != 2 {
		t.Fatalf("dispatched %d runs, want exactly the cap (2)", len(got))
	}
	for _, r := range got {
		if r.DataIntervalEnd == nil || !r.DataIntervalEnd.After(anchor.Add(3*time.Hour)) {
			t.Errorf("run covers %v..%v -- the cap must drop the OLDEST slices, not the newest",
				r.DataIntervalStart, r.DataIntervalEnd)
		}
	}
}

func TestCatchupDefaultStaysSingleShot(t *testing.T) {
	dir := t.TempDir()
	st, err := store.NewSQLiteStore(filepath.Join(dir, "cu.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	eng := drainEngineOnCleanup(t, NewEngine(st))
	sched := NewScheduler(eng, st, &fakeLeaderElector{leader: true})

	// Three hours behind, catchup NOT set: today's behavior exactly -- one
	// catch-up run, not one per missed interval.
	p, _ := catchupFixture(t, st, dir, false, 3)
	sched.catchUpMissedRuns([]models.Pipeline{*p})
	eng.bg.Wait()

	deadline := time.Now().Add(2 * time.Second)
	var got []models.Run
	for {
		got = newRunsOldestFirst(t, st, p.ID)
		if len(got) >= 1 || time.Now().After(deadline) {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if len(got) != 1 {
		t.Fatalf("default catch-up dispatched %d runs, must stay exactly 1 (opt-in only)", len(got))
	}
}

func TestCatchupPerIntervalSkipsWhenNotLeader(t *testing.T) {
	dir := t.TempDir()
	st, err := store.NewSQLiteStore(filepath.Join(dir, "cu.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	eng := drainEngineOnCleanup(t, NewEngine(st))
	sched := NewScheduler(eng, st, &fakeLeaderElector{leader: false})

	p, _ := catchupFixture(t, st, dir, true, 3)
	sched.catchUpMissedRuns([]models.Pipeline{*p})
	eng.bg.Wait()

	if got := newRunsOldestFirst(t, st, p.ID); len(got) != 0 {
		t.Fatalf("a non-leader dispatched %d catch-up runs; the gate must hold for the per-interval path too", len(got))
	}
}
