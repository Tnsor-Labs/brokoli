package api

import (
	"testing"
	"time"

	"github.com/Tnsor-Labs/brokoli/models"
	"github.com/Tnsor-Labs/brokoli/store"
)

// seedRunsAt creates a pipeline and n runs with an explicit start time and
// duration, for the cases seedRuns's "age ago, one second long" shape
// cannot express.
func seedRunsAt(t *testing.T, s store.Store, pipelineID, name string, n int, status models.RunStatus, started time.Time, dur time.Duration) {
	t.Helper()
	if err := s.CreatePipeline(&models.Pipeline{
		ID: pipelineID, Name: name, Enabled: true,
		WorkspaceID: models.DefaultWorkspaceID,
		Nodes:       []models.Node{{ID: "s1", Type: models.NodeTypeSourceFile, Name: "src"}},
		CreatedAt:   time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}); err != nil && n > 0 {
		// A second call for the same pipeline is fine; the caller is adding
		// runs of another status to it.
		if _, getErr := s.GetPipeline(pipelineID); getErr != nil {
			t.Fatalf("create pipeline: %v", err)
		}
	}
	for i := 0; i < n; i++ {
		run := &models.Run{
			ID:         pipelineID + "-" + string(status) + "-" + string(rune('a'+i)),
			PipelineID: pipelineID,
			Status:     status,
			StartedAt:  &started,
		}
		if dur > 0 {
			finished := started.Add(dur)
			run.FinishedAt = &finished
		}
		if err := s.CreateRun(run); err != nil {
			t.Fatalf("create run: %v", err)
		}
	}
}

// #606: the denominator was every run in the window, so a run that had not
// finished lowered the rate of a pipeline that had failed nothing, and a
// cancelled run counted as a failure.
func TestDashboardSuccessRateCountsOnlyFinishedRuns(t *testing.T) {
	s := newDashboardTestStore(t)
	started := time.Now().UTC().Add(-time.Hour)
	seedRunsAt(t, s, "pipe-1", "mixed", 3, models.RunStatusSuccess, started, time.Second)
	seedRunsAt(t, s, "pipe-1", "mixed", 1, models.RunStatusFailed, started, time.Second)
	seedRunsAt(t, s, "pipe-1", "mixed", 2, models.RunStatusRunning, started, 0)
	seedRunsAt(t, s, "pipe-1", "mixed", 1, models.RunStatusCancelled, started, time.Second)

	d := getDashboard(t, s)

	if got := d["runs_24h_total"]; got != float64(7) {
		t.Fatalf("runs_24h_total = %v, want 7 (every run is still reported)", got)
	}
	if got := d["runs_24h_finished"]; got != float64(4) {
		t.Fatalf("runs_24h_finished = %v, want 4 (3 success + 1 failed)", got)
	}
	// 3 of 4 finished runs succeeded. Over all seven it would read 42.
	if got := d["success_rate_24h"]; got != float64(75) {
		t.Fatalf("success_rate_24h = %v, want 75; in-flight and cancelled runs must not be in the denominator", got)
	}
}

// #606: there is no success rate over zero finished runs, and 100 is the
// value most likely to be read as "everything is fine".
func TestDashboardSuccessRateIsNullWithNothingFinished(t *testing.T) {
	s := newDashboardTestStore(t)

	d := getDashboard(t, s)
	if got, present := d["success_rate_24h"]; got != nil {
		t.Fatalf("success_rate_24h = %v (present=%v), want null on an empty window", got, present)
	}

	// Still null when runs exist but none has finished, which is the case a
	// bare "no runs" check would miss.
	seedRunsAt(t, s, "pipe-1", "starting", 2, models.RunStatusRunning, time.Now().UTC().Add(-time.Minute), 0)
	d = getDashboard(t, s)
	if got := d["success_rate_24h"]; got != nil {
		t.Fatalf("success_rate_24h = %v, want null while every run is still running", got)
	}
	if got := d["runs_24h_total"]; got != float64(2) {
		t.Fatalf("runs_24h_total = %v, want 2", got)
	}
}

