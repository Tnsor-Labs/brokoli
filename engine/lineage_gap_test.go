package engine

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Tnsor-Labs/brokoli/models"
	"github.com/Tnsor-Labs/brokoli/store"
)

/*
 * A run whose lineage record is incomplete has to say so on the run.
 *
 * Before this, a store that could not save profiles produced a run that
 * looked complete: success, full row counts, and zero node_profiles. The
 * only trace was a debug line per node, and for provenance a log.Printf
 * that never reached the run at all.
 */

// lineageFailingStore makes exactly the two lineage writes fail, leaving
// everything else to the real store, so a run in these tests is a real
// run and the only difference is the one under test.
type lineageFailingStore struct {
	store.Store
	profileErr    error
	provenanceErr error
}

func (s *lineageFailingStore) SaveNodeProfile(runID, nodeID, profileJSON, schemaJSON, driftJSON string) error {
	if s.profileErr != nil {
		return s.profileErr
	}
	return s.Store.SaveNodeProfile(runID, nodeID, profileJSON, schemaJSON, driftJSON)
}

func (s *lineageFailingStore) SaveNodeProvenance(p *models.NodeProvenance) error {
	if s.provenanceErr != nil {
		return s.provenanceErr
	}
	return s.Store.SaveNodeProvenance(p)
}

