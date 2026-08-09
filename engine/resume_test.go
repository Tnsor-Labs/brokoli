package engine

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Tnsor-Labs/brokoli/extensions"
	"github.com/Tnsor-Labs/brokoli/models"
	"github.com/Tnsor-Labs/brokoli/pkg/common"
	"github.com/Tnsor-Labs/brokoli/store"
)

// controllableExecutor is a test-only extensions.NodeExecutor that lets a
// test script exactly which call to a given node type fails vs. succeeds,
// and records what input each call received. This is what makes it
// possible to build a deterministic "fails once, succeeds on resume"
// pipeline node without depending on real, flaky I/O (a bad webhook, a
// locked file, etc).
type controllableExecutor struct {
	mu       sync.Mutex
	nodeType string
	calls    int
	inputs   []*common.DataSet
	onCall   func(call int, input *common.DataSet) (*common.DataSet, error)
}

func (e *controllableExecutor) Name() string { return "test-controllable" }

func (e *controllableExecutor) CanHandle(nodeType string) bool { return nodeType == e.nodeType }

func (e *controllableExecutor) Execute(ctx extensions.ExecutionContext) (*extensions.ExecutionResult, error) {
	var input *common.DataSet
	if ctx.InputData != nil {
		input, _ = ctx.InputData.(*common.DataSet)
	}
	e.mu.Lock()
	e.calls++
	call := e.calls
	e.inputs = append(e.inputs, input)
	e.mu.Unlock()

	out, err := e.onCall(call, input)
	if err != nil {
		return nil, err
	}
	return &extensions.ExecutionResult{OutputData: out}, nil
}

func (e *controllableExecutor) callCount() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.calls
}

func (e *controllableExecutor) lastInput() *common.DataSet {
	e.mu.Lock()
	defer e.mu.Unlock()
	if len(e.inputs) == 0 {
		return nil
	}
	return e.inputs[len(e.inputs)-1]
}

// failOnce returns an onCall behavior that errors on the first call and
// echoes its input back unchanged (or a fixed output, if out != nil) on
// every subsequent call.
func failOnce(out *common.DataSet) func(call int, input *common.DataSet) (*common.DataSet, error) {
	return func(call int, input *common.DataSet) (*common.DataSet, error) {
		if call == 1 {
			return nil, errFlaky
		}
		if out != nil {
			return out, nil
		}
		return input, nil
	}
}

var errFlaky = &flakyError{}

type flakyError struct{}

func (*flakyError) Error() string { return "flaky node: simulated failure on first attempt" }

// newResumeTestEngine builds a fresh SQLite-backed Engine with its own
// temp-dir artifact and pagination-checkpoint stores — every resume test
// needs a durable run store and a durable artifact store to exercise the
// real restore path; pagination-checkpoint tests need the latter isolated
// to a temp dir too, rather than NewEngine's default
// ./brokoli-pagination-checkpoints (which would otherwise leak into the
// process's working directory whenever a test exercises checkpoint_every).
func newResumeTestEngine(t *testing.T) (*Engine, *store.SQLiteStore) {
	t.Helper()
	dir := t.TempDir()
	s, err := store.NewSQLiteStore(filepath.Join(dir, "resume.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	eng := NewEngine(s)
	eng.ArtifactStore = NewLocalDiskArtifactStore(filepath.Join(dir, "artifacts"))
	eng.PaginationCheckpointStore = NewLocalDiskPaginationCheckpointStore(filepath.Join(dir, "pagination-checkpoints"))
	// Registered after the store's cleanup, so it runs first (LIFO): the
	// engine drains its background goroutines before the store closes and
	// before t.TempDir's removal — the test half of Tnsor-Labs/brokoli#94.
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := eng.Close(ctx); err != nil {
			t.Errorf("engine close: %v", err)
		}
	})
	return eng, s
}

func writeCSV(t *testing.T, dir, name, content string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write csv: %v", err)
	}
	return path
}

