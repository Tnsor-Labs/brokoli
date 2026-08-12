package engine

// brokoli#138: retry_backoff was accepted into the compiled config and
// documented, but the runner's retry loop hardcoded an exponential formula
// and never read it -- ComputeBackoff (engine/retry.go) already implemented
// exponential/linear/fixed correctly, it just had no caller. This file
// proves the fix reaches the real retry loop (engine/runner.go's
// executeNode), not just that ComputeBackoff computes the right numbers in
// isolation -- retry_test.go's TestComputeBackoff_* already passed before
// the fix and would keep passing even if the wiring were still missing.
//
// Asserting on wall-clock sleep timing would be flaky under CI scheduling
// jitter, so these read back the COMPUTED delay the runner persisted to
// each models.RetryScheduled run event (RunEventPayload.BackoffMs) instead
// of measuring real elapsed time -- exact, deterministic, no tolerance
// windows needed.

import (
	"context"
	"fmt"
	"path/filepath"
	"sync"
	"testing"

	"github.com/Tnsor-Labs/brokoli/extensions"
	"github.com/Tnsor-Labs/brokoli/models"
	"github.com/Tnsor-Labs/brokoli/store"
)

// countingFlakyExecutor fails its first failFirstN calls, then succeeds.
type countingFlakyExecutor struct {
	mu         sync.Mutex
	nodeType   string
	failFirstN int
	calls      int
}

func (e *countingFlakyExecutor) Name() string                  { return "test-counting-flaky" }
func (e *countingFlakyExecutor) CanHandle(nt string) bool       { return nt == e.nodeType }
func (e *countingFlakyExecutor) Execute(ctx extensions.ExecutionContext) (*extensions.ExecutionResult, error) {
	e.mu.Lock()
	e.calls++
	callNum := e.calls
	e.mu.Unlock()

	if callNum <= e.failFirstN {
		return nil, fmt.Errorf("simulated failure on call %d", callNum)
	}
	return &extensions.ExecutionResult{}, nil
}

// runFlakyRetryPipeline runs source_file -> a flaky node configured with the
// given retry_backoff/retry_delay/max_retries, then returns the
// RetryScheduled events' BackoffMs values it persisted, in attempt order.
func runFlakyRetryPipeline(t *testing.T, dbName, nodeType string, failFirstN int, retryBackoff string, retryDelayMs float64) []int64 {
	t.Helper()
	dir := t.TempDir()
	s, err := store.NewSQLiteStore(filepath.Join(dir, dbName))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	exec := &countingFlakyExecutor{nodeType: nodeType, failFirstN: failFirstN}
	eng := NewEngine(s)
	defer eng.Close(context.Background())
	eng.Executors = []extensions.NodeExecutor{exec}

	csv := execCtxSourceCSV(t)
	pipeline := &models.Pipeline{
		ID: "p-" + dbName, Name: "Flaky " + dbName, Enabled: true,
		Nodes: []models.Node{
			{ID: "source", Type: models.NodeTypeSourceFile, Name: "Source", Config: map[string]interface{}{"path": csv, "format": "csv"}},
			{ID: "flaky", Type: models.NodeType(nodeType), Name: "Flaky", Config: map[string]interface{}{
				"max_retries":   float64(failFirstN),
				"retry_delay":   retryDelayMs,
				"retry_backoff": retryBackoff,
			}},
		},
		Edges: []models.Edge{{From: "source", To: "flaky"}},
	}
	if err := s.CreatePipeline(pipeline); err != nil {
		t.Fatal(err)
	}

	run, err := eng.RunPipeline(pipeline.ID)
	if err != nil && run == nil {
		t.Fatalf("run pipeline: %v", err)
	}
	if run.Status != models.RunStatusSuccess {
		t.Fatalf("run status = %s, want success (error: %s)", run.Status, run.Error)
	}

	events, err := s.ListEventsByRun(run.ID)
	if err != nil {
		t.Fatalf("list run events: %v", err)
	}
	var backoffs []int64
	for _, e := range events {
		if e.EventType == models.RetryScheduled && e.NodeID == "flaky" {
			backoffs = append(backoffs, e.Payload.BackoffMs)
		}
	}
	return backoffs
}

// TestRetryBackoff_FixedComputesFlatDelays proves retry_backoff="fixed"
// produces the SAME persisted delay on every attempt. Before the fix, the
// runner always used its hardcoded exponential formula regardless of this
// config key, so the persisted delays would double each attempt instead.
func TestRetryBackoff_FixedComputesFlatDelays(t *testing.T) {
	backoffs := runFlakyRetryPipeline(t, "backoff-fixed.db", "flaky-fixed", 3, "fixed", 100)
	want := []int64{100, 100, 100}
	if len(backoffs) != len(want) {
		t.Fatalf("got %d RetryScheduled event(s) %v, want %d", len(backoffs), backoffs, len(want))
	}
	for i, b := range backoffs {
		if b != want[i] {
			t.Errorf("attempt %d: persisted BackoffMs = %d, want %d (fixed backoff -- if this grows, "+
				"the runner is still hardcoding exponential regardless of retry_backoff)", i+1, b, want[i])
		}
	}
}

// TestRetryBackoff_LinearComputesAdditiveDelays proves retry_backoff="linear"
// grows by a constant increment per attempt (base, 2*base, 3*base), not by
// doubling (base, 2*base, 4*base) the way the old hardcoded formula did.
func TestRetryBackoff_LinearComputesAdditiveDelays(t *testing.T) {
	backoffs := runFlakyRetryPipeline(t, "backoff-linear.db", "flaky-linear", 3, "linear", 50)
	want := []int64{50, 100, 150} // linear: base*attempt
	if len(backoffs) != len(want) {
		t.Fatalf("got %d RetryScheduled event(s) %v, want %d", len(backoffs), backoffs, len(want))
	}
	for i, b := range backoffs {
		if b != want[i] {
			t.Errorf("attempt %d: persisted BackoffMs = %d, want %d (linear backoff -- exponential would "+
				"give %d)", i+1, b, want[i], int64(50)<<uint(i))
		}
	}
}

// TestRetryBackoff_ExponentialUnchanged is a regression guard: the default
// (and the pre-existing documented) behavior -- no retry_backoff set --
// must keep doubling exactly as it always did.
func TestRetryBackoff_ExponentialUnchanged(t *testing.T) {
	backoffs := runFlakyRetryPipeline(t, "backoff-exponential.db", "flaky-exp", 3, "exponential", 25)
	want := []int64{25, 50, 100} // exponential: base * 2^(attempt-1)
	if len(backoffs) != len(want) {
		t.Fatalf("got %d RetryScheduled event(s) %v, want %d", len(backoffs), backoffs, len(want))
	}
	for i, b := range backoffs {
		if b != want[i] {
			t.Errorf("attempt %d: persisted BackoffMs = %d, want %d", i+1, b, want[i])
		}
	}
}
