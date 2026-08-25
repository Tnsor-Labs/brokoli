package engine

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/Tnsor-Labs/brokoli/models"
	"github.com/Tnsor-Labs/brokoli/store"
)

// #289: an interrupted run that provably wrote nothing goes back on the
// queue instead of being failed. The tests below are mostly about the cases
// where it must NOT — a safety rule is only as good as its refusals.

// seedRequeuePipeline builds source -> mid -> sink, so a fixture can put a
// durable record on a node that cannot write (source, mid) or one that can
// (sink) and get opposite answers from the same shape.
func seedRequeuePipeline(t *testing.T, s *store.SQLiteStore, pipelineID string, midType models.NodeType) *models.Pipeline {
	t.Helper()
	now := time.Now().UTC()
	pipe := &models.Pipeline{
		ID: pipelineID, Name: "Requeue Pipeline", Enabled: true,
		CreatedAt: now, UpdatedAt: now,
		Nodes: []models.Node{
			{ID: "source", Type: models.NodeTypeSourceFile, Name: "Source", Config: map[string]interface{}{"path": "unused.csv"}},
			{ID: "mid", Type: midType, Name: "Mid"},
			{ID: "sink", Type: models.NodeTypeSinkFile, Name: "Sink", Config: map[string]interface{}{"path": "unused-out.csv", "format": "csv"}},
		},
		Edges: []models.Edge{{From: "source", To: "mid"}, {From: "mid", To: "sink"}},
	}
	if err := s.CreatePipeline(pipe); err != nil {
		t.Fatalf("CreatePipeline: %v", err)
	}
	return pipe
}

