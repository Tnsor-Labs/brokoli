package store

import (
	"strconv"
	"testing"
	"time"

	"github.com/Tnsor-Labs/brokoli/models"
	"github.com/Tnsor-Labs/brokoli/pkg/common"
)

// seedRunWithStatus creates a pipeline (if it doesn't already exist — tests
// may seed several runs against the same pipeline) and a run row with the
// given status directly, so tests can freely construct pending/running/
// terminal fixtures without executing a real pipeline — mirrors
// seedRunForEvents/seedRunForAttempts in the neighboring test files.
func seedRunWithStatus(t *testing.T, s *SQLiteStore, pipelineID, runID string, status models.RunStatus) {
	t.Helper()
	now := time.Now().UTC().Truncate(time.Millisecond)
	if _, err := s.GetPipeline(pipelineID); err != nil {
		if err := s.CreatePipeline(&models.Pipeline{
			ID: pipelineID, Name: "Recovery Test Pipeline", CreatedAt: now, UpdatedAt: now,
		}); err != nil {
			t.Fatalf("CreatePipeline: %v", err)
		}
	}
	r := &models.Run{ID: runID, PipelineID: pipelineID, Status: status}
	if status == models.RunStatusRunning || status == models.RunStatusPending {
		r.StartedAt = &now
	}
	if err := s.CreateRun(r); err != nil {
		t.Fatalf("CreateRun(%s, %s): %v", runID, status, err)
	}
}

func TestListNonTerminalRunsExcludesTerminalStatuses(t *testing.T) {
	s := newTestStore(t)
	seedRunWithStatus(t, s, "pipe-nonterm", "run-pending", models.RunStatusPending)
	seedRunWithStatus(t, s, "pipe-nonterm", "run-running", models.RunStatusRunning)
	seedRunWithStatus(t, s, "pipe-nonterm", "run-success", models.RunStatusSuccess)
	seedRunWithStatus(t, s, "pipe-nonterm", "run-failed", models.RunStatusFailed)
	seedRunWithStatus(t, s, "pipe-nonterm", "run-cancelled", models.RunStatusCancelled)
	seedRunWithStatus(t, s, "pipe-nonterm", "run-blocked", models.RunStatusBlocked)

	runs, hasNext, err := s.ListNonTerminalRuns("", 100)
	if err != nil {
		t.Fatalf("ListNonTerminalRuns: %v", err)
	}
	if hasNext {
		t.Fatalf("hasNext = true, want false (only 2 non-terminal rows exist, well under limit)")
	}
	if len(runs) != 2 {
		t.Fatalf("ListNonTerminalRuns returned %d runs, want 2 (pending + running only): %+v", len(runs), runs)
	}
	got := map[string]models.RunStatus{}
	for _, r := range runs {
		got[r.ID] = r.Status
	}
	if got["run-pending"] != models.RunStatusPending {
		t.Errorf("run-pending status = %q, want pending", got["run-pending"])
	}
	if got["run-running"] != models.RunStatusRunning {
		t.Errorf("run-running status = %q, want running", got["run-running"])
	}
	for _, terminalID := range []string{"run-success", "run-failed", "run-cancelled", "run-blocked"} {
		if _, ok := got[terminalID]; ok {
			t.Errorf("ListNonTerminalRuns included terminal run %s, want excluded", terminalID)
		}
	}
}

// TestListNonTerminalRunsCursorPaginationSurvivesMutation is the specific
// correctness property that ruled out plain offset-based pagination for
// this method: a caller (engine.RecoverNonTerminalRuns) resolves runs to
// terminal statuses AS it pages through them, which would corrupt an
// offset-based scan (later pages would skip over unprocessed rows that
// shifted into the gap left by earlier ones). Keyset (afterID) pagination
// must be immune to this: each row is visited exactly once regardless of
// what happens to earlier rows in the same scan.
func TestListNonTerminalRunsCursorPaginationSurvivesMutation(t *testing.T) {
	s := newTestStore(t)
	const n = 5
	ids := make([]string, n)
	for i := 0; i < n; i++ {
		id := common.NewID()
		ids[i] = id
		seedRunWithStatus(t, s, "pipe-cursor", id, models.RunStatusRunning)
	}

	// Page through with limit=2, resolving each run to terminal (success)
	// immediately after it's returned — simulating what recovery actually
	// does — before fetching the next page.
	seen := map[string]bool{}
	afterID := ""
	for {
		page, hasNext, err := s.ListNonTerminalRuns(afterID, 2)
		if err != nil {
			t.Fatalf("ListNonTerminalRuns: %v", err)
		}
		if len(page) == 0 {
			break
		}
		for _, r := range page {
			if seen[r.ID] {
				t.Fatalf("run %s returned twice across pages", r.ID)
			}
			seen[r.ID] = true
			// Resolve it to terminal, mutating the underlying non-terminal
			// set mid-scan — the exact hazard keyset pagination must survive.
			r.Status = models.RunStatusSuccess
			if err := s.UpdateRun(&r); err != nil {
				t.Fatalf("UpdateRun: %v", err)
			}
			afterID = r.ID
		}
		if !hasNext {
			break
		}
	}

	if len(seen) != n {
		t.Fatalf("visited %d runs across pages, want %d: %v", len(seen), n, seen)
	}
	for _, id := range ids {
		if !seen[id] {
			t.Errorf("run %s was never visited", id)
		}
	}

	// Every run must now be terminal — confirms nothing was skipped.
	remaining, _, err := s.ListNonTerminalRuns("", 100)
	if err != nil {
		t.Fatalf("ListNonTerminalRuns (final check): %v", err)
	}
	if len(remaining) != 0 {
		t.Fatalf("ListNonTerminalRuns after resolving all runs = %d, want 0: %+v", len(remaining), remaining)
	}
}