// #607: both timestamps were formatted without a fractional part, so a run
// shorter than a second arrived with identical start and finish.
func TestDashboardSubSecondRunHasADuration(t *testing.T) {
	s := newDashboardTestStore(t)
	started := time.Now().UTC().Add(-time.Hour).Truncate(time.Second).Add(250 * time.Millisecond)
	seedRunsAt(t, s, "pipe-1", "quick", 1, models.RunStatusSuccess, started, 120*time.Millisecond)

	d := getDashboard(t, s)
	runs, _ := d["recent_runs"].([]interface{})
	if len(runs) != 1 {
		t.Fatalf("recent_runs = %d, want 1", len(runs))
	}
	run, _ := runs[0].(map[string]interface{})

	if got := run["duration_ms"]; got != float64(120) {
		t.Errorf("duration_ms = %v, want 120", got)
	}
	// The timestamps must carry the sub-second part too, so a client that
	// does subtract them reaches the same answer.
	startedAt, _ := run["started_at"].(string)
	finishedAt, _ := run["finished_at"].(string)
	if startedAt == finishedAt {
		t.Fatalf("started_at == finished_at == %q; a 120ms run must not look instantaneous", startedAt)
	}
	st, err := time.Parse(time.RFC3339Nano, startedAt)
	if err != nil {
		t.Fatalf("started_at %q: %v", startedAt, err)
	}
	ft, err := time.Parse(time.RFC3339Nano, finishedAt)
	if err != nil {
		t.Fatalf("finished_at %q: %v", finishedAt, err)
	}
	if d := ft.Sub(st); d != 120*time.Millisecond {
		t.Errorf("finished - started = %v, want 120ms", d)
	}
}

// #609: the trends keys are local dates and the buckets were filled by
// slicing the UTC timestamp string. East of UTC that files a run under the
// wrong day; west of UTC the run's UTC date is a tomorrow that is not in
// the map, and it is dropped from the chart entirely.
//
// The zone is chosen from the current UTC hour so that the UTC date and the
// local date always differ, whichever way the wall clock happens to sit.
func TestDashboardTrendsBucketByLocalDay(t *testing.T) {
	nowUTC := time.Now().UTC()
	offset := -11 * time.Hour // local date is a day behind UTC
	if nowUTC.Hour() >= 11 {
		offset = 13 * time.Hour // local date is a day ahead of UTC
	}
	saved := time.Local
	time.Local = time.FixedZone("test-zone", int(offset.Seconds()))
	t.Cleanup(func() { time.Local = saved })

	localDay := nowUTC.In(time.Local).Format("2006-01-02")
	utcDay := nowUTC.Format("2006-01-02")
	if localDay == utcDay {
		t.Fatalf("test setup: local day %s equals UTC day %s, the bug is not reachable", localDay, utcDay)
	}

	s := newDashboardTestStore(t)
	seedRunsAt(t, s, "pipe-1", "tz", 1, models.RunStatusSuccess, nowUTC, time.Second)

	d := getDashboard(t, s)
	trends, _ := d["trends"].([]interface{})
	if len(trends) != 7 {
		t.Fatalf("trends = %d entries, want 7", len(trends))
	}

	total := 0.0
	var onLocalDay float64
	for _, e := range trends {
		m, _ := e.(map[string]interface{})
		total += m["total"].(float64)
		if m["date"] == localDay {
			onLocalDay = m["total"].(float64)
		}
	}
	// West of UTC the run used to vanish; this catches that.
	if total != 1 {
		t.Errorf("trends total across all days = %v, want 1; the run must not be dropped", total)
	}
	// East of UTC it used to land on the previous day; this catches that.
	if onLocalDay != 1 {
		t.Errorf("trends[%s].total = %v, want 1; the run belongs to the local day", localDay, onLocalDay)
	}
}

// #610: the failure ranking counted every failure in the loaded window
// regardless of age, so a pipeline dealt with weeks ago outranked one
// failing now, on a panel whose neighbours are all 24-hour figures.
func TestDashboardTopFailingIsWindowed(t *testing.T) {
	s := newDashboardTestStore(t)
	old := time.Now().UTC().Add(-72 * time.Hour)
	recent := time.Now().UTC().Add(-2 * time.Hour)
	seedRunsAt(t, s, "pipe-old", "dealt-with", 9, models.RunStatusFailed, old, time.Second)
	seedRunsAt(t, s, "pipe-new", "failing-now", 2, models.RunStatusFailed, recent, time.Second)

	d := getDashboard(t, s)
	if got := d["top_failing_window_hours"]; got != float64(24) {
		t.Errorf("top_failing_window_hours = %v, want 24", got)
	}

	top, _ := d["top_failing"].([]interface{})
	if len(top) != 1 {
		t.Fatalf("top_failing = %+v, want only the pipeline failing inside the window", top)
	}
	entry, _ := top[0].(map[string]interface{})
	if entry["name"] != "failing-now" {
		t.Errorf("top_failing[0].name = %v, want failing-now; the 72h-old failures must not rank", entry["name"])
	}
	if entry["fail_count"] != float64(2) {
		t.Errorf("fail_count = %v, want 2", entry["fail_count"])
	}
}

// The cap is still there (#608). The response says so, rather than leaving
// a client to assume the counts are complete.
func TestDashboardDeclaresItsPerPipelineCap(t *testing.T) {
	s := newDashboardTestStore(t)
	d := getDashboard(t, s)
	if got := d["runs_per_pipeline_cap"]; got != float64(dashboardRunsPerPipeline) {
		t.Errorf("runs_per_pipeline_cap = %v, want %d", got, dashboardRunsPerPipeline)
	}
}