// TestResumeRun_RestoresSourceOutputForDownstreamNode is the "resume after
// a source node succeeded" acceptance case from Tnsor-Labs/brokoli#8: a
// node directly downstream of a source that already succeeded must see the
// source's REAL data on resume, not an empty dataset.
func TestResumeRun_RestoresSourceOutputForDownstreamNode(t *testing.T) {
	eng, s := newResumeTestEngine(t)
	dir := t.TempDir()
	csvPath := writeCSV(t, dir, "in.csv", "id,name\n1,brokoli\n2,sql\n")

	flaky := &controllableExecutor{nodeType: "flaky", onCall: failOnce(nil)}
	eng.Executors = []extensions.NodeExecutor{flaky}

	pipeline := &models.Pipeline{
		ID: "p-source-resume", Name: "Source Resume", Enabled: true,
		Nodes: []models.Node{
			{ID: "source", Type: models.NodeTypeSourceFile, Name: "Source", Config: map[string]interface{}{"path": csvPath, "format": "csv"}},
			{ID: "flaky", Type: models.NodeType("flaky"), Name: "Flaky"},
		},
		Edges: []models.Edge{{From: "source", To: "flaky"}},
	}
	if err := s.CreatePipeline(pipeline); err != nil {
		t.Fatalf("create pipeline: %v", err)
	}

	firstRun, err := eng.RunPipeline(pipeline.ID)
	if err != nil && firstRun == nil {
		t.Fatalf("run pipeline: %v", err)
	}
	if firstRun.Status != models.RunStatusFailed {
		t.Fatalf("first run status = %s, want failed", firstRun.Status)
	}
	if flaky.callCount() != 1 {
		t.Fatalf("flaky call count after first run = %d, want 1", flaky.callCount())
	}

	resumed, err := eng.ResumeRun(firstRun.ID)
	if err != nil {
		t.Fatalf("resume run: %v", err)
	}
	if resumed.Status != models.RunStatusSuccess {
		t.Fatalf("resumed run status = %s, want success (error on run: %s)", resumed.Status, resumed.Error)
	}
	if resumed.ResumedFromRunID != firstRun.ID {
		t.Fatalf("resumed.ResumedFromRunID = %q, want %q", resumed.ResumedFromRunID, firstRun.ID)
	}

	got := flaky.lastInput()
	if got == nil {
		t.Fatal("flaky node received nil input on resume — source output was not restored")
	}
	if len(got.Rows) != 2 {
		t.Fatalf("flaky node received %d rows on resume, want 2 (the source's real output, not an empty dataset)", len(got.Rows))
	}
	if got.Rows[0]["name"] != "brokoli" || got.Rows[1]["name"] != "sql" {
		t.Fatalf("flaky node received unexpected rows on resume: %+v", got.Rows)
	}
}

// TestResumeRun_RestoresTransformOutputForDownstreamNode is the "resume
// after a transform node succeeded" acceptance case: the node downstream of
// a transform must see the transform's OWN output (not the raw source
// data, and not empty) after resume.
func TestResumeRun_RestoresTransformOutputForDownstreamNode(t *testing.T) {
	eng, s := newResumeTestEngine(t)
	dir := t.TempDir()
	csvPath := writeCSV(t, dir, "in.csv", "id,name\n1,brokoli\n2,sql\n")

	flaky := &controllableExecutor{nodeType: "flaky", onCall: failOnce(nil)}
	eng.Executors = []extensions.NodeExecutor{flaky}

	pipeline := &models.Pipeline{
		ID: "p-transform-resume", Name: "Transform Resume", Enabled: true,
		Nodes: []models.Node{
			{ID: "source", Type: models.NodeTypeSourceFile, Name: "Source", Config: map[string]interface{}{"path": csvPath, "format": "csv"}},
			{ID: "transform", Type: models.NodeTypeTransform, Name: "Transform", Config: map[string]interface{}{
				"rules": []map[string]interface{}{
					{"type": "add_column", "name": "stage", "expression": "transformed"},
				},
			}},
			{ID: "flaky", Type: models.NodeType("flaky"), Name: "Flaky"},
		},
		Edges: []models.Edge{{From: "source", To: "transform"}, {From: "transform", To: "flaky"}},
	}
	if err := s.CreatePipeline(pipeline); err != nil {
		t.Fatalf("create pipeline: %v", err)
	}

	firstRun, err := eng.RunPipeline(pipeline.ID)
	if err != nil && firstRun == nil {
		t.Fatalf("run pipeline: %v", err)
	}
	if firstRun.Status != models.RunStatusFailed {
		t.Fatalf("first run status = %s, want failed", firstRun.Status)
	}

	resumed, err := eng.ResumeRun(firstRun.ID)
	if err != nil {
		t.Fatalf("resume run: %v", err)
	}
	if resumed.Status != models.RunStatusSuccess {
		t.Fatalf("resumed run status = %s, want success (error: %s)", resumed.Status, resumed.Error)
	}

	got := flaky.lastInput()
	if got == nil {
		t.Fatal("flaky node received nil input on resume — transform output was not restored")
	}
	if len(got.Rows) != 2 {
		t.Fatalf("flaky node received %d rows on resume, want 2", len(got.Rows))
	}
	for i, row := range got.Rows {
		if row["stage"] != "transformed" {
			t.Fatalf("row %d missing transform's added column (stage=transformed): %+v — resume restored the wrong (pre-transform or empty) data", i, row)
		}
	}
}

