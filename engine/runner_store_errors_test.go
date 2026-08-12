package engine

import (
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Tnsor-Labs/brokoli/models"
	"github.com/Tnsor-Labs/brokoli/pkg/common"
	"github.com/Tnsor-Labs/brokoli/store"
)

// faultInjectingStore wraps a real *store.SQLiteStore and lets a test force
// specific writes to fail. Its purpose is to prove two things: first, that
// the engine/runner.go fixes (Tnsor-Labs/brokoli#7) actually surface a
// store failure as a hard failed run, rather than silently continuing as if
// the write had succeeded (the previous, buggy behavior for CreateNodeRun,
// UpdateNodeRun, SaveNodePreview, and AddToDLQ); and second, that
// RunPipelineAsync's dispatch outbox write (CreateRunTx, AppendEventTx, and
// CreateExecutionAttemptTx, all inside one WithTx) is genuinely
// transactional, so a failure partway through rolls back everything instead
// of leaving a partially-written run.
//
// It embeds the concrete *store.SQLiteStore (not the store.Store interface)
// so every method — including store.ExecutionAttemptStore's, which are not
// part of store.Store — is promoted and available to type assertions the
// same way engine.go's `e.store.(store.ExecutionAttemptStore)` expects of a
// real store. Unset failure fields simply delegate to the real store.
type faultInjectingStore struct {
	*store.SQLiteStore
	failCreateNodeRun            bool
	failUpdateNodeRun            bool
	failSaveNodePreview          bool
	failAddToDLQ                 bool
	failAppendEventTx            bool
	failAppendEventType          models.RunEventType
	failCreateExecutionAttemptTx bool
}

var errInjected = errors.New("injected store failure")

func (f *faultInjectingStore) CreateNodeRun(nr *models.NodeRun) error {
	if f.failCreateNodeRun {
		return errInjected
	}
	return f.SQLiteStore.CreateNodeRun(nr)
}

// CreateNodeRunTx also honors failCreateNodeRun: since Tnsor-Labs/brokoli#90
// M3 wired node-level attempts through store.ExecutionAttemptStore, the
// runner now persists NodeRun via this Tx variant (alongside
// CreateExecutionAttemptTx) whenever the store supports it — which
// faultInjectingStore, embedding a real *store.SQLiteStore, always does.
// Without this override failCreateNodeRun would silently stop firing.
func (f *faultInjectingStore) CreateNodeRunTx(tx *sql.Tx, nr *models.NodeRun) error {
	if f.failCreateNodeRun {
		return errInjected
	}
	return f.SQLiteStore.CreateNodeRunTx(tx, nr)
}

func (f *faultInjectingStore) UpdateNodeRun(nr *models.NodeRun) error {
	if f.failUpdateNodeRun {
		return errInjected
	}
	return f.SQLiteStore.UpdateNodeRun(nr)
}

func (f *faultInjectingStore) SaveNodePreview(runID, nodeID string, columns []string, rows []common.DataRow) error {
	if f.failSaveNodePreview {
		return errInjected
	}
	return f.SQLiteStore.SaveNodePreview(runID, nodeID, columns, rows)
}

func (f *faultInjectingStore) AddToDLQ(pipelineID, runID, nodeID, nodeName, errMsg, payload string) error {
	if f.failAddToDLQ {
		return errInjected
	}
	return f.SQLiteStore.AddToDLQ(pipelineID, runID, nodeID, nodeName, errMsg, payload)
}

func (f *faultInjectingStore) AppendEventTx(tx *sql.Tx, e *models.RunEvent) error {
	if f.failAppendEventTx || (f.failAppendEventType != "" && f.failAppendEventType == e.EventType) {
		return errInjected
	}
	return f.SQLiteStore.AppendEventTx(tx, e)
}

func (f *faultInjectingStore) CreateExecutionAttemptTx(tx *sql.Tx, a *models.ExecutionAttempt) error {
	if f.failCreateExecutionAttemptTx {
		return errInjected
	}
	return f.SQLiteStore.CreateExecutionAttemptTx(tx, a)
}

