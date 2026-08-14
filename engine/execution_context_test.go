package engine

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/Tnsor-Labs/brokoli/extensions"
	"github.com/Tnsor-Labs/brokoli/models"
	"github.com/Tnsor-Labs/brokoli/store"
)

// execCtxSourceCSV writes a tiny CSV file and returns its path. Every
// pipeline in this file needs a real source node (ValidatePipeline requires
// one), so the node under test sits downstream of this source.
func execCtxSourceCSV(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "in.csv")
	if err := os.WriteFile(path, []byte("id,name\n1,brokoli\n"), 0o600); err != nil {
		t.Fatalf("write csv: %v", err)
	}
	return path
}

// capturingExecutor is a test-only extensions.NodeExecutor that records the
// full extensions.ExecutionContext of every Execute call it receives, so
// tests can assert on the Attempt/IdempotencyKey/Context fields the engine
// populates at the dispatch site (engine/runner.go's runNodeLogic), not just
// on the final run outcome.
type capturingExecutor struct {
	mu       sync.Mutex
	nodeType string
	calls    []extensions.ExecutionContext
	// ctxErrAtCall records ctx.Context.Err(), read synchronously inside
	// Execute — i.e. while the context was actually handed to this
	// executor — since reading it later (after the whole run, and thus
	// the run's context, has finished) would always see it cancelled.
	ctxErrAtCall []error
	// failFirstN causes the first N calls to fail; subsequent calls succeed.
	failFirstN int
}

func (e *capturingExecutor) Name() string { return "test-capturing" }

func (e *capturingExecutor) CanHandle(nodeType string) bool { return nodeType == e.nodeType }

func (e *capturingExecutor) Execute(ctx extensions.ExecutionContext) (*extensions.ExecutionResult, error) {
	var ctxErr error
	if ctx.Context != nil {
		ctxErr = ctx.Context.Err()
	} else {
		ctxErr = errors.New("ExecutionContext.Context is nil")
	}

	e.mu.Lock()
	e.calls = append(e.calls, ctx)
	e.ctxErrAtCall = append(e.ctxErrAtCall, ctxErr)
	callNum := len(e.calls)
	e.mu.Unlock()

	if callNum <= e.failFirstN {
		return nil, fmt.Errorf("simulated failure on call %d", callNum)
	}
	return &extensions.ExecutionResult{}, nil
}

func (e *capturingExecutor) snapshot() []extensions.ExecutionContext {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make([]extensions.ExecutionContext, len(e.calls))
	copy(out, e.calls)
	return out
}

func (e *capturingExecutor) ctxErrsAtCall() []error {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make([]error, len(e.ctxErrAtCall))
	copy(out, e.ctxErrAtCall)
	return out
}

