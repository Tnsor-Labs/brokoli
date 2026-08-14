package engine

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/Tnsor-Labs/brokoli/models"
	"github.com/Tnsor-Labs/brokoli/store"
)

// TestCrashRecoveryBaseline records the persisted state after the scheduler
// process dies at important execution boundaries, then — unlike before
// issue #9 — reopens the store and runs engine.RecoverNonTerminalRuns
// against it exactly as cmd/serve.go does on the next boot, asserting the
// run reaches a terminal state instead of staying stuck non-terminal
// forever. See assertCrashRecovery.
func TestCrashRecoveryBaseline(t *testing.T) {
	// This harness's whole design is "assert recovery's classification is
	// correct with no other live-process signal available" — every event
	// AppendEvent persists is stamped with a fresh now() (store can't take
	// a caller-supplied CreatedAt), so recoveryTransitionGracePeriod's
	// real-clock grace window (engine/recovery.go) would otherwise defer
	// every one of these crash points on the first RecoverNonTerminalRuns
	// call, same as the false-transition-gap race it exists to guard
	// against. Zero it out here, exactly like the recoveryTransitionGracePeriod
	// overrides in recovery_test.go, so this harness keeps testing the
	// reconstruction logic itself rather than the grace period.
	old := recoveryTransitionGracePeriod
	recoveryTransitionGracePeriod = 0
	defer func() { recoveryTransitionGracePeriod = old }()

	cases := []struct {
		name           string
		point          string
		runStatus      models.RunStatus
		nodeStatuses   map[string]models.RunStatus
		outputExpected bool
		// wantEvents is the exact, ordered sequence of run_events.event_type
		// values that must be durably persisted by the time the process
		// crashes at this point. It is a direct consequence of the dual
		// writes added alongside CreateRun/UpdateRun/CreateNodeRun/
		// UpdateNodeRun in engine/runner.go — issue #6 requires that every
		// transition already exercised by this harness also produces a
		// persisted event, not just a mutated runs/node_runs row.
		wantEvents []models.RunEventType

		// wantRecoveredStatus is the run's terminal status after
		// RecoverNonTerminalRuns runs against the post-crash database.
		// Left "" for the one case with no run at all (nothing to recover).
		wantRecoveredStatus models.RunStatus
		// wantRecoveryEventType is the event type recovery must append to
		// explain how it reached wantRecoveredStatus: RunEventTerminal when
		// the event log could reconstruct the same outcome the live runner
		// would have persisted, RunEventRecoveryFailed when it had no
		// recoverable path, or "" when the run was already terminal before
		// recovery even ran (so recovery does nothing new at all).
		wantRecoveryEventType models.RunEventType
		// wantRecoveryScanned is how many non-terminal runs
		// RecoverNonTerminalRuns should find — 0 only for the
		// already-terminal case.
		wantRecoveryScanned int
		// liveLeaseNodeID names the node whose attempt 0 is still Claimed/
		// Started — and so still holds a live, unexpired
		// store.ExecutionAttemptStore lease (Tnsor-Labs/brokoli#90 M3) —
		// at the moment this crash point fires. Non-empty for every crash
		// point strictly between a node's attempt being claimed and that
		// same attempt's Complete/FailAttempt call landing. For these,
		// reconcileExecutionAttempts (engine/recovery.go) correctly defers
		// on the FIRST recovery pass — a live lease might still belong to
		// a genuinely live process — and only reclaims once that lease is
		// made to look expired, exercised via assertCrashRecoveryDefersThenReclaims
		// instead of assertCrashRecovery's single-pass assertion.
		liveLeaseNodeID string
	}{
		{name: "before run creation", point: crashPointBeforeRunCreate},
		{name: "after run creation", point: crashPointAfterRunCreate, runStatus: models.RunStatusRunning,
			wantEvents:            []models.RunEventType{models.RunEventCreated},
			wantRecoveredStatus:   models.RunStatusFailed,
			wantRecoveryEventType: models.RunEventRecoveryFailed,
			wantRecoveryScanned:   1},
		{name: "before node attempt creation", point: crashPointBeforeNodeAttemptCreate + ":source", runStatus: models.RunStatusRunning,
			wantEvents:            []models.RunEventType{models.RunEventCreated},
			wantRecoveredStatus:   models.RunStatusFailed,
			wantRecoveryEventType: models.RunEventRecoveryFailed,
			wantRecoveryScanned:   1},
		{name: "after node attempt creation", point: crashPointAfterNodeAttemptCreate + ":source", runStatus: models.RunStatusRunning, nodeStatuses: map[string]models.RunStatus{"source": models.RunStatusRunning},
			wantEvents:            []models.RunEventType{models.RunEventCreated, models.AttemptStarted},
			wantRecoveredStatus:   models.RunStatusFailed,
			wantRecoveryEventType: models.RunEventRecoveryFailed,
			wantRecoveryScanned:   1,
			liveLeaseNodeID:       "source"},
		{name: "before node execution", point: crashPointBeforeNodeExecution + ":source", runStatus: models.RunStatusRunning, nodeStatuses: map[string]models.RunStatus{"source": models.RunStatusRunning},
			wantEvents:            []models.RunEventType{models.RunEventCreated, models.AttemptStarted},
			wantRecoveredStatus:   models.RunStatusFailed,
			wantRecoveryEventType: models.RunEventRecoveryFailed,
			wantRecoveryScanned:   1,
			liveLeaseNodeID:       "source"},
		{name: "after sink side effect before persistence", point: crashPointAfterNodeExecutionBeforePersist + ":sink", runStatus: models.RunStatusRunning, nodeStatuses: map[string]models.RunStatus{"source": models.RunStatusSuccess, "condition": models.RunStatusSuccess, "sink": models.RunStatusRunning}, outputExpected: true,
			wantEvents:            []models.RunEventType{models.RunEventCreated, models.AttemptStarted, models.AttemptCompleted, models.AttemptStarted, models.AttemptCompleted, models.AttemptStarted},
			wantRecoveredStatus:   models.RunStatusFailed,
			wantRecoveryEventType: models.RunEventRecoveryFailed,
			wantRecoveryScanned:   1,
			liveLeaseNodeID:       "sink"},
		{name: "after sink completion persistence", point: crashPointAfterNodeCompletionPersist + ":sink", runStatus: models.RunStatusRunning, nodeStatuses: map[string]models.RunStatus{"source": models.RunStatusSuccess, "condition": models.RunStatusSuccess, "sink": models.RunStatusSuccess}, outputExpected: true,
			wantEvents:            []models.RunEventType{models.RunEventCreated, models.AttemptStarted, models.AttemptCompleted, models.AttemptStarted, models.AttemptCompleted, models.AttemptStarted, models.AttemptCompleted},
			wantRecoveredStatus:   models.RunStatusSuccess,
			wantRecoveryEventType: models.RunEventTerminal,
			wantRecoveryScanned:   1},
		{name: "before terminal persistence", point: crashPointBeforeRunTerminalPersist, runStatus: models.RunStatusRunning, nodeStatuses: map[string]models.RunStatus{"source": models.RunStatusSuccess, "condition": models.RunStatusSuccess, "sink": models.RunStatusSuccess}, outputExpected: true,
			wantEvents:            []models.RunEventType{models.RunEventCreated, models.AttemptStarted, models.AttemptCompleted, models.AttemptStarted, models.AttemptCompleted, models.AttemptStarted, models.AttemptCompleted},
			wantRecoveredStatus:   models.RunStatusSuccess,
			wantRecoveryEventType: models.RunEventTerminal,
			wantRecoveryScanned:   1},
		{name: "after terminal persistence", point: crashPointAfterRunTerminalPersist, runStatus: models.RunStatusSuccess, nodeStatuses: map[string]models.RunStatus{"source": models.RunStatusSuccess, "condition": models.RunStatusSuccess, "sink": models.RunStatusSuccess}, outputExpected: true,
			wantEvents:            []models.RunEventType{models.RunEventCreated, models.AttemptStarted, models.AttemptCompleted, models.AttemptStarted, models.AttemptCompleted, models.AttemptStarted, models.AttemptCompleted, models.RunEventTerminal},
			wantRecoveredStatus:   models.RunStatusSuccess,
			wantRecoveryEventType: "", // already terminal before recovery ran — nothing new to append
			wantRecoveryScanned:   0},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			dbPath := filepath.Join(dir, "crash-harness.db")
			inputPath := filepath.Join(dir, "input.csv")
			outputPath := filepath.Join(dir, "output.csv")
			if err := os.WriteFile(inputPath, []byte("id,name\n1,brokoli\n"), 0o600); err != nil {
				t.Fatalf("write input: %v", err)
			}

			cmd := exec.Command(os.Args[0], "-test.run=^TestCrashRecoveryBaselineProcess$")
			cmd.Env = append(os.Environ(),
				"BROKOLI_ENABLE_TEST_FAILPOINTS=1",
				"BROKOLI_TEST_CRASH_POINT="+tc.point,
				"BROKOLI_CRASH_HARNESS_DB="+dbPath,
				"BROKOLI_CRASH_HARNESS_INPUT="+inputPath,
				"BROKOLI_CRASH_HARNESS_OUTPUT="+outputPath,
			)
			err := cmd.Run()
			var exitErr *exec.ExitError
			if !errors.As(err, &exitErr) || exitErr.ExitCode() != testCrashExitCode {
				t.Fatalf("crash subprocess error = %v, want exit code %d", err, testCrashExitCode)
			}

			assertCrashBaseline(t, dbPath, tc.runStatus, tc.nodeStatuses, outputPath, tc.outputExpected, tc.wantEvents)

			if tc.runStatus != "" {
				if tc.liveLeaseNodeID != "" {
					assertCrashRecoveryDefersThenReclaims(t, dbPath, tc.liveLeaseNodeID, tc.wantRecoveredStatus, tc.wantRecoveryEventType, tc.wantRecoveryScanned)
				} else {
					assertCrashRecovery(t, dbPath, tc.wantRecoveredStatus, tc.wantRecoveryEventType, tc.wantRecoveryScanned)
				}
			}
		})
	}
}