func TestListNonTerminalRunsPaginatesWithHasNext(t *testing.T) {
	s := newTestStore(t)
	for i := 0; i < 3; i++ {
		seedRunWithStatus(t, s, "pipe-page", common.NewID(), models.RunStatusRunning)
	}

	page1, hasNext1, err := s.ListNonTerminalRuns("", 2)
	if err != nil {
		t.Fatalf("ListNonTerminalRuns page1: %v", err)
	}
	if len(page1) != 2 {
		t.Fatalf("page1 len = %d, want 2", len(page1))
	}
	if !hasNext1 {
		t.Fatal("hasNext1 = false, want true (3 rows exist, page size 2)")
	}

	page2, hasNext2, err := s.ListNonTerminalRuns(page1[len(page1)-1].ID, 2)
	if err != nil {
		t.Fatalf("ListNonTerminalRuns page2: %v", err)
	}
	if len(page2) != 1 {
		t.Fatalf("page2 len = %d, want 1", len(page2))
	}
	if hasNext2 {
		t.Fatal("hasNext2 = true, want false (exhausted)")
	}
}

func TestListExecutionAttemptsByRunReturnsAllStatusesAndNodes(t *testing.T) {
	s := newTestStore(t)
	seedRunForAttempts(t, s, "pipe-list-attempts", "run-list-attempts")

	createAttempt(t, s, "run-list-attempts", "node-a", 0)
	createAttempt(t, s, "run-list-attempts", "node-b", 0)
	createAttempt(t, s, "run-list-attempts", "node-b", 1) // a retry of node-b

	gen, ok, err := s.ClaimAttempt("run-list-attempts", "node-a", "", 0, "worker-1", time.Hour)
	if err != nil || !ok {
		t.Fatalf("ClaimAttempt: ok=%v err=%v", ok, err)
	}
	if err := s.CompleteAttempt("run-list-attempts", "node-a", "", 0, gen); err != nil {
		t.Fatalf("CompleteAttempt: %v", err)
	}

	// A different run's attempts must not leak in.
	seedRunForAttempts(t, s, "pipe-list-attempts-2", "run-list-attempts-other")
	createAttempt(t, s, "run-list-attempts-other", "node-z", 0)

	attempts, err := s.ListExecutionAttemptsByRun("run-list-attempts")
	if err != nil {
		t.Fatalf("ListExecutionAttemptsByRun: %v", err)
	}
	if len(attempts) != 3 {
		t.Fatalf("ListExecutionAttemptsByRun returned %d attempts, want 3: %+v", len(attempts), attempts)
	}
	byKey := map[string]models.ExecutionAttempt{}
	for _, a := range attempts {
		if a.RunID != "run-list-attempts" {
			t.Errorf("attempt run_id = %q, want run-list-attempts", a.RunID)
		}
		byKey[a.NodeID+":"+strconv.Itoa(a.Attempt)] = a
	}
	if got := byKey["node-a:0"]; got.Status != models.AttemptStatusCompleted {
		t.Errorf("node-a:0 status = %q, want completed", got.Status)
	}
	if got := byKey["node-b:0"]; got.Status != models.AttemptStatusQueued {
		t.Errorf("node-b:0 status = %q, want queued", got.Status)
	}
	if got := byKey["node-b:1"]; got.Status != models.AttemptStatusQueued {
		t.Errorf("node-b:1 status = %q, want queued", got.Status)
	}
}