// newExecCtxTestEngine builds a minimal SQLite-backed Engine for exercising
// the real Runner.executeNode/runNodeLogic dispatch path end to end.
func newExecCtxTestEngine(t *testing.T) (*Engine, *store.SQLiteStore) {
	t.Helper()
	dir := t.TempDir()
	s, err := store.NewSQLiteStore(filepath.Join(dir, "execctx.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return drainEngineOnCleanup(t, NewEngine(s)), s
}

// TestExecutionContext_AttemptAndIdempotencyKeyMatchEngineTracking proves
// that extensions.ExecutionContext.Attempt and .IdempotencyKey, as observed
// by an external NodeExecutor, are populated from the SAME attempt counter
// the engine durably tracks for that node's execution (models.NodeRun,
// persisted via store.CreateNodeRun/UpdateNodeRun on every attempt) — not
// some independently-derived or stale value.
func TestExecutionContext_AttemptAndIdempotencyKeyMatchEngineTracking(t *testing.T) {
	eng, s := newExecCtxTestEngine(t)

	exec := &capturingExecutor{nodeType: "flaky", failFirstN: 2} // fails attempts 0 and 1, succeeds on attempt 2
	eng.Executors = []extensions.NodeExecutor{exec}

	csvPath := execCtxSourceCSV(t)
	pipeline := &models.Pipeline{
		ID: "p-execctx-attempt", Name: "ExecCtx Attempt", Enabled: true,
		Nodes: []models.Node{
			{ID: "source", Type: models.NodeTypeSourceFile, Name: "Source", Config: map[string]interface{}{"path": csvPath, "format": "csv"}},
			{ID: "flaky", Type: models.NodeType("flaky"), Name: "Flaky", Config: map[string]interface{}{
				"max_retries": float64(2),
				"retry_delay": float64(1), // ms — keep the test fast
			}},
		},
		Edges: []models.Edge{{From: "source", To: "flaky"}},
	}
	if err := s.CreatePipeline(pipeline); err != nil {
		t.Fatalf("create pipeline: %v", err)
	}

	run, err := eng.RunPipeline(pipeline.ID)
	if err != nil && run == nil {
		t.Fatalf("run pipeline: %v", err)
	}
	if run.Status != models.RunStatusSuccess {
		t.Fatalf("run status = %s, want success (error: %s)", run.Status, run.Error)
	}

	calls := exec.snapshot()
	if len(calls) != 3 {
		t.Fatalf("executor received %d call(s), want 3 (2 failures + 1 success)", len(calls))
	}

	// Cross-check against the engine's own durable attempt records — the
	// actual source of truth for "which attempt is this" (Tnsor-Labs/brokoli#7).
	allNodeRuns, err := s.ListNodeRunsByRun(run.ID)
	if err != nil {
		t.Fatalf("list node runs: %v", err)
	}
	var nodeRuns []models.NodeRun
	for _, nr := range allNodeRuns {
		if nr.NodeID == "flaky" {
			nodeRuns = append(nodeRuns, nr)
		}
	}
	sort.Slice(nodeRuns, func(i, j int) bool { return nodeRuns[i].Attempt < nodeRuns[j].Attempt })
	if len(nodeRuns) != 3 {
		t.Fatalf("store recorded %d NodeRun attempt(s) for node %q, want 3", len(nodeRuns), "flaky")
	}

	for i, call := range calls {
		wantAttempt := nodeRuns[i].Attempt
		if call.Attempt != wantAttempt {
			t.Errorf("call %d: ExecutionContext.Attempt = %d, want %d (engine's models.NodeRun.Attempt)", i, call.Attempt, wantAttempt)
		}
		if call.Attempt != i {
			t.Errorf("call %d: ExecutionContext.Attempt = %d, want %d", i, call.Attempt, i)
		}
		if call.RunID != run.ID {
			t.Errorf("call %d: ExecutionContext.RunID = %q, want %q", i, call.RunID, run.ID)
		}
		if call.NodeID != "flaky" {
			t.Errorf("call %d: ExecutionContext.NodeID = %q, want %q", i, call.NodeID, "flaky")
		}

		wantKey := fmt.Sprintf("%s:%s:%d", run.ID, "flaky", i)
		if call.IdempotencyKey != wantKey {
			t.Errorf("call %d: ExecutionContext.IdempotencyKey = %q, want %q", i, call.IdempotencyKey, wantKey)
		}
	}

	// Every attempt's key must be distinct — a redispatch of a DIFFERENT
	// attempt must never collide with another attempt's idempotency key.
	seen := make(map[string]bool)
	for _, call := range calls {
		if seen[call.IdempotencyKey] {
			t.Fatalf("duplicate IdempotencyKey %q across distinct attempts", call.IdempotencyKey)
		}
		seen[call.IdempotencyKey] = true
	}
}

// TestExecutionContext_ContextIsUsable proves ExecutionContext.Context is a
// real, non-nil context.Context — not the struct's zero value — and that it
// carries a deadline (derived from the node's timeout), matching its doc
// comment.
func TestExecutionContext_ContextIsUsable(t *testing.T) {
	eng, s := newExecCtxTestEngine(t)

	exec := &capturingExecutor{nodeType: "flaky"}
	eng.Executors = []extensions.NodeExecutor{exec}

	csvPath := execCtxSourceCSV(t)
	pipeline := &models.Pipeline{
		ID: "p-execctx-context", Name: "ExecCtx Context", Enabled: true,
		Nodes: []models.Node{
			{ID: "source", Type: models.NodeTypeSourceFile, Name: "Source", Config: map[string]interface{}{"path": csvPath, "format": "csv"}},
			{ID: "flaky", Type: models.NodeType("flaky"), Name: "Flaky", Config: map[string]interface{}{
				"timeout": float64(60), // seconds
			}},
		},
		Edges: []models.Edge{{From: "source", To: "flaky"}},
	}
	if err := s.CreatePipeline(pipeline); err != nil {
		t.Fatalf("create pipeline: %v", err)
	}

	run, err := eng.RunPipeline(pipeline.ID)
	if err != nil && run == nil {
		t.Fatalf("run pipeline: %v", err)
	}
	if run.Status != models.RunStatusSuccess {
		t.Fatalf("run status = %s, want success (error: %s)", run.Status, run.Error)
	}

	calls := exec.snapshot()
	if len(calls) != 1 {
		t.Fatalf("executor received %d call(s), want 1", len(calls))
	}

	ctx := calls[0].Context
	if ctx == nil {
		t.Fatal("ExecutionContext.Context is nil, want a real context.Context")
	}
	// ctxErrAtCall was captured synchronously inside Execute, i.e. while the
	// run (and thus its context) was still in flight — checking ctx.Err()
	// now, after RunPipeline has returned and the run's context has been
	// cancelled by Runner.Execute's own cleanup, would always see it done.
	errsAtCall := exec.ctxErrsAtCall()
	if len(errsAtCall) != 1 {
		t.Fatalf("captured %d ctxErrAtCall entries, want 1", len(errsAtCall))
	}
	if errsAtCall[0] != nil {
		t.Fatalf("ExecutionContext.Context was already done during Execute: %v", errsAtCall[0])
	}
	deadline, ok := ctx.Deadline()
	if !ok {
		t.Fatal("ExecutionContext.Context has no deadline, want one derived from the node's configured timeout")
	}
	if time.Until(deadline) > 60*time.Second || time.Until(deadline) <= 0 {
		t.Fatalf("ExecutionContext.Context deadline = %v from now, want roughly <= 60s and in the future", time.Until(deadline))
	}
}

// blockingUntilCancelExecutor is a fake extensions.NodeExecutor that signals
// startedCh once Execute begins, then blocks until ctx.Context is done (or a
// generous safety timeout elapses), recording which one happened. This is
// what proves an external executor (e.g. a Kubernetes Job controller) can
// actually observe run cancellation through ExecutionContext.Context, which
// is the whole point of adding it — before this change there was no
// context.Context on the struct at all, despite the doc comment's claim.
type blockingUntilCancelExecutor struct {
	nodeType  string
	startedCh chan struct{}
	observed  chan error
}

func (e *blockingUntilCancelExecutor) Name() string { return "test-blocking" }

func (e *blockingUntilCancelExecutor) CanHandle(nodeType string) bool { return nodeType == e.nodeType }

func (e *blockingUntilCancelExecutor) Execute(ctx extensions.ExecutionContext) (*extensions.ExecutionResult, error) {
	close(e.startedCh)
	if ctx.Context == nil {
		e.observed <- errors.New("ExecutionContext.Context is nil")
		return nil, errors.New("nil context")
	}
	select {
	case <-ctx.Context.Done():
		e.observed <- ctx.Context.Err()
		return nil, ctx.Context.Err()
	case <-time.After(10 * time.Second):
		e.observed <- errors.New("safety timeout elapsed without observing cancellation")
		return nil, errors.New("test executor safety timeout")
	}
}

// TestExecutionContext_ObservesRunCancellation proves that cancelling a run
// via Engine.CancelRun propagates to the extensions.ExecutionContext.Context
// handed to an in-flight external NodeExecutor, and that the executor sees
// it as context.Canceled through ctx.Done()/ctx.Err() — real, observable
// cancellation, not just a struct field with cancellation in its name.
func TestExecutionContext_ObservesRunCancellation(t *testing.T) {
	eng, s := newExecCtxTestEngine(t)

	exec := &blockingUntilCancelExecutor{
		nodeType:  "blocking",
		startedCh: make(chan struct{}),
		observed:  make(chan error, 1),
	}
	eng.Executors = []extensions.NodeExecutor{exec}

	csvPath := execCtxSourceCSV(t)
	pipeline := &models.Pipeline{
		ID: "p-execctx-cancel", Name: "ExecCtx Cancel", Enabled: true,
		Nodes: []models.Node{
			{ID: "source", Type: models.NodeTypeSourceFile, Name: "Source", Config: map[string]interface{}{"path": csvPath, "format": "csv"}},
			{ID: "blocking", Type: models.NodeType("blocking"), Name: "Blocking"},
		},
		Edges: []models.Edge{{From: "source", To: "blocking"}},
	}
	if err := s.CreatePipeline(pipeline); err != nil {
		t.Fatalf("create pipeline: %v", err)
	}

	runID, err := eng.RunPipelineAsync(pipeline.ID)
	if err != nil {
		t.Fatalf("run pipeline async: %v", err)
	}

	select {
	case <-exec.startedCh:
	case <-time.After(5 * time.Second):
		t.Fatal("executor was never dispatched")
	}

	if err := eng.CancelRun(runID); err != nil {
		t.Fatalf("cancel run: %v", err)
	}

	select {
	case obsErr := <-exec.observed:
		if !errors.Is(obsErr, context.Canceled) {
			t.Fatalf("executor observed %v, want context.Canceled", obsErr)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("executor never observed cancellation through ExecutionContext.Context")
	}

	deadline := time.Now().Add(5 * time.Second)
	for {
		stored, getErr := s.GetRun(runID)
		if getErr != nil {
			t.Fatal(getErr)
		}
		if stored.Status == models.RunStatusCancelled {
			break
		}
		if stored.Status == models.RunStatusFailed {
			t.Fatalf("cancelled in-flight run was overwritten as failed: %s", stored.Error)
		}
		if time.Now().After(deadline) {
			t.Fatalf("run status remained %q after cancellation", stored.Status)
		}
		time.Sleep(10 * time.Millisecond)
	}
}