// TestResumeRun_EmptyDatasetRegressionGuard is the dedicated regression test
// for the bug described in Tnsor-Labs/brokoli#8: previously, a skipped
// node's entry in the in-memory outputs map was never populated, so its
// downstream consumer fell through to a nil-input fallback that silently
// substituted an EMPTY dataset. This test pins exact row values, not just
// "no error" — if Runner.executeNode's skip branch is reverted to the old
// `if skip { log(...); return nil }` (i.e. restoreSkippedNodeOutput is
// removed), flaky would receive &common.DataSet{Rows: []} instead of the 2
// real rows asserted below, and this test fails.
func TestResumeRun_EmptyDatasetRegressionGuard(t *testing.T) {
	eng, s := newResumeTestEngine(t)
	dir := t.TempDir()
	csvPath := writeCSV(t, dir, "in.csv", "id,name\n1,brokoli\n2,sql\n3,go\n")

	flaky := &controllableExecutor{nodeType: "flaky", onCall: failOnce(nil)}
	eng.Executors = []extensions.NodeExecutor{flaky}

	pipeline := &models.Pipeline{
		ID: "p-regression-guard", Name: "Regression Guard", Enabled: true,
		Nodes: []models.Node{
			{ID: "source", Type: models.NodeTypeSourceFile, Name: "Source", Config: map[string]interface{}{"path": csvPath, "format": "csv"}},
			{ID: "flaky", Type: models.NodeType("flaky"), Name: "Flaky"},
		},
		Edges: []models.Edge{{From: "source", To: "flaky"}},
	}
	if err := s.CreatePipeline(pipeline); err != nil {
		t.Fatalf("create pipeline: %v", err)
	}

	firstRun, err := eng.RunPipeline(pipeline.ID)
	if err != nil && firstRun == nil {
		t.Fatalf("run pipeline: %v", err)
	}
	if firstRun.Status != models.RunStatusFailed {
		t.Fatalf("first run status = %s, want failed", firstRun.Status)
	}

	resumed, err := eng.ResumeRun(firstRun.ID)
	if err != nil {
		t.Fatalf("resume run: %v", err)
	}
	if resumed.Status != models.RunStatusSuccess {
		t.Fatalf("resumed run status = %s, want success", resumed.Status)
	}

	got := flaky.lastInput()
	if got == nil || len(got.Rows) == 0 {
		t.Fatalf("REGRESSION: downstream node received an empty/nil dataset on resume (got %+v) — a skipped node's output was not restored", got)
	}
	if len(got.Rows) != 3 {
		t.Fatalf("REGRESSION: downstream node received %d rows on resume, want exactly 3 (the source's real row count)", len(got.Rows))
	}
	wantNames := []string{"brokoli", "sql", "go"}
	for i, want := range wantNames {
		if got.Rows[i]["name"] != want {
			t.Fatalf("REGRESSION: row %d = %+v, want name=%q — resumed data does not match the original run's real output", i, got.Rows[i], want)
		}
	}
}