// runWithLineageStore runs a two-node pipeline so announce-once is
// actually exercised: with one node, "once per run" and "once per node"
// are the same assertion and the test would pass either way.
func runWithLineageStore(t *testing.T, profileErr, provenanceErr error) (store.Store, *models.Run) {
	t.Helper()
	base, err := store.NewSQLiteStore(filepath.Join(t.TempDir(), "lineage.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = base.Close() })

	csvPath := filepath.Join(t.TempDir(), "in.csv")
	if err := os.WriteFile(csvPath, []byte("id,name\n1,a\n2,b\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	pipe := &models.Pipeline{
		ID: "lineage-gap", PipelineID: "lineage-gap", Name: "lineage-gap", Enabled: true,
		Nodes: []models.Node{
			{ID: "first", Name: "First", Type: models.NodeTypeSourceFile,
				Config: map[string]interface{}{"path": csvPath, "format": "csv"}},
			{ID: "second", Name: "Second", Type: models.NodeTypeSourceFile,
				Config: map[string]interface{}{"path": csvPath, "format": "csv"}},
		},
		CreatedAt: now, UpdatedAt: now,
	}
	if err := base.CreatePipeline(pipe); err != nil {
		t.Fatal(err)
	}

	s := &lineageFailingStore{Store: base, profileErr: profileErr, provenanceErr: provenanceErr}
	runner := NewRunner(s, nil, pipe, nil, nil, nil, nil, "", nil)
	// The profile write is fire-and-forget in production; wait for it here
	// so the assertions below are not racing it.
	runner.profileWG = &sync.WaitGroup{}
	run, err := runner.Execute()
	if err != nil {
		t.Fatalf("run failed: %v", err)
	}
	runner.profileWG.Wait()
	return base, run
}

func gapEvents(t *testing.T, s store.Store, runID string) []models.RunEvent {
	t.Helper()
	events, err := s.ListEventsByRun(runID)
	if err != nil {
		t.Fatal(err)
	}
	var gaps []models.RunEvent
	for _, e := range events {
		if e.EventType == models.RunEventLineageIncomplete {
			gaps = append(gaps, e)
		}
	}
	return gaps
}

func logsMatching(t *testing.T, s store.Store, runID string, level models.LogLevel, substr string) []models.LogEntry {
	t.Helper()
	entries, err := s.GetLogs(runID)
	if err != nil {
		t.Fatal(err)
	}
	var hits []models.LogEntry
	for _, e := range entries {
		if e.Level == level && strings.Contains(e.Message, substr) {
			hits = append(hits, e)
		}
	}
	return hits
}

// A store that cannot save profiles is a permanent limit, so the run says
// it once and the run still succeeds.
func TestUnsupportedProfileStoreAnnouncesTheGapOncePerRun(t *testing.T) {
	unsupported := fmt.Errorf("apistore: SaveNodeProfile not supported: %w", store.ErrUnsupported)
	s, run := runWithLineageStore(t, unsupported, nil)

	if run.Status != models.RunStatusSuccess {
		t.Fatalf("run status = %q, want success: a lineage gap must not fail the run", run.Status)
	}
	gaps := gapEvents(t, s, run.ID)
	if len(gaps) != 1 {
		t.Fatalf("got %d lineage-gap events across 2 nodes, want exactly 1 per run", len(gaps))
	}
	if !strings.Contains(gaps[0].Payload.Error, "this store cannot record profile") {
		t.Errorf("event payload %q does not say the store cannot record it", gaps[0].Payload.Error)
	}
	if got := logsMatching(t, s, run.ID, models.LogLevelWarning, "cannot record profile"); len(got) != 1 {
		t.Errorf("got %d warning log lines, want 1: an unsupported store is not a malfunction, "+
			"but it must be visible above debug", len(got))
	}
}

// A real write failure is not a capability limit and is logged loudly.
func TestRealProfileWriteFailureIsLoggedAtErrorLevel(t *testing.T) {
	s, run := runWithLineageStore(t, errors.New("disk I/O error"), nil)

	gaps := gapEvents(t, s, run.ID)
	if len(gaps) != 1 {
		t.Fatalf("got %d lineage-gap events, want 1", len(gaps))
	}
	if got := logsMatching(t, s, run.ID, models.LogLevelError, "lineage record is incomplete"); len(got) != 1 {
		t.Errorf("got %d error log lines, want 1: a write that failed is not the same as one the "+
			"store cannot do, and must not be demoted to a warning", len(got))
	}
	if got := logsMatching(t, s, run.ID, models.LogLevelWarning, "cannot record"); len(got) != 0 {
		t.Errorf("a genuine failure was reported as an unsupported-store warning (%d lines)", len(got))
	}
}

// Provenance used to go to the process log. It must reach the run.
func TestProvenanceGapReachesTheRun(t *testing.T) {
	s, run := runWithLineageStore(t, nil, errors.New("provenance table is gone"))

	gaps := gapEvents(t, s, run.ID)
	if len(gaps) != 1 {
		t.Fatalf("got %d lineage-gap events, want 1", len(gaps))
	}
	if !strings.Contains(gaps[0].Payload.Error, "provenance") {
		t.Errorf("event %q does not name provenance", gaps[0].Payload.Error)
	}
	if got := logsMatching(t, s, run.ID, models.LogLevelError, "provenance"); len(got) == 0 {
		t.Error("the provenance failure is not in the run's own logs; it used to go to the process log")
	}
}

// The two halves of the record fail independently and are reported
// independently: one event each, not one event for "lineage".
func TestProfileAndProvenanceAreAnnouncedSeparately(t *testing.T) {
	s, run := runWithLineageStore(t,
		fmt.Errorf("no profiles here: %w", store.ErrUnsupported),
		errors.New("provenance write failed"))

	gaps := gapEvents(t, s, run.ID)
	if len(gaps) != 2 {
		t.Fatalf("got %d lineage-gap events, want 2 (one profile, one provenance)", len(gaps))
	}
	var sawProfile, sawProvenance bool
	for _, g := range gaps {
		if strings.Contains(g.Payload.Error, "profile") {
			sawProfile = true
		}
		if strings.Contains(g.Payload.Error, "provenance") {
			sawProvenance = true
		}
	}
	if !sawProfile || !sawProvenance {
		t.Errorf("profile reported = %v, provenance reported = %v; both must be", sawProfile, sawProvenance)
	}
}

// The control. A run that records everything must say nothing, or the
// event means nothing.
func TestCompleteLineageEmitsNoGapEvent(t *testing.T) {
	s, run := runWithLineageStore(t, nil, nil)

	if gaps := gapEvents(t, s, run.ID); len(gaps) != 0 {
		t.Fatalf("a run with complete lineage emitted %d gap events, want 0: %q",
			len(gaps), gaps[0].Payload.Error)
	}
	profileJSON, _, _, err := s.GetNodeProfile(run.ID, "first")
	if err != nil || profileJSON == "" {
		t.Errorf("the control run did not actually record a profile (err=%v), so the other tests "+
			"prove nothing about the gap", err)
	}
}

// The event is informational. A projection must not let a note about
// metadata overwrite why a run failed.
func TestLineageGapEventDoesNotChangeRunOutcome(t *testing.T) {
	finished := time.Now().UTC()
	events := []models.RunEvent{
		{RunID: "r1", EventType: models.RunEventCreated,
			Payload: models.RunEventPayload{Status: models.RunStatusRunning, PipelineID: "p1"}},
		{RunID: "r1", EventType: models.RunEventTerminal,
			Payload: models.RunEventPayload{
				Status: models.RunStatusFailed, FinishedAt: &finished, Error: "node s1 failed: boom"}},
		{RunID: "r1", EventType: models.RunEventLineageIncomplete,
			Payload: models.RunEventPayload{Error: "this store cannot record profile"}},
	}
	run := ProjectRun("r1", events)
	if run.Status != models.RunStatusFailed {
		t.Errorf("status = %q, want failed", run.Status)
	}
	if run.Error != "node s1 failed: boom" {
		t.Errorf("run error = %q; the lineage note overwrote why the run failed", run.Error)
	}
}
