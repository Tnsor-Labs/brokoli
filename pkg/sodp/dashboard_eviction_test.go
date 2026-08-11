package sodp

import (
	"testing"
	"time"
)

// The dashboard snapshot's aggregates are computed by scanning live state,
// but completed runs are evicted from that state after a short TTL
// (api/server.go wires StartEviction at 30 minutes). So every window the
// snapshot claims to summarise — 24h counters, today/yesterday, the 7-day
// trend — can only ever see runs completed inside that TTL.
//
// This test documents the gap rather than asserting the desired behaviour:
// it shows the numbers collapsing while the runs they describe are still
// well inside their nominal window.
func TestRecomputeDashboard_AggregatesDecayAfterEviction(t *testing.T) {
	srv := NewServer()
	now := time.Now().UTC()
	// Two runs that finished an hour ago — comfortably inside "last 24h"
	// and inside "today", and squarely inside the 7-day trend.
	finished := now.Add(-1 * time.Hour)

	for _, id := range []string{"run-1", "run-2"} {
		bridgeEvent(srv, BridgeEvent{
			Type: "run.started", RunID: id, PipelineID: "pipe-1",
			PipelineName: "hourly-sync", Timestamp: finished,
		})
		bridgeEvent(srv, BridgeEvent{
			Type: "run.completed", RunID: id, PipelineID: "pipe-1",
			Timestamp: finished,
		})
	}

	recomputeDashboard(srv, "", now)
	before := dashboardSnapshot(t, srv, "")
	if before["runs_24h_total"] != 2 {
		t.Fatalf("precondition: runs_24h_total = %v, want 2", before["runs_24h_total"])
	}
	// "An hour ago" is normally still today, but in the first hour after
	// UTC midnight it crosses into yesterday's calendar day. Derive the
	// expected split from the seeded timestamp rather than assuming
	// "today", so the eviction assertions below aren't held hostage to
	// the wall clock. (The 24h window above is unaffected either way.)
	wantToday, wantYesterday := 2, 0
	if finished.Format("2006-01-02") != now.Format("2006-01-02") {
		wantToday, wantYesterday = 0, 2
	}
	if before["runs_today"] != wantToday {
		t.Fatalf("precondition: runs_today = %v, want %v", before["runs_today"], wantToday)
	}
	if before["runs_yesterday"] != wantYesterday {
		t.Fatalf("precondition: runs_yesterday = %v, want %v", before["runs_yesterday"], wantYesterday)
	}

	// Evict on the same 30-minute TTL production uses. These runs finished
	// an hour ago, so both are dropped.
	if n := srv.State.EvictCompleted(30 * time.Minute); n == 0 {
		t.Fatal("expected the hour-old completed runs to be evicted")
	}
	recomputeDashboard(srv, "", now)
	after := dashboardSnapshot(t, srv, "")

	t.Logf("runs_24h_total:   %v -> %v", before["runs_24h_total"], after["runs_24h_total"])
	t.Logf("runs_today:       %v -> %v", before["runs_today"], after["runs_today"])
	t.Logf("success_rate_24h: %v -> %v", before["success_rate_24h"], after["success_rate_24h"])

	// The runs are an hour old — still inside every window the snapshot
	// claims to report — yet the counters read zero.
	if after["runs_24h_total"] != 0 {
		t.Errorf("runs_24h_total = %v; expected the documented gap (0) — update this test if the source of truth changed", after["runs_24h_total"])
	}
	if after["runs_today"] != 0 {
		t.Errorf("runs_today = %v; expected the documented gap (0)", after["runs_today"])
	}
	// And success_rate_24h reports a perfect 100% off zero runs, because
	// that is its documented no-runs default.
	if after["success_rate_24h"] != 100 {
		t.Errorf("success_rate_24h = %v, want the misleading 100%% default", after["success_rate_24h"])
	}

	// The 7-day trend is the starkest case: it is structurally incapable of
	// showing seven days of history from a 30-minute window.
	trends, _ := after["trends"].([]map[string]any)
	if len(trends) != 7 {
		t.Fatalf("trends = %d buckets, want 7", len(trends))
	}
	totalAcrossWeek := 0
	for _, d := range trends {
		totalAcrossWeek += d["total"].(int)
	}
	if totalAcrossWeek != 0 {
		t.Errorf("7-day trend total = %d; expected 0, showing it cannot see past the eviction TTL", totalAcrossWeek)
	}
}