// TestResumeRun_SkipsAlreadySucceededSinkWithoutReexecuting is the "resume
// after a sink node succeeded" acceptance case. A sink has no downstream
// consumers, so the correctness bar is different from source/transform: a
// resume must not re-invoke it at all (which would double a side effect,
// e.g. re-sending a webhook or re-writing a file), and must still succeed
// with the rest of the pipeline retried.
func TestResumeRun_SkipsAlreadySucceededSinkWithoutReexecuting(t *testing.T) {
	eng, s := newResumeTestEngine(t)
	dir := t.TempDir()
	csvPath := writeCSV(t, dir, "in.csv", "id,name\n1,brokoli\n")

	sink := &controllableExecutor{nodeType: "counting_sink", onCall: func(call int, input *common.DataSet) (*common.DataSet, error) {
		return nil, nil // sinks are terminal — no output to hand downstream
	}}
	flaky := &controllableExecutor{nodeType: "flaky", onCall: failOnce(nil)}
	eng.Executors = []extensions.NodeExecutor{sink, flaky}

	pipeline := &models.Pipeline{
		ID: "p-sink-resume", Name: "Sink Resume", Enabled: true,
		Nodes: []models.Node{
			{ID: "source", Type: models.NodeTypeSourceFile, Name: "Source", Config: map[string]interface{}{"path": csvPath, "format": "csv"}},
			{ID: "sink", Type: models.NodeType("counting_sink"), Name: "Sink"},
			{ID: "flaky", Type: models.NodeType("flaky"), Name: "Flaky"},
		},
		// Both sink and flaky consume source directly — a diamond with two
		// terminal branches, so the sink succeeding is independent of flaky
		// failing.
		Edges: []models.Edge{{From: "source", To: "sink"}, {From: "source", To: "flaky"}},
	}
	if err := s.CreatePipeline(pipeline); err != nil {
		t.Fatalf("create pipeline: %v", err)
	}

	firstRun, err := eng.RunPipeline(pipeline.ID)
	if err != nil && firstRun == nil {
		t.Fatalf("run pipeline: %v", err)
	}
	if firstRun.Status != models.RunStatusFailed {
		t.Fatalf("first run status = %s, want failed", firstRun.Status)
	}
	if sink.callCount() != 1 {
		t.Fatalf("sink call count after first run = %d, want 1", sink.callCount())
	}

	resumed, err := eng.ResumeRun(firstRun.ID)
	if err != nil {
		t.Fatalf("resume run: %v", err)
	}
	if resumed.Status != models.RunStatusSuccess {
		t.Fatalf("resumed run status = %s, want success (error: %s)", resumed.Status, resumed.Error)
	}
	if sink.callCount() != 1 {
		t.Fatalf("sink call count after resume = %d, want 1 (it must not be re-executed)", sink.callCount())
	}
	if flaky.callCount() != 2 {
		t.Fatalf("flaky call count after resume = %d, want 2 (it must be retried, having failed originally)", flaky.callCount())
	}
}

// TestResumeRun_NonResumableSucceededNodeWithDownstreamFailsLoudly covers
// the other half of the resume contract: a node type that is explicitly
// declared non-resumable (nonResumableNodeTypes — notify/migrate/dbt) never
// gets a durable artifact even if it happened to succeed and return data.
// If something downstream still depends on that data, resume must fail
// loudly naming the node, instead of silently feeding the downstream node
// empty or synthesized data.
//
// Uses "dbt" rather than "notify"/"migrate" for the non-resumable node:
// pipeline structural validation forbids notify/migrate from having any
// outgoing edges at all (they're modeled as always-terminal), so a
// non-resumable node with a downstream consumer can only be built with dbt,
// which validation treats as a source-capable node instead.
func TestResumeRun_NonResumableSucceededNodeWithDownstreamFailsLoudly(t *testing.T) {
	eng, s := newResumeTestEngine(t)

	// This executor intercepts the real "dbt" node type entirely (the
	// executor dispatch in Runner.runNodeLogic runs before the built-in
	// switch), so it can succeed and return a real, non-nil DataSet without
	// needing a real dbt project on disk — dbt is exactly the kind of
	// external-side-effect node nonResumableNodeTypes is meant to describe.
	dbt := &controllableExecutor{nodeType: "dbt", onCall: func(call int, input *common.DataSet) (*common.DataSet, error) {
		return &common.DataSet{Columns: []string{"model"}, Rows: []common.DataRow{{"model": "orders"}}}, nil
	}}
	flaky := &controllableExecutor{nodeType: "flaky", onCall: failOnce(nil)}
	eng.Executors = []extensions.NodeExecutor{dbt, flaky}

	pipeline := &models.Pipeline{
		ID: "p-nonresumable", Name: "Non-resumable", Enabled: true,
		Nodes: []models.Node{
			{ID: "dbt", Type: models.NodeTypeDBT, Name: "DBT"},
			{ID: "flaky", Type: models.NodeType("flaky"), Name: "Flaky"},
		},
		Edges: []models.Edge{{From: "dbt", To: "flaky"}},
	}
	if err := s.CreatePipeline(pipeline); err != nil {
		t.Fatalf("create pipeline: %v", err)
	}

	firstRun, err := eng.RunPipeline(pipeline.ID)
	if err != nil && firstRun == nil {
		t.Fatalf("run pipeline: %v", err)
	}
	if firstRun.Status != models.RunStatusFailed {
		t.Fatalf("first run status = %s, want failed", firstRun.Status)
	}
	if dbt.callCount() != 1 {
		t.Fatalf("dbt call count after first run = %d, want 1", dbt.callCount())
	}

	resumed, err := eng.ResumeRun(firstRun.ID)
	if err == nil {
		t.Fatalf("expected resume to fail loudly for a non-resumable node with downstream consumers, got resumed run %+v", resumed)
	}
	if !strings.Contains(err.Error(), "dbt") {
		t.Fatalf("resume error = %q, want it to name the non-resumable node (dbt)", err.Error())
	}
	if resumed == nil || resumed.Status != models.RunStatusFailed {
		t.Fatalf("resumed run = %+v, want a persisted failed run explaining the loud failure", resumed)
	}
	// The dbt node itself must not have been re-invoked (it was already
	// successful and is being skipped, not retried).
	if dbt.callCount() != 1 {
		t.Fatalf("dbt call count after failed resume = %d, want 1 (must not be re-executed)", dbt.callCount())
	}
}