// TestCrashRecoveryBaselineProcess runs only in the subprocess spawned above.
func TestCrashRecoveryBaselineProcess(t *testing.T) {
	if os.Getenv("BROKOLI_ENABLE_TEST_FAILPOINTS") != "1" {
		return
	}

	dbPath := os.Getenv("BROKOLI_CRASH_HARNESS_DB")
	inputPath := os.Getenv("BROKOLI_CRASH_HARNESS_INPUT")
	outputPath := os.Getenv("BROKOLI_CRASH_HARNESS_OUTPUT")
	if dbPath == "" || inputPath == "" || outputPath == "" {
		t.Fatal("crash harness paths must be configured")
	}

	s, err := store.NewSQLiteStore(dbPath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer s.Close()

	now := time.Now().UTC()
	pipeline := &models.Pipeline{
		ID: "crash-harness", Name: "Crash Harness", Enabled: true,
		CreatedAt: now, UpdatedAt: now,
		Nodes: []models.Node{
			{ID: "source", Type: models.NodeTypeSourceFile, Name: "Source", Config: map[string]interface{}{"path": inputPath}},
			{ID: "condition", Type: models.NodeTypeCondition, Name: "Condition", Config: map[string]interface{}{}},
			{ID: "sink", Type: models.NodeTypeSinkFile, Name: "Sink", Config: map[string]interface{}{"path": outputPath, "format": "csv"}},
		},
		Edges: []models.Edge{{From: "source", To: "condition"}, {From: "condition", To: "sink"}},
	}
	if err := s.CreatePipeline(pipeline); err != nil {
		t.Fatalf("create pipeline: %v", err)
	}
	if _, err := drainEngineOnCleanup(t, NewEngine(s)).RunPipeline(pipeline.ID); err != nil {
		t.Fatalf("run pipeline before injected crash: %v", err)
	}
	t.Fatal("crash point was not reached")
}

func assertCrashBaseline(t *testing.T, dbPath string, expectedRun models.RunStatus, expectedNodes map[string]models.RunStatus, outputPath string, outputExpected bool, wantEvents []models.RunEventType) {
	t.Helper()

	s, err := store.NewSQLiteStore(dbPath)
	if err != nil {
		t.Fatalf("reopen store after crash: %v", err)
	}
	defer s.Close()

	runs, err := s.ListRunsByPipeline("crash-harness", 10)
	if err != nil {
		t.Fatalf("list runs: %v", err)
	}
	if expectedRun == "" {
		if len(runs) != 0 {
			t.Fatalf("runs after pre-create crash = %d, want 0", len(runs))
		}
	} else {
		if len(runs) != 1 {
			t.Fatalf("runs after crash = %d, want 1", len(runs))
		}
		if runs[0].Status != expectedRun {
			t.Errorf("run status = %q, want %q", runs[0].Status, expectedRun)
		}

		nodeRuns, err := s.ListNodeRunsByRun(runs[0].ID)
		if err != nil {
			t.Fatalf("list node runs: %v", err)
		}
		var got map[string]models.RunStatus
		if len(nodeRuns) > 0 {
			got = make(map[string]models.RunStatus, len(nodeRuns))
			for _, nodeRun := range nodeRuns {
				got[nodeRun.NodeID] = nodeRun.Status
			}
		}
		if !reflect.DeepEqual(got, expectedNodes) {
			t.Errorf("node states = %v, want %v", got, expectedNodes)
		}

		assertCrashEvents(t, s, runs[0].ID, wantEvents)
	}

	_, err = os.Stat(outputPath)
	if outputExpected && err != nil {
		t.Errorf("output missing after sink side effect: %v", err)
	}
	if !outputExpected && !errors.Is(err, os.ErrNotExist) {
		t.Errorf("output exists before sink side effect")
	}
}

// assertCrashEvents checks that exactly the run_events the harness expects
// to have been durably appended before this crash point survived the crash
// — the same "what transitions happened before the crash point" question
// assertCrashBaseline already asks of runs/node_runs, now asked of the
// event log dual-written alongside it (issue #6).
func assertCrashEvents(t *testing.T, s *store.SQLiteStore, runID string, wantEvents []models.RunEventType) {
	t.Helper()

	events, err := s.ListEventsByRun(runID)
	if err != nil {
		t.Fatalf("list events: %v", err)
	}
	gotTypes := make([]models.RunEventType, len(events))
	for i, e := range events {
		gotTypes[i] = e.EventType
		if e.RunID != runID {
			t.Errorf("event[%d].RunID = %q, want %q", i, e.RunID, runID)
		}
	}
	if !reflect.DeepEqual(gotTypes, wantEvents) {
		t.Errorf("run_events types = %v, want %v", gotTypes, wantEvents)
	}
}

// assertCrashRecoveryDefersThenReclaims covers a crash point where the
// crashed node's own attempt still holds a live, unexpired
// store.ExecutionAttemptStore lease (Tnsor-Labs/brokoli#90 M3's claim/lease
// wiring) at the exact moment the process dies. reconcileExecutionAttempts
// (engine/recovery.go) correctly refuses to touch the run on a first
// recovery pass in that state — a live lease might still belong to a
// genuinely live process elsewhere, the same guarantee
// TestRecoverNonTerminalRunsDefersToLiveLease already proves directly
// against the store. This forces the lease to look expired (RenewLease with
// a negative duration, mirroring that same test's ClaimAttempt trick — the
// attempt here is already claimed, so RenewLease is the right call) and
// then hands off to assertCrashRecovery for the now-familiar
// reclaim-and-reach-terminal assertion.
func assertCrashRecoveryDefersThenReclaims(t *testing.T, dbPath, liveLeaseNodeID string, wantStatus models.RunStatus, wantEventType models.RunEventType, wantScanned int) {
	t.Helper()

	s, err := store.NewSQLiteStore(dbPath)
	if err != nil {
		t.Fatalf("reopen store for first recovery pass: %v", err)
	}

	runs, err := s.ListRunsByPipeline("crash-harness", 10)
	if err != nil {
		t.Fatalf("list runs before first recovery pass: %v", err)
	}
	if len(runs) != 1 {
		t.Fatalf("runs before first recovery pass = %d, want 1", len(runs))
	}
	runID := runs[0].ID

	eng := drainEngineOnCleanup(t, NewEngine(s))
	first, err := eng.RecoverNonTerminalRuns()
	if err != nil {
		t.Fatalf("first RecoverNonTerminalRuns: %v", err)
	}
	if first.RunsDeferred != 1 || first.RunsReconciled != 0 || first.RunsFailed != 0 {
		t.Fatalf("first recovery summary = %+v, want 1 deferred, 0 reconciled, 0 failed (live lease on node %q must not be touched)", first, liveLeaseNodeID)
	}
	stillRunning, err := s.GetRun(runID)
	if err != nil {
		t.Fatalf("GetRun after first recovery pass: %v", err)
	}
	if stillRunning.Status != models.RunStatusRunning {
		t.Fatalf("run status after first recovery pass = %q, want unchanged running (live lease must defer, not reclaim)", stillRunning.Status)
	}

	attempt, err := s.GetExecutionAttempt(runID, liveLeaseNodeID, "", 0)
	if err != nil {
		t.Fatalf("GetExecutionAttempt(%s, %s, 0): %v", runID, liveLeaseNodeID, err)
	}
	if ok, err := s.RenewLease(runID, liveLeaseNodeID, "", 0, attempt.ClaimedBy, attempt.FencingGeneration, -time.Second); err != nil || !ok {
		t.Fatalf("force-expire lease via RenewLease: ok=%v err=%v", ok, err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("close store between recovery passes: %v", err)
	}

	// The lease now looks expired; the next pass (reopened fresh, exactly
	// as assertCrashRecovery's own doc comment already establishes for a
	// real process restart) reclaims it and proceeds through the same
	// event-log reconciliation this whole harness otherwise exercises —
	// including its own internal idempotency check as the pass after that.
	assertCrashRecovery(t, dbPath, wantStatus, wantEventType, wantScanned)
}

// assertCrashRecovery is the phase assertCrashBaseline explicitly deferred
// (per its original doc comment): reopen the post-crash database exactly as
// cmd/serve.go does on the next boot, run engine.RecoverNonTerminalRuns
// against it, and assert the run this failpoint left behind reaches a
// terminal state — issue #9's core acceptance criterion, "no run remains
// permanently running merely because its process died" — rather than
// remaining stuck non-terminal forever as it did before this PR (there was
// previously no code path that ever touched it again).
//
// It also proves the idempotency acceptance criterion directly: running
// recovery a second time against the now-terminal run must be a no-op.
func assertCrashRecovery(t *testing.T, dbPath string, wantStatus models.RunStatus, wantEventType models.RunEventType, wantScanned int) {
	t.Helper()

	s, err := store.NewSQLiteStore(dbPath)
	if err != nil {
		t.Fatalf("reopen store for recovery: %v", err)
	}
	defer s.Close()

	eng := drainEngineOnCleanup(t, NewEngine(s))
	summary, err := eng.RecoverNonTerminalRuns()
	if err != nil {
		t.Fatalf("RecoverNonTerminalRuns: %v", err)
	}
	if summary.RunsScanned != wantScanned {
		t.Errorf("RecoverNonTerminalRuns scanned %d non-terminal run(s), want %d: %+v", summary.RunsScanned, wantScanned, summary)
	}

	runs, err := s.ListRunsByPipeline("crash-harness", 10)
	if err != nil {
		t.Fatalf("list runs after recovery: %v", err)
	}
	if len(runs) != 1 {
		t.Fatalf("runs after recovery = %d, want 1", len(runs))
	}
	got := runs[0]
	if got.Status != wantStatus {
		t.Errorf("run status after recovery = %q, want %q", got.Status, wantStatus)
	}
	if got.Status != models.RunStatusSuccess && got.Status != models.RunStatusFailed && got.Status != models.RunStatusCancelled && got.Status != models.RunStatusBlocked {
		t.Errorf("run status after recovery = %q, want a terminal status — a run must never remain permanently non-terminal merely because its process died", got.Status)
	}

	if wantEventType != "" {
		events, err := s.ListEventsByRun(got.ID)
		if err != nil {
			t.Fatalf("list events after recovery: %v", err)
		}
		found := false
		for _, e := range events {
			if e.EventType == wantEventType {
				found = true
				break
			}
		}
		if !found {
			gotTypes := make([]models.RunEventType, len(events))
			for i, e := range events {
				gotTypes[i] = e.EventType
			}
			t.Errorf("events after recovery = %v, want to include %q", gotTypes, wantEventType)
		}
	}

	// Idempotency (issue #9's third acceptance criterion): a second
	// recovery pass against the now-terminal run must find nothing left to
	// do — ListNonTerminalRuns excludes it once it's terminal.
	second, err := eng.RecoverNonTerminalRuns()
	if err != nil {
		t.Fatalf("second RecoverNonTerminalRuns: %v", err)
	}
	if second.RunsScanned != 0 {
		t.Errorf("second RecoverNonTerminalRuns scanned %d run(s), want 0 (recovery is not idempotent)", second.RunsScanned)
	}
	again, err := s.GetRun(got.ID)
	if err != nil {
		t.Fatalf("GetRun after second recovery pass: %v", err)
	}
	if again.Status != got.Status {
		t.Errorf("run status changed from %q to %q across a second recovery pass — recovery is not idempotent", got.Status, again.Status)
	}
}
