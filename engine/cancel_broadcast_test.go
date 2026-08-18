package engine

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/Tnsor-Labs/brokoli/extensions"
	"github.com/Tnsor-Labs/brokoli/models"
	"github.com/Tnsor-Labs/brokoli/store"
)

// recordingCancelBroadcaster is a fake extensions.RunCancelBroadcaster that
// records every broadcast instead of transporting it anywhere.
type recordingCancelBroadcaster struct {
	mu         sync.Mutex
	broadcasts []string
}

func (r *recordingCancelBroadcaster) BroadcastCancel(runID string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.broadcasts = append(r.broadcasts, runID)
	return nil
}

func (r *recordingCancelBroadcaster) SubscribeCancels(func(runID string)) error { return nil }

func (r *recordingCancelBroadcaster) Close() error { return nil }

func (r *recordingCancelBroadcaster) recorded() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.broadcasts...)
}

func seedCancelTestPipeline(t *testing.T, s *store.SQLiteStore, pipeID string) {
	t.Helper()
	pipe := &models.Pipeline{
		ID: pipeID, Name: "Cancel Semantics", Enabled: true,
		Nodes: []models.Node{
			{ID: "source", Type: models.NodeTypeSourceFile, Name: "Source", Config: map[string]interface{}{"path": execCtxSourceCSV(t), "format": "csv"}},
		},
	}
	if err := s.CreatePipeline(pipe); err != nil {
		t.Fatalf("create pipeline: %v", err)
	}
}