// newFaultTestEngine returns an Engine backed by a fault-injecting store
// wrapping a real SQLite store, plus the real store (for post-hoc
// assertions) and a single-source-node pipeline ID that succeeds when no
// fault is injected.
func newFaultTestEngine(t *testing.T) (*Engine, *faultInjectingStore, *store.SQLiteStore, string) {
	t.Helper()
	dir := t.TempDir()
	real, err := store.NewSQLiteStore(filepath.Join(dir, "fault.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = real.Close() })

	csvPath := filepath.Join(dir, "input.csv")
	if err := os.WriteFile(csvPath, []byte("id,name\n1,brokoli\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	pipeline := &models.Pipeline{
		ID:   "fault-pipeline",
		Name: "Fault pipeline",
		Nodes: []models.Node{{
			ID:     "source",
			Type:   models.NodeTypeSourceFile,
			Name:   "Source",
			Config: map[string]interface{}{"path": csvPath, "format": "csv"},
		}},
		CreatedAt: time.Now().UTC(),
		UpdatedAt: time.Now().UTC(),
	}
	if err := real.CreatePipeline(pipeline); err != nil {
		t.Fatal(err)
	}

	fault := &faultInjectingStore{SQLiteStore: real}
	return NewEngine(fault), fault, real, pipeline.ID
}

func TestRunnerSurfacesCreateNodeRunFailure(t *testing.T) {
	eng, fault, real, pipelineID := newFaultTestEngine(t)
	fault.failCreateNodeRun = true

	run, err := eng.RunPipeline(pipelineID)
	if err == nil {
		t.Fatal("RunPipeline returned nil error despite injected CreateNodeRun failure — the store error was silently swallowed")
	}
	if !strings.Contains(err.Error(), "persist node attempt") || !errors.Is(err, errInjected) {
		t.Fatalf("error = %v, want it to wrap the injected CreateNodeRun failure with a \"persist node attempt\" message", err)
	}
	if run == nil || run.Status != models.RunStatusFailed {
		t.Fatalf("run = %+v, want status failed", run)
	}
	nodeRuns, listErr := real.ListNodeRunsByRun(run.ID)
	if listErr != nil {
		t.Fatal(listErr)
	}
	// No durable attempt record could be written, so no external side effect
	// (runNodeLogic) should have been considered dispatched — proven here by
	// there being no node_runs row at all, not a row claiming some status.
	if len(nodeRuns) != 0 {
		t.Fatalf("node_runs = %d, want 0 (CreateNodeRun never persisted, so no attempt should be considered dispatched)", len(nodeRuns))
	}
}

func TestRunnerSurfacesUpdateNodeRunFailureOnSuccessPath(t *testing.T) {
	eng, fault, real, pipelineID := newFaultTestEngine(t)
	fault.failUpdateNodeRun = true

	run, err := eng.RunPipeline(pipelineID)
	if err == nil {
		t.Fatal("RunPipeline returned nil error despite injected UpdateNodeRun failure — the store error was silently swallowed")
	}
	if !strings.Contains(err.Error(), "persist successful node attempt") || !errors.Is(err, errInjected) {
		t.Fatalf("error = %v, want it to wrap the injected UpdateNodeRun failure with a \"persist successful node attempt\" message", err)
	}
	if run == nil || run.Status != models.RunStatusFailed {
		t.Fatalf("run = %+v, want status failed", run)
	}
	nodeRuns, listErr := real.ListNodeRunsByRun(run.ID)
	if listErr != nil {
		t.Fatal(listErr)
	}
	if len(nodeRuns) != 1 {
		t.Fatalf("node_runs = %d, want 1 (CreateNodeRun succeeded before the injected UpdateNodeRun failure)", len(nodeRuns))
	}
	// The node's actual work succeeded in-memory, but that success could
	// never be durably recorded. Before this fix, the error was discarded
	// and the run proceeded as if nothing happened — the row would be
	// silently left in a stale "running" state while the run itself moved
	// on. It must never read back as "success" here.
	if nodeRuns[0].Status == models.RunStatusSuccess {
		t.Fatal("node run status = success, but persistence of that success was injected to fail — this is exactly the silent-data-loss bug being fixed")
	}
}

func TestRunnerSurfacesSaveNodePreviewFailure(t *testing.T) {
	eng, fault, _, pipelineID := newFaultTestEngine(t)
	fault.failSaveNodePreview = true

	run, err := eng.RunPipeline(pipelineID)
	if err == nil {
		t.Fatal("RunPipeline returned nil error despite injected SaveNodePreview failure — the store error was silently swallowed")
	}
	if !strings.Contains(err.Error(), "persist node preview") || !errors.Is(err, errInjected) {
		t.Fatalf("error = %v, want it to wrap the injected SaveNodePreview failure with a \"persist node preview\" message", err)
	}
	if run == nil || run.Status != models.RunStatusFailed {
		t.Fatalf("run = %+v, want status failed", run)
	}
}

func TestRunnerSurfacesAddToDLQFailureAlongsideOriginalRunError(t *testing.T) {
	dir := t.TempDir()
	real, err := store.NewSQLiteStore(filepath.Join(dir, "dlq-fault.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer real.Close()

	pipeline := &models.Pipeline{
		ID:   "dlq-fault-pipeline",
		Name: "DLQ fault pipeline",
		Nodes: []models.Node{{
			ID:     "source",
			Type:   models.NodeTypeSourceFile,
			Name:   "Source",
			Config: map[string]interface{}{"path": filepath.Join(dir, "does-not-exist.csv"), "format": "csv"},
		}},
		CreatedAt: time.Now().UTC(),
		UpdatedAt: time.Now().UTC(),
	}
	if err := real.CreatePipeline(pipeline); err != nil {
		t.Fatal(err)
	}

	// This node fails for a genuine reason (missing input file) — not an
	// injected store fault — so failRun's real-error path runs, and only
	// the DLQ write on top of it is injected to fail.
	fault := &faultInjectingStore{SQLiteStore: real, failAddToDLQ: true}
	eng := NewEngine(fault)

	run, err := eng.RunPipeline(pipeline.ID)
	if err == nil {
		t.Fatal("RunPipeline returned nil error despite the node genuinely failing")
	}
	if !strings.Contains(err.Error(), "persist dlq entry") || !errors.Is(err, errInjected) {
		t.Fatalf("error = %v, want it to also wrap the injected AddToDLQ failure with a \"persist dlq entry\" message", err)
	}
	if !strings.Contains(err.Error(), "run failed") {
		t.Fatalf("error = %v, want the original node/run failure cause preserved alongside the DLQ failure, not replaced by it", err)
	}
	if run == nil || run.Status != models.RunStatusFailed {
		t.Fatalf("run = %+v, want status failed", run)
	}
	// The run's own terminal state was still durably persisted (UpdateRun
	// succeeded) even though the DLQ write failed — only the DLQ failure
	// is what additionally surfaces.
	persisted, getErr := real.GetRun(run.ID)
	if getErr != nil {
		t.Fatal(getErr)
	}
	if persisted.Status != models.RunStatusFailed {
		t.Fatalf("persisted run status = %s, want failed", persisted.Status)
	}
}
