package engine

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Tnsor-Labs/brokoli/models"
	"github.com/Tnsor-Labs/brokoli/store"
)

// ADR-028 phase 1 (#397), the engine half: a dispatched interval reaches
// the run row and the pipeline's own config strings, and a run without one
// leaves the references visibly unresolved rather than silently empty.

func TestRunPipelineOptsStampsAndResolvesTheInterval(t *testing.T) {
	dir := t.TempDir()
	csv := filepath.Join(dir, "in.csv")
	if err := os.WriteFile(csv, []byte("id\n1\n2\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	st, err := store.NewSQLiteStore(filepath.Join(dir, "iv.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	eng := drainEngineOnCleanup(t, NewEngine(st))

	// The sink path names the interval end: if resolution works, the file
	// lands at the resolved name; if it silently failed, the run would
	// "succeed" writing to a literal ${...} path -- which the assertions
	// below tell apart.
	p := &models.Pipeline{
		ID: "ivp", Name: "ivp", Enabled: true,
		Nodes: []models.Node{
			{ID: "src", Type: models.NodeTypeSourceFile, Name: "S",
				Config: map[string]interface{}{"path": csv, "format": "csv"}},
			{ID: "sink", Type: models.NodeTypeSinkFile, Name: "K",
				Config: map[string]interface{}{
					"path":   filepath.Join(dir, "out-${interval.end}.csv"),
					"format": "csv"}},
		},
		Edges:     []models.Edge{{From: "src", To: "sink"}},
		CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}
	if err := st.CreatePipeline(p); err != nil {
		t.Fatal(err)
	}

	start := time.Date(2026, 8, 27, 6, 0, 0, 0, time.UTC)
	end := time.Date(2026, 8, 28, 6, 0, 0, 0, time.UTC)
	run, err := eng.RunPipelineOpts("ivp", RunOptions{
		Trigger:           models.RunTriggerScheduled,
		DataIntervalStart: &start,
		DataIntervalEnd:   &end,
	})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if run.Status != models.RunStatusSuccess {
		t.Fatalf("run failed: %s", run.Error)
	}

	// The run row carries what dispatched it.
	got, err := st.GetRun(run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Trigger != models.RunTriggerScheduled {
		t.Errorf("trigger = %q, want scheduled", got.Trigger)
	}
	if got.DataIntervalStart == nil || !got.DataIntervalStart.Equal(start) ||
		got.DataIntervalEnd == nil || !got.DataIntervalEnd.Equal(end) {
		t.Errorf("interval did not survive to the run row: %v .. %v",
			got.DataIntervalStart, got.DataIntervalEnd)
	}

	// The variable resolved in node config: the file exists at the
	// RFC3339-named path, and no literal ${...} file exists.
	want := filepath.Join(dir, "out-2026-08-28T06:00:00Z.csv")
	if _, err := os.Stat(want); err != nil {
		entries, _ := os.ReadDir(dir)
		names := make([]string, 0, len(entries))
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Fatalf("resolved output missing at %s; dir holds %v", want, names)
	}

	// And the same pipeline run WITHOUT an interval leaves the reference
	// visibly unresolved -- the file lands at the literal name, which is
	// ugly on purpose: silent emptiness is how bad data happens.
	run2, err := eng.RunPipeline("ivp")
	if err != nil {
		t.Fatalf("manual run: %v", err)
	}
	if run2.Status != models.RunStatusSuccess {
		t.Fatalf("manual run failed: %s", run2.Error)
	}
	if _, err := os.Stat(filepath.Join(dir, "out-${interval.end}.csv")); err != nil {
		t.Error("an interval-less run should leave ${interval.end} visibly unresolved, not empty")
	}
	got2, err := st.GetRun(run2.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got2.Trigger != "" || got2.DataIntervalStart != nil {
		t.Errorf("a manual run gained provenance: %+v", got2)
	}
}

// The scheduler's derivation: options for a fired tick carry trigger
// "scheduled" and the [previous tick, tick) interval, falling back to
// clock-derived ticks when no cron entry is registered.
func TestScheduledRunOptionsDerivesTheInterval(t *testing.T) {
	dir := t.TempDir()
	st, err := store.NewSQLiteStore(filepath.Join(dir, "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	eng := drainEngineOnCleanup(t, NewEngine(st))
	sched := NewScheduler(eng, st, nil)

	hourly, err := scheduleFor("0 * * * *", "")
	if err != nil {
		t.Fatal(err)
	}
	opts := sched.scheduledRunOptions("unregistered-pipe", hourly)
	if opts.Trigger != models.RunTriggerScheduled {
		t.Errorf("trigger = %q, want scheduled", opts.Trigger)
	}
	if opts.DataIntervalStart == nil || opts.DataIntervalEnd == nil {
		t.Fatal("an hourly schedule must always yield an interval")
	}
	if !opts.DataIntervalStart.Before(*opts.DataIntervalEnd) {
		t.Error("interval start must precede its end")
	}
	if got := opts.DataIntervalEnd.Sub(*opts.DataIntervalStart); got != time.Hour {
		t.Errorf("hourly interval spans %v, want 1h", got)
	}
	if !opts.DataIntervalEnd.Before(time.Now().Add(time.Second)) {
		t.Error("the interval end must be a tick that has already occurred")
	}
	// Ends on a tick: minutes and smaller are zero for an hourly cron.
	if e := opts.DataIntervalEnd; e.Minute() != 0 || e.Second() != 0 {
		t.Errorf("interval end %v is not on an hourly tick", e)
	}

	// An impossible schedule dispatches without an interval rather than
	// refusing to dispatch: the run is more important than its stamp.
	never, err := scheduleFor("0 0 30 2 *", "")
	if err != nil {
		t.Fatal(err)
	}
	opts = sched.scheduledRunOptions("unregistered-pipe", never)
	if opts.Trigger != models.RunTriggerScheduled {
		t.Error("trigger survives even without a derivable interval")
	}
	if opts.DataIntervalStart != nil || opts.DataIntervalEnd != nil {
		t.Error("an underivable interval must be absent, not invented")
	}
}

// The interval references resolve through ResolveConfig the way every
// other variable does, including nested config values.
func TestIntervalVariablesResolve(t *testing.T) {
	start := time.Date(2026, 8, 27, 6, 0, 0, 0, time.UTC)
	end := time.Date(2026, 8, 28, 6, 0, 0, 0, time.UTC)
	vc := NewVariableContext(nil, "r1", time.Now())
	vc.IntervalStart, vc.IntervalEnd = &start, &end

	got := vc.Resolve("WHERE ts >= '${interval.start}' AND ts < '${interval.end}'")
	want := "WHERE ts >= '2026-08-27T06:00:00Z' AND ts < '2026-08-28T06:00:00Z'"
	if got != want {
		t.Errorf("resolved %q, want %q", got, want)
	}

	// Nil interval: visibly unresolved, exactly like any unknown variable.
	vc2 := NewVariableContext(nil, "r2", time.Now())
	if got := vc2.Resolve("${interval.start}"); got != "${interval.start}" {
		t.Errorf("nil interval resolved to %q; it must stay visible", got)
	}
	// An unknown interval field stays visible too.
	if got := vc.Resolve("${interval.middle}"); got != "${interval.middle}" {
		t.Errorf("unknown interval field resolved to %q", got)
	}
}

// A resumed run re-runs with its ORIGINAL interval: the varCtx reads the
// run row, not the dispatcher, so the slice a failed run was responsible
// for is the slice its resume processes.
func TestResumedRunKeepsItsInterval(t *testing.T) {
	dir := t.TempDir()
	st, err := store.NewSQLiteStore(filepath.Join(dir, "rv.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	eng := drainEngineOnCleanup(t, NewEngine(st))

	// The sink's parent "directory" is pre-created as a FILE, so the
	// first run fails after the interval is stamped (sink_file creates
	// missing directories, so a merely-absent one would not fail); the
	// resume, with the obstruction cleared, must write to the ORIGINAL
	// interval's filename.
	blocked := filepath.Join(dir, "later")
	if err := os.WriteFile(blocked, []byte("in the way"), 0o600); err != nil {
		t.Fatal(err)
	}
	csv := filepath.Join(dir, "in.csv")
	if err := os.WriteFile(csv, []byte("id\n1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	p := &models.Pipeline{
		ID: "rvp", Name: "rvp", Enabled: true,
		Nodes: []models.Node{
			{ID: "src", Type: models.NodeTypeSourceFile, Name: "S",
				Config: map[string]interface{}{"path": csv, "format": "csv"}},
			{ID: "sink", Type: models.NodeTypeSinkFile, Name: "K",
				Config: map[string]interface{}{
					"path":   filepath.Join(blocked, "out-${interval.start}.csv"),
					"format": "csv"}},
		},
		Edges:     []models.Edge{{From: "src", To: "sink"}},
		CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}
	if err := st.CreatePipeline(p); err != nil {
		t.Fatal(err)
	}
	start := time.Date(2026, 8, 27, 6, 0, 0, 0, time.UTC)
	end := time.Date(2026, 8, 28, 6, 0, 0, 0, time.UTC)
	first, err := eng.RunPipelineOpts("rvp", RunOptions{
		Trigger: models.RunTriggerScheduled, DataIntervalStart: &start, DataIntervalEnd: &end,
	})
	if err == nil && first.Status == models.RunStatusSuccess {
		t.Fatal("the first run should have failed on the missing directory")
	}
	if first == nil {
		t.Fatal("no run recorded")
	}

	if err := os.Remove(blocked); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(blocked, 0o755); err != nil {
		t.Fatal(err)
	}
	resumed, err := eng.ResumeRun(first.ID)
	if err != nil {
		t.Fatalf("resume: %v", err)
	}
	if resumed.Status != models.RunStatusSuccess {
		t.Fatalf("resumed run failed: %s", resumed.Error)
	}
	if _, err := os.Stat(filepath.Join(blocked, "out-2026-08-27T06:00:00Z.csv")); err != nil {
		t.Error("the resumed run did not write to its ORIGINAL interval's path -- " +
			"a resume must process the slice the failed run was responsible for")
	}
}