// TestCancelRun_PendingRunCancelledAtomically proves cancelling a queued,
// unclaimed run works at all (before the PendingRunCanceller capability it
// returned "not found or already completed" — the run then executed
// whenever a worker got to it), and that the cancel is a compare-and-swap
// no later claim can override: ClaimPendingRun on the cancelled run must
// lose.
func TestCancelRun_PendingRunCancelledAtomically(t *testing.T) {
	eng, s := newExecCtxTestEngine(t)
	seedCancelTestPipeline(t, s, "p-cancel-pending")

	run := &models.Run{ID: "run-pending-cancel", PipelineID: "p-cancel-pending", Status: models.RunStatusPending}
	if err := s.CreateRun(run); err != nil {
		t.Fatalf("create pending run: %v", err)
	}

	if err := eng.CancelRun(run.ID); err != nil {
		t.Fatalf("CancelRun on a pending run: %v", err)
	}

	stored, err := s.GetRun(run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Status != models.RunStatusCancelled {
		t.Fatalf("pending run status after cancel = %q, want cancelled", stored.Status)
	}
	if stored.FinishedAt == nil {
		t.Error("cancelled pending run has no FinishedAt")
	}

	claimed, err := s.ClaimPendingRun(run.ID, "p-cancel-pending", time.Now().UTC(), "trace")
	if err != nil {
		t.Fatal(err)
	}
	if claimed {
		t.Fatal("ClaimPendingRun claimed a run that was already cancelled — the conditional swap failed")
	}
}

// TestCancelRun_RunningElsewhere_Broadcasts proves the distributed path: a
// run that the store says is running but that has no Runner in this
// process (it executes on another instance) is broadcast through the
// configured broadcaster instead of failing with "not found" — the exact live
// k3s failure where every API-pod cancel 404'd while the worker kept
// executing. Without a broadcaster the error remains, loudly.
func TestCancelRun_RunningElsewhere_Broadcasts(t *testing.T) {
	eng, s := newExecCtxTestEngine(t)
	seedCancelTestPipeline(t, s, "p-cancel-remote")

	started := time.Now().UTC()
	run := &models.Run{ID: "run-remote", PipelineID: "p-cancel-remote", Status: models.RunStatusRunning, StartedAt: &started}
	if err := s.CreateRun(run); err != nil {
		t.Fatalf("create running run: %v", err)
	}

	broadcaster := &recordingCancelBroadcaster{}
	eng.CancelBroadcaster = broadcaster
	if err := eng.CancelRun(run.ID); err != nil {
		t.Fatalf("CancelRun with broadcaster: %v", err)
	}
	got := broadcaster.recorded()
	if len(got) != 1 || got[0] != run.ID {
		t.Fatalf("broadcasts = %v, want exactly [%s]", got, run.ID)
	}
	// The durable intent is persisted BEFORE the broadcast, so a crash or
	// lost message after this point still converges.
	stored, err := s.GetRun(run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !stored.CancelRequested {
		t.Fatal("cancel_requested was not persisted before broadcasting")
	}
}

// TestCancelRun_TerminalRun_Errors proves terminal runs are never
// re-cancelled or broadcast — a broadcaster must not receive noise for runs
// that already finished.
func TestCancelRun_TerminalRun_Errors(t *testing.T) {
	eng, s := newExecCtxTestEngine(t)
	seedCancelTestPipeline(t, s, "p-cancel-terminal")

	broadcaster := &recordingCancelBroadcaster{}
	eng.CancelBroadcaster = broadcaster

	for _, status := range []models.RunStatus{models.RunStatusSuccess, models.RunStatusFailed, models.RunStatusCancelled} {
		run := &models.Run{ID: "run-terminal-" + string(status), PipelineID: "p-cancel-terminal", Status: status}
		if err := s.CreateRun(run); err != nil {
			t.Fatal(err)
		}
		if err := eng.CancelRun(run.ID); err == nil {
			t.Fatalf("CancelRun on %s run should error", status)
		}
	}
	if got := broadcaster.recorded(); len(got) != 0 {
		t.Fatalf("terminal-run cancels reached the broadcaster: %v", got)
	}
}

// TestCancelRelayedRun_CancelsLocalRun proves the receiving side of the
// broadcaster: a broadcast delivered to the instance that owns the run cancels
// it exactly like a direct CancelRun — the in-flight executor observes
// context.Canceled and the run finalizes as cancelled. A relayed ID this
// instance does not own is a quiet no-op.
func TestCancelRelayedRun_CancelsLocalRun(t *testing.T) {
	eng, s := newExecCtxTestEngine(t)

	// Unknown run ID: must not panic, must not do anything.
	eng.CancelRelayedRun("no-such-run")

	exec := &blockingUntilCancelExecutor{
		nodeType:  "blocking",
		startedCh: make(chan struct{}),
		observed:  make(chan error, 1),
	}
	eng.Executors = []extensions.NodeExecutor{exec}

	pipe := &models.Pipeline{
		ID: "p-relayed-cancel", Name: "Relayed Cancel", Enabled: true,
		Nodes: []models.Node{
			{ID: "source", Type: models.NodeTypeSourceFile, Name: "Source", Config: map[string]interface{}{"path": execCtxSourceCSV(t), "format": "csv"}},
			{ID: "blocking", Type: models.NodeType("blocking"), Name: "Blocking"},
		},
		Edges: []models.Edge{{From: "source", To: "blocking"}},
	}
	if err := s.CreatePipeline(pipe); err != nil {
		t.Fatalf("create pipeline: %v", err)
	}

	runID, err := eng.RunPipelineAsync(pipe.ID)
	if err != nil {
		t.Fatalf("run pipeline async: %v", err)
	}
	select {
	case <-exec.startedCh:
	case <-time.After(5 * time.Second):
		t.Fatal("executor was never dispatched")
	}

	eng.CancelRelayedRun(runID)

	select {
	case obsErr := <-exec.observed:
		if !errors.Is(obsErr, context.Canceled) {
			t.Fatalf("executor observed %v, want context.Canceled", obsErr)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("executor never observed the relayed cancellation")
	}

	deadline := time.Now().Add(5 * time.Second)
	for {
		stored, getErr := s.GetRun(runID)
		if getErr != nil {
			t.Fatal(getErr)
		}
		if stored.Status == models.RunStatusCancelled {
			return
		}
		if stored.Status == models.RunStatusFailed {
			t.Fatalf("relayed-cancelled run was recorded as failed: %s", stored.Error)
		}
		if time.Now().After(deadline) {
			t.Fatalf("run status remained %q after relayed cancellation", stored.Status)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// gateExecutor succeeds only after proceed is closed — it lets a test hold
// a node "in flight" while arranging state, then release it.
type gateExecutor struct {
	nodeType  string
	startedCh chan struct{}
	proceed   chan struct{}
}

func (g *gateExecutor) Name() string                   { return "test-gate" }
func (g *gateExecutor) CanHandle(nodeType string) bool { return nodeType == g.nodeType }
func (g *gateExecutor) Execute(ctx extensions.ExecutionContext) (*extensions.ExecutionResult, error) {
	close(g.startedCh)
	select {
	case <-g.proceed:
		return &extensions.ExecutionResult{}, nil
	case <-time.After(10 * time.Second):
		return nil, errors.New("gate executor safety timeout")
	}
}

// tallyExecutor records whether it was ever dispatched.
type tallyExecutor struct {
	nodeType string
	mu       sync.Mutex
	calls    int
}

func (x *tallyExecutor) Name() string                   { return "test-tally" }
func (x *tallyExecutor) CanHandle(nodeType string) bool { return nodeType == x.nodeType }
func (x *tallyExecutor) Execute(extensions.ExecutionContext) (*extensions.ExecutionResult, error) {
	x.mu.Lock()
	x.calls++
	x.mu.Unlock()
	return &extensions.ExecutionResult{}, nil
}

func (x *tallyExecutor) count() int {
	x.mu.Lock()
	defer x.mu.Unlock()
	return x.calls
}

// TestCancelRun_DurableIntent_NoRelay proves a cancel for a run executing
// in another process succeeds WITHOUT a broadcaster, via the durable
// cancel_requested flag: intent is persisted, CancelRun reports success,
// and the owning Runner converges at its next wave boundary (proven
// separately below). Before the flag existed this path was a hard error.
func TestCancelRun_DurableIntent_NoRelay(t *testing.T) {
	eng, s := newExecCtxTestEngine(t)
	seedCancelTestPipeline(t, s, "p-cancel-intent")

	started := time.Now().UTC()
	run := &models.Run{ID: "run-intent", PipelineID: "p-cancel-intent", Status: models.RunStatusRunning, StartedAt: &started}
	if err := s.CreateRun(run); err != nil {
		t.Fatal(err)
	}

	if err := eng.CancelRun(run.ID); err != nil {
		t.Fatalf("CancelRun without broadcaster should succeed via durable intent: %v", err)
	}
	stored, err := s.GetRun(run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !stored.CancelRequested {
		t.Fatal("cancel_requested flag was not persisted")
	}
	// Not finalized by this instance — the owning Runner does that.
	if stored.Status != models.RunStatusRunning {
		t.Fatalf("status = %s, want still running (owner finalizes)", stored.Status)
	}
}

// TestRunner_HonorsDurableCancelIntentAtWaveBoundary proves the
// convergence half: a Runner whose run has cancel_requested set — a lost
// broadcast, or a cancel issued while no process owned the run —
// cancels at the next wave boundary. The downstream node never executes
// and the run finalizes as cancelled.
func TestRunner_HonorsDurableCancelIntentAtWaveBoundary(t *testing.T) {
	eng, s := newExecCtxTestEngine(t)

	gate := &gateExecutor{nodeType: "gate", startedCh: make(chan struct{}), proceed: make(chan struct{})}
	tail := &tallyExecutor{nodeType: "tail"}
	eng.Executors = []extensions.NodeExecutor{gate, tail}

	pipe := &models.Pipeline{
		ID: "p-wave-intent", Name: "Wave Intent", Enabled: true,
		Nodes: []models.Node{
			{ID: "source", Type: models.NodeTypeSourceFile, Name: "Source", Config: map[string]interface{}{"path": execCtxSourceCSV(t), "format": "csv"}},
			{ID: "gate", Type: models.NodeType("gate"), Name: "Gate"},
			{ID: "tail", Type: models.NodeType("tail"), Name: "Tail"},
		},
		Edges: []models.Edge{{From: "source", To: "gate"}, {From: "gate", To: "tail"}},
	}
	if err := s.CreatePipeline(pipe); err != nil {
		t.Fatal(err)
	}

	runID, err := eng.RunPipelineAsync(pipe.ID)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-gate.startedCh:
	case <-time.After(5 * time.Second):
		t.Fatal("gate executor was never dispatched")
	}

	// Simulate a lost broadcast: intent lands in the store only.
	if requested, rErr := s.RequestRunCancel(runID); rErr != nil || !requested {
		t.Fatalf("RequestRunCancel = %v, %v", requested, rErr)
	}
	close(gate.proceed)

	deadline := time.Now().Add(5 * time.Second)
	for {
		stored, gErr := s.GetRun(runID)
		if gErr != nil {
			t.Fatal(gErr)
		}
		if stored.Status == models.RunStatusCancelled {
			break
		}
		if isTerminalRunStatus(stored.Status) {
			t.Fatalf("run finalized as %s, want cancelled", stored.Status)
		}
		if time.Now().After(deadline) {
			t.Fatalf("run stuck in %s; wave-boundary intent check never fired", stored.Status)
		}
		time.Sleep(10 * time.Millisecond)
	}
	if tail.count() != 0 {
		t.Fatalf("tail node executed %d time(s) after cancel intent", tail.count())
	}
}

// TestCancelRun_DuringSemaphoreWait proves the claim-to-register window is
// closed: a queued run claimed by this process but still waiting for a
// concurrency slot IS cancellable — the runner is registered in the
// active map before the semaphore wait, and the durable pre-Execute
// Cancel finalizes it as cancelled the moment it gets its slot, without
// executing any node.
func TestCancelRun_DuringSemaphoreWait(t *testing.T) {
	eng, s := newExecCtxTestEngine(t)
	eng.SetMaxConcurrentRuns(1)

	blocker := &gateExecutor{nodeType: "blocker", startedCh: make(chan struct{}), proceed: make(chan struct{})}
	starved := &tallyExecutor{nodeType: "starved"}
	eng.Executors = []extensions.NodeExecutor{blocker, starved}

	blockPipe := &models.Pipeline{
		ID: "p-sem-blocker", Name: "Sem Blocker", Enabled: true,
		Nodes: []models.Node{
			{ID: "source", Type: models.NodeTypeSourceFile, Name: "Source", Config: map[string]interface{}{"path": execCtxSourceCSV(t), "format": "csv"}},
			{ID: "blocker", Type: models.NodeType("blocker"), Name: "Blocker"},
		},
		Edges: []models.Edge{{From: "source", To: "blocker"}},
	}
	starvedPipe := &models.Pipeline{
		ID: "p-sem-starved", Name: "Sem Starved", Enabled: true,
		Nodes: []models.Node{
			{ID: "source", Type: models.NodeTypeSourceFile, Name: "Source", Config: map[string]interface{}{"path": execCtxSourceCSV(t), "format": "csv"}},
			{ID: "starved", Type: models.NodeType("starved"), Name: "Starved"},
		},
		Edges: []models.Edge{{From: "source", To: "starved"}},
	}
	for _, p := range []*models.Pipeline{blockPipe, starvedPipe} {
		if err := s.CreatePipeline(p); err != nil {
			t.Fatal(err)
		}
	}

	// Occupy the single slot.
	if _, err := eng.RunPipelineAsync(blockPipe.ID); err != nil {
		t.Fatal(err)
	}
	select {
	case <-blocker.startedCh:
	case <-time.After(5 * time.Second):
		t.Fatal("blocker was never dispatched")
	}

	// Seed and claim-execute a second run; it parks on the semaphore.
	queued := &models.Run{ID: "run-sem-starved", PipelineID: starvedPipe.ID, Status: models.RunStatusPending}
	if err := s.CreateRun(queued); err != nil {
		t.Fatal(err)
	}
	execDone := make(chan struct{})
	go func() {
		defer close(execDone)
		_, _ = eng.ExecuteQueuedRun(queued.ID, starvedPipe.ID, nil)
	}()

	// Wait until the queued run is claimed and registered as active
	// (status running + cancellable locally).
	deadline := time.Now().Add(5 * time.Second)
	for {
		if err := eng.CancelRun(queued.ID); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("queued run never became cancellable while waiting for a slot")
		}
		time.Sleep(10 * time.Millisecond)
	}

	// Release the blocker; the starved run must finalize cancelled
	// without its node ever executing.
	close(blocker.proceed)
	select {
	case <-execDone:
	case <-time.After(10 * time.Second):
		t.Fatal("ExecuteQueuedRun never returned")
	}
	stored, err := s.GetRun(queued.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Status != models.RunStatusCancelled {
		t.Fatalf("starved run status = %s, want cancelled", stored.Status)
	}
	if starved.count() != 0 {
		t.Fatalf("starved node executed %d time(s) despite pre-slot cancel", starved.count())
	}
}

// TestRunnerCancelBeforeExecuteIsDurable proves Cancel is not lost when it
// lands before Execute has created the run context — the window where a
// runner is registered in Engine.active (RunPipeline registers before
// spawning the goroutine; ExecuteQueuedRun can wait on the concurrency
// semaphore for minutes under saturation) but r.cancel is still nil.
// Before this, such a Cancel was a silent no-op and the run executed to
// completion as if never cancelled.
func TestRunnerCancelBeforeExecuteIsDurable(t *testing.T) {
	eng, s := newExecCtxTestEngine(t)
	seedCancelTestPipeline(t, s, "p-precancel")
	pipe, err := s.GetPipeline("p-precancel")
	if err != nil {
		t.Fatal(err)
	}

	eventCh := make(chan models.Event, 64)
	done := make(chan struct{})
	go func() {
		defer close(done)
		for range eventCh {
		}
	}()
	t.Cleanup(func() { close(eventCh); <-done })
	_ = eng // engine only used for store/harness lifecycle

	r := NewRunner(s, eventCh, pipe, nil, nil, nil, nil, "", nil)
	r.Cancel() // before Execute: previously a silent no-op

	run, execErr := r.Execute()
	if execErr == nil {
		t.Fatal("Execute after pre-Execute Cancel should return the cancellation error")
	}
	if run == nil {
		t.Fatal("Execute should still return the run it created")
	}
	stored, err := s.GetRun(run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Status != models.RunStatusCancelled {
		t.Fatalf("pre-cancelled run status = %q, want cancelled", stored.Status)
	}
}

// TestRunner_CancelIntentWatcherFiresMidNode is the live-found regression:
// a single-node pipeline mid-execution has no wave boundary, so when the
// broadcaster transport is down (a Redis restart used to kill every subscriber
// permanently), a durably-recorded cancel was never observed and the run
// completed. The watcher polls the flag DURING execution: the gate node
// here never finishes on its own — only the watcher can end this run.
func TestRunner_CancelIntentWatcherFiresMidNode(t *testing.T) {
	oldInterval := cancelIntentPollInterval
	cancelIntentPollInterval = 50 * time.Millisecond
	defer func() { cancelIntentPollInterval = oldInterval }()

	eng, s := newExecCtxTestEngine(t)
	gate := &gateExecutor{nodeType: "gate", startedCh: make(chan struct{}), proceed: make(chan struct{})}
	defer close(gate.proceed) // let the executor's goroutine exit after the test
	eng.Executors = []extensions.NodeExecutor{gate}

	pipe := &models.Pipeline{
		ID: "p-midnode-intent", Name: "Mid-node Intent", Enabled: true,
		Nodes: []models.Node{
			{ID: "source", Type: models.NodeTypeSourceFile, Name: "Source", Config: map[string]interface{}{"path": execCtxSourceCSV(t), "format": "csv"}},
			{ID: "gate", Type: models.NodeType("gate"), Name: "Gate"},
		},
		Edges: []models.Edge{{From: "source", To: "gate"}},
	}
	if err := s.CreatePipeline(pipe); err != nil {
		t.Fatal(err)
	}
	runID, err := eng.RunPipelineAsync(pipe.ID)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-gate.startedCh:
	case <-time.After(5 * time.Second):
		t.Fatal("gate never dispatched")
	}

	// Simulate the dead-transport scenario: intent lands durably, nothing
	// delivers it — no broadcaster, no CancelRun, just the flag.
	if requested, rErr := s.RequestRunCancel(runID); rErr != nil || !requested {
		t.Fatalf("RequestRunCancel = %v, %v", requested, rErr)
	}

	deadline := time.Now().Add(5 * time.Second)
	for {
		stored, gErr := s.GetRun(runID)
		if gErr != nil {
			t.Fatal(gErr)
		}
		if stored.Status == models.RunStatusCancelled {
			return
		}
		if stored.Status == models.RunStatusFailed || stored.Status == models.RunStatusSuccess {
			t.Fatalf("run finalized as %s, want cancelled", stored.Status)
		}
		if time.Now().After(deadline) {
			t.Fatalf("watcher never fired; run still %s mid-node", stored.Status)
		}
		time.Sleep(20 * time.Millisecond)
	}
}
