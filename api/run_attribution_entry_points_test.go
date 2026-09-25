package api

import (
	"testing"
	"time"

	"github.com/Tnsor-Labs/brokoli/models"
)

// The entry points #241 lists that the first pass did not reach.

// A dependency fan-out is neither a person nor a schedule: an upstream
// pipeline finishing is what started it.
func TestDependencyFanOutIsAttributed(t *testing.T) {
	s := attributionStore(t)
	seedAttributionRun(t, s, "p1", "r1")
	// The kind must at least round-trip; the fan-out itself needs two
	// pipelines and a finished upstream run, which engine tests cover.
	if err := s.SetRunAttribution("r1", &models.RunAttribution{
		Kind: models.RunTriggerKindDependency,
	}); err != nil {
		t.Fatalf("dependency attribution: %v", err)
	}
	got, _ := s.GetRunAttribution([]string{"r1"})
	if got["r1"].Kind != models.RunTriggerKindDependency {
		t.Errorf("kind = %q, want dependency", got["r1"].Kind)
	}
	if got["r1"].UserID != "" {
		t.Errorf("user = %q, want nobody on a dependency fan-out", got["r1"].UserID)
	}
}

// The dashboard's recent-activity list is one of the three places the
// issue names, and it builds its own row type rather than reusing
// models.Run, so it needed the field carried separately.
func TestDashboardRecentRunsCarryWhoStartedThem(t *testing.T) {
	s := newDashboardTestStore(t)
	started := time.Now().UTC().Add(-time.Hour)
	seedRunsAt(t, s, "pipe-1", "attributed", 1, models.RunStatusSuccess, started, time.Second)

	runs, err := s.ListRunsByPipeline("pipe-1", 10)
	if err != nil || len(runs) != 1 {
		t.Fatalf("seed: %v (%d runs)", err, len(runs))
	}
	if err := s.SetRunAttribution(runs[0].ID, &models.RunAttribution{
		Kind: models.RunTriggerKindUser, UserID: "u1", UserName: "Alice",
	}); err != nil {
		t.Fatal(err)
	}

	d := getDashboard(t, s)
	recent, _ := d["recent_runs"].([]interface{})
	if len(recent) != 1 {
		t.Fatalf("recent_runs = %d, want 1", len(recent))
	}
	row, _ := recent[0].(map[string]interface{})
	by, ok := row["triggered_by"].(map[string]interface{})
	if !ok {
		t.Fatalf("recent_runs[0] has no triggered_by: %+v", row)
	}
	if by["user_name"] != "Alice" || by["kind"] != "user" {
		t.Errorf("triggered_by = %+v, want Alice/user", by)
	}
}

// A run with no attribution must not grow an empty triggered_by on the
// dashboard either.
func TestDashboardRecentRunsOmitAbsentAttribution(t *testing.T) {
	s := newDashboardTestStore(t)
	seedRunsAt(t, s, "pipe-1", "plain", 1, models.RunStatusSuccess,
		time.Now().UTC().Add(-time.Hour), time.Second)

	d := getDashboard(t, s)
	recent, _ := d["recent_runs"].([]interface{})
	row, _ := recent[0].(map[string]interface{})
	if _, present := row["triggered_by"]; present {
		t.Errorf("an unattributed run carries triggered_by: %+v", row)
	}
}