func newRequeueTestEngine(t *testing.T) (*Engine, *store.SQLiteStore, *idempotentFakeQueue) {
	t.Helper()
	dir := t.TempDir()
	s, err := store.NewSQLiteStore(filepath.Join(dir, "requeue.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	eng := drainEngineOnCleanup(t, NewEngine(s))
	queue := newIdempotentFakeQueue()
	eng.JobQueue = queue
	return eng, s, queue
}

// seedInterruptedRun writes the durable trace of a run that died with
// nodeID's attempt in flight: created, attempt started, nothing after.
func seedInterruptedRun(t *testing.T, s *store.SQLiteStore, pipeID, runID string, inFlight string, completed ...string) *models.Run {
	t.Helper()
	run := seedOrphanedRun(t, s, pipeID, runID, models.RunStatusRunning)
	appendRecoveryEvent(t, s, &models.RunEvent{
		RunID: run.ID, EventType: models.RunEventCreated,
		Payload: models.RunEventPayload{Status: models.RunStatusRunning, PipelineID: run.PipelineID, StartedAt: run.StartedAt},
	})
	for _, nodeID := range completed {
		appendRecoveryEvent(t, s, &models.RunEvent{
			RunID: run.ID, NodeID: nodeID, Attempt: attemptPtr(0), EventType: models.AttemptStarted,
			Payload: models.RunEventPayload{Status: models.RunStatusRunning, NodeRunID: nodeID + "-nr"},
		})
		appendRecoveryEvent(t, s, &models.RunEvent{
			RunID: run.ID, NodeID: nodeID, Attempt: attemptPtr(0), EventType: models.AttemptCompleted,
			Payload: models.RunEventPayload{Status: models.RunStatusSuccess, NodeRunID: nodeID + "-nr", RowCount: 1},
		})
	}
	if inFlight != "" {
		appendRecoveryEvent(t, s, &models.RunEvent{
			RunID: run.ID, NodeID: inFlight, Attempt: attemptPtr(0), EventType: models.AttemptStarted,
			Payload: models.RunEventPayload{Status: models.RunStatusRunning, NodeRunID: inFlight + "-nr"},
		})
	}
	return run
}

func withZeroGracePeriod(t *testing.T) {
	t.Helper()
	old := recoveryTransitionGracePeriod
	recoveryTransitionGracePeriod = 0
	t.Cleanup(func() { recoveryTransitionGracePeriod = old })
}

// The case the issue was filed for: a worker evicted while the SOURCE node
// was running. Nothing downstream existed yet, so there is nothing to
// duplicate and the run belongs back on the queue rather than in a human's
// inbox.
func TestRecoveryRequeuesInterruptedRunThatWroteNothing(t *testing.T) {
	withZeroGracePeriod(t)
	eng, s, queue := newRequeueTestEngine(t)
	seedRequeuePipeline(t, s, "p-requeue", models.NodeTypeTransform)
	run := seedInterruptedRun(t, s, "p-requeue", "run-requeue", "source")

	summary, err := eng.RecoverNonTerminalRuns()
	if err != nil {
		t.Fatal(err)
	}
	if summary.RunsRequeued != 1 || summary.RunsFailed != 0 {
		t.Fatalf("summary = %+v, want 1 requeued and 0 failed", summary)
	}

	got, err := s.GetRun(run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != models.RunStatusPending {
		t.Fatalf("run status = %s, want pending", got.Status)
	}
	if got.StartedAt != nil {
		t.Error("StartedAt must be cleared: the run that started is not the run that will now execute")
	}
	if _, ok := queue.job(run.ID); !ok {
		t.Fatal("the run was set pending but never enqueued, which would strand it exactly as #216 describes")
	}

	// The event log records it, which is both the audit trail and what
	// bounds repeated re-queues.
	events, err := s.ListEventsByRun(run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if countPriorRequeues(events) != 1 {
		t.Errorf("expected exactly one requeue event, got %d", countPriorRequeues(events))
	}
	// And the projection agrees with the snapshot, so a later replay does
	// not resurrect the run as running.
	if p := ProjectRun(run.ID, events); p.Status != models.RunStatusPending {
		t.Errorf("projection status = %s, want pending", p.Status)
	}
}

// A sink whose attempt was in flight is the ambiguous case: it may have
// written some or all of its rows. Re-running would duplicate them, so this
// must still fail.
func TestRecoveryDoesNotRequeueWhenASinkWasInFlight(t *testing.T) {
	withZeroGracePeriod(t)
	eng, s, queue := newRequeueTestEngine(t)
	seedRequeuePipeline(t, s, "p-sink-inflight", models.NodeTypeTransform)
	run := seedInterruptedRun(t, s, "p-sink-inflight", "run-sink-inflight", "sink", "source", "mid")

	summary, err := eng.RecoverNonTerminalRuns()
	if err != nil {
		t.Fatal(err)
	}
	if summary.RunsFailed != 1 || summary.RunsRequeued != 0 {
		t.Fatalf("summary = %+v, want the run failed and not requeued", summary)
	}
	if _, ok := queue.job(run.ID); ok {
		t.Fatal("a run whose sink may have written was put back on the queue")
	}
	got, err := s.GetRun(run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != models.RunStatusFailed {
		t.Fatalf("run status = %s, want failed", got.Status)
	}
}

// A sink that COMPLETED is if anything worse: its rows are definitely
// written, so a re-run definitely duplicates them.
func TestRecoveryDoesNotRequeueAfterASinkCompleted(t *testing.T) {
	withZeroGracePeriod(t)
	eng, s, queue := newRequeueTestEngine(t)
	// mid runs after the sink here only so that something is left in
	// flight; what matters is the sink's completed record.
	seedRequeuePipeline(t, s, "p-sink-done", models.NodeTypeTransform)
	run := seedInterruptedRun(t, s, "p-sink-done", "run-sink-done", "mid", "source", "sink")

	if _, err := eng.RecoverNonTerminalRuns(); err != nil {
		t.Fatal(err)
	}
	if _, ok := queue.job(run.ID); ok {
		t.Fatal("a run whose sink already wrote was put back on the queue")
	}
}

// A code node runs arbitrary user code, so nothing can be proven about what
// it did. It is in the pipeline's middle, which is exactly where someone
// would put a "post to our internal API" script.
func TestRecoveryDoesNotRequeueWhenACodeNodeRan(t *testing.T) {
	withZeroGracePeriod(t)
	eng, s, queue := newRequeueTestEngine(t)
	seedRequeuePipeline(t, s, "p-code", models.NodeTypeCode)
	run := seedInterruptedRun(t, s, "p-code", "run-code", "mid", "source")

	if _, err := eng.RecoverNonTerminalRuns(); err != nil {
		t.Fatal(err)
	}
	if _, ok := queue.job(run.ID); ok {
		t.Fatal("a run that executed a code node was put back on the queue")
	}
}

// Without a job queue there is nothing to dispatch a pending run, so
// parking it pending would strand it forever — the failure mode #216
// describes. Failing is correct there.
func TestRecoveryDoesNotRequeueWithoutAJobQueue(t *testing.T) {
	withZeroGracePeriod(t)
	eng, s, _ := newRequeueTestEngine(t)
	eng.JobQueue = nil
	seedRequeuePipeline(t, s, "p-noqueue", models.NodeTypeTransform)
	run := seedInterruptedRun(t, s, "p-noqueue", "run-noqueue", "source")

	summary, err := eng.RecoverNonTerminalRuns()
	if err != nil {
		t.Fatal(err)
	}
	if summary.RunsFailed != 1 || summary.RunsRequeued != 0 {
		t.Fatalf("summary = %+v, want failed rather than parked pending", summary)
	}
	got, err := s.GetRun(run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != models.RunStatusFailed {
		t.Fatalf("run status = %s, want failed — pending with no queue is stranded", got.Status)
	}
}

// Durable cancel intent outranks re-queueing: the user asked for this run
// to stop, and putting it back on the queue would restart work they
// cancelled.
func TestRecoveryDoesNotRequeueACancelledRun(t *testing.T) {
	withZeroGracePeriod(t)
	eng, s, queue := newRequeueTestEngine(t)
	seedRequeuePipeline(t, s, "p-cancelled", models.NodeTypeTransform)
	run := seedInterruptedRun(t, s, "p-cancelled", "run-cancelled", "source")
	// UpdateRun deliberately does not write cancel_requested; the durable
	// intent has its own conditional write.
	if ok, err := s.RequestRunCancel(run.ID); err != nil || !ok {
		t.Fatalf("RequestRunCancel: ok=%v err=%v", ok, err)
	}

	if _, err := eng.RecoverNonTerminalRuns(); err != nil {
		t.Fatal(err)
	}
	if _, ok := queue.job(run.ID); ok {
		t.Fatal("a run with cancel intent was put back on the queue")
	}
	got, err := s.GetRun(run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != models.RunStatusCancelled {
		t.Fatalf("run status = %s, want cancelled", got.Status)
	}
}

// A run that keeps being interrupted must eventually stop cycling and
// become visible, or this feature replaces a failed run with an invisible
// infinite loop.
func TestRecoveryRequeueIsBounded(t *testing.T) {
	withZeroGracePeriod(t)
	eng, s, _ := newRequeueTestEngine(t)
	seedRequeuePipeline(t, s, "p-bounded", models.NodeTypeTransform)
	run := seedInterruptedRun(t, s, "p-bounded", "run-bounded", "source")

	// Every prior re-queue this run has already had.
	for i := 0; i < maxRecoveryRequeues; i++ {
		appendRecoveryEvent(t, s, &models.RunEvent{
			RunID: run.ID, EventType: models.RunEventRecoveryRequeued,
			Payload: models.RunEventPayload{Status: models.RunStatusPending},
		})
	}
	// Claimed again and interrupted again, which is what the delivery
	// after the last re-queue looks like. The claim matters: without it
	// the projection still reads pending and recovery correctly leaves a
	// never-claimed run alone.
	run.Status = models.RunStatusRunning
	if err := s.UpdateRun(run); err != nil {
		t.Fatal(err)
	}
	started := time.Now().UTC()
	appendRecoveryEvent(t, s, &models.RunEvent{
		RunID: run.ID, EventType: models.RunEventClaimed,
		Payload: models.RunEventPayload{Status: models.RunStatusRunning, StartedAt: &started},
	})
	appendRecoveryEvent(t, s, &models.RunEvent{
		RunID: run.ID, NodeID: "source", Attempt: attemptPtr(1), EventType: models.AttemptStarted,
		Payload: models.RunEventPayload{Status: models.RunStatusRunning, NodeRunID: "source-nr-2"},
	})

	summary, err := eng.RecoverNonTerminalRuns()
	if err != nil {
		t.Fatal(err)
	}
	if summary.RunsRequeued != 0 || summary.RunsFailed != 1 {
		t.Fatalf("summary = %+v, want the run failed after exhausting its re-queues", summary)
	}
}

// The whitelist itself, stated as a table so a change to it is a visible
// decision rather than a diff nobody reads.
func TestNodeCannotWriteExternally(t *testing.T) {
	safe := []models.NodeType{
		models.NodeTypeSourceFile, models.NodeTypeSourceDB,
		models.NodeTypeTransform, models.NodeTypeQualityCheck,
		models.NodeTypeSQLGenerate, models.NodeTypeJoin,
		models.NodeTypeCondition, models.NodeTypeUnion,
		models.NodeTypeDatasetMap, models.NodeTypeDatasetFilter,
	}
	for _, tp := range safe {
		if !nodeCannotWriteExternally(models.Node{Type: tp}) {
			t.Errorf("%s should be re-runnable", tp)
		}
	}

	unsafe := []models.NodeType{
		models.NodeTypeSinkFile, models.NodeTypeSinkDB, models.NodeTypeSinkAPI,
		models.NodeTypeNotify, models.NodeTypeMigrate, models.NodeTypeDBT,
		// Arbitrary user code.
		models.NodeTypeCode,
		// Looks like a read, but its HTTP method is configurable.
		models.NodeTypeSourceAPI,
		// Unknown types get no assumption made about them.
		models.NodeType("some-plugin-node"), models.NodeType(""),
	}
	for _, tp := range unsafe {
		if nodeCannotWriteExternally(models.Node{Type: tp}) {
			t.Errorf("%s must not be treated as re-runnable", tp)
		}
	}

	// A declared sink capability outranks the type name: the SDK can say
	// what a node does, and that is a stronger statement than its type.
	declared := models.Node{Type: models.NodeTypeTransform, Capabilities: []string{models.CapabilitySink}}
	if nodeCannotWriteExternally(declared) {
		t.Error("a node declaring the sink capability must not be treated as re-runnable")
	}
}

// A record for a node the current pipeline version no longer contains
// cannot be reasoned about at all.
func TestRequeueRefusesUnknownNodeRecord(t *testing.T) {
	pipe := &models.Pipeline{Nodes: []models.Node{{ID: "source", Type: models.NodeTypeSourceFile}}}
	latest := map[string]models.NodeRun{
		"source":     {Status: models.RunStatusSuccess},
		"gone-in-v2": {Status: models.RunStatusSuccess},
	}
	safe, why := interruptedRunIsSafeToRequeue(pipe, latest)
	if safe {
		t.Fatal("a record for a node not in this pipeline version must block re-queueing")
	}
	if why == "" {
		t.Error("the refusal should say why")
	}
}

// A skipped node did not execute, so it cannot have written — even a sink.
func TestRequeueIgnoresSkippedNodes(t *testing.T) {
	pipe := &models.Pipeline{Nodes: []models.Node{
		{ID: "source", Type: models.NodeTypeSourceFile},
		{ID: "sink", Type: models.NodeTypeSinkDB},
	}}
	latest := map[string]models.NodeRun{
		"source": {Status: models.RunStatusSuccess},
		"sink":   {Status: models.RunStatusSkipped},
	}
	if safe, why := interruptedRunIsSafeToRequeue(pipe, latest); !safe {
		t.Fatalf("a skipped sink did not run and must not block re-queueing: %s", why)
	}
}