// TestResumeRun_PinsOriginalPipelineVersionNotLiveEdit is the "run
// definition snapshot" acceptance case: a pipeline edited between the
// original run and its resume must not change what the resume executes.
func TestResumeRun_PinsOriginalPipelineVersionNotLiveEdit(t *testing.T) {
	eng, s := newResumeTestEngine(t)
	dir := t.TempDir()
	csvPath := writeCSV(t, dir, "in.csv", "id,name\n1,brokoli\n")

	flaky := &controllableExecutor{nodeType: "flaky", onCall: failOnce(nil)}
	eng.Executors = []extensions.NodeExecutor{flaky}

	pipeline := &models.Pipeline{
		ID: "p-version-pin", Name: "Version Pin", Enabled: true,
		Nodes: []models.Node{
			{ID: "source", Type: models.NodeTypeSourceFile, Name: "Source", Config: map[string]interface{}{"path": csvPath, "format": "csv"}},
			{ID: "flaky", Type: models.NodeType("flaky"), Name: "Flaky"},
		},
		Edges: []models.Edge{{From: "source", To: "flaky"}},
	}
	if err := s.CreatePipeline(pipeline); err != nil {
		t.Fatalf("create pipeline: %v", err)
	}

	firstRun, err := eng.RunPipeline(pipeline.ID)
	if err != nil && firstRun == nil {
		t.Fatalf("run pipeline: %v", err)
	}
	if firstRun.Status != models.RunStatusFailed {
		t.Fatalf("first run status = %s, want failed", firstRun.Status)
	}
	if firstRun.PipelineVersion <= 0 {
		t.Fatalf("firstRun.PipelineVersion = %d, want a recorded version > 0", firstRun.PipelineVersion)
	}

	// Now edit the live pipeline: retype the still-failed "flaky" node (the
	// one resume must re-execute) to sql_generate with no config. The
	// source node stays untouched — it already succeeded and is skipped on
	// resume regardless of which pipeline definition is used, so mutating
	// it wouldn't distinguish "used the pinned snapshot" from "used the
	// live pipeline." Retyping the node that WILL actually execute does:
	// controllableExecutor only intercepts type "flaky", so if resume
	// wrongly re-fetches this edited live pipeline instead of the pinned
	// snapshot, the node falls through to the real sql_generate handler,
	// which requires a "table" config it doesn't have, and errors.
	edited, err := s.GetPipeline(pipeline.ID)
	if err != nil {
		t.Fatalf("get pipeline: %v", err)
	}
	for i := range edited.Nodes {
		if edited.Nodes[i].ID == "flaky" {
			edited.Nodes[i].Type = models.NodeTypeSQLGenerate
			edited.Nodes[i].Config = map[string]interface{}{}
		}
	}
	if err := s.UpdatePipeline(edited); err != nil {
		t.Fatalf("update pipeline: %v", err)
	}

	resumed, err := eng.ResumeRun(firstRun.ID)
	if err != nil {
		t.Fatalf("resume run: %v (should have used the pinned snapshot, not the edited live pipeline)", err)
	}
	if resumed.Status != models.RunStatusSuccess {
		t.Fatalf("resumed run status = %s, want success — resume must have used the ORIGINAL pipeline definition, not the edited live one (error: %s)", resumed.Status, resumed.Error)
	}
	if flaky.callCount() != 2 {
		t.Fatalf("flaky call count after resume = %d, want 2 — the pinned snapshot's \"flaky\" node type must still be handled by the test executor, not the edited live pipeline's sql_generate", flaky.callCount())
	}
}
