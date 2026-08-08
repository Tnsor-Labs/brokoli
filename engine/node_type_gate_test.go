package engine

import (
	"strings"
	"testing"

	"github.com/Tnsor-Labs/brokoli/extensions"
	"github.com/Tnsor-Labs/brokoli/models"
)

// setNodeTypeGate installs a NodeTypeGateFunc for the duration of a test and
// restores the previous value (normally nil) afterward, since it's a
// package-level var in extensions and would otherwise leak into unrelated
// tests running later in the same package.
func setNodeTypeGate(t *testing.T, fn func(orgID, nodeType string) string) {
	t.Helper()
	prev := extensions.NodeTypeGateFunc
	extensions.NodeTypeGateFunc = fn
	t.Cleanup(func() { extensions.NodeTypeGateFunc = prev })
}

// TestNodeTypeGate_BlocksExecutorDispatchedNodeType locks in the fix for
// Tnsor-Labs/brokoli#64: NodeTypeGateFunc must apply to a node type that a
// registered NodeExecutor also claims, not just to built-in types. Before
// the fix, the executor loop ran first and returned before the gate check
// was ever reached for this node type.
func TestNodeTypeGate_BlocksExecutorDispatchedNodeType(t *testing.T) {
	eng, s := newExecCtxTestEngine(t)

	exec := &capturingExecutor{nodeType: "gated"}
	eng.Executors = []extensions.NodeExecutor{exec}

	const blockMsg = "node type \"gated\" is not available on your plan"
	setNodeTypeGate(t, func(orgID, nodeType string) string {
		if nodeType == "gated" {
			return blockMsg
		}
		return ""
	})

	csvPath := execCtxSourceCSV(t)
	pipeline := &models.Pipeline{
		ID: "p-gate-executor", Name: "Gate vs Executor", Enabled: true,
		Nodes: []models.Node{
			{ID: "source", Type: models.NodeTypeSourceFile, Name: "Source", Config: map[string]interface{}{"path": csvPath, "format": "csv"}},
			{ID: "gated", Type: models.NodeType("gated"), Name: "Gated", Config: map[string]interface{}{}},
		},
		Edges: []models.Edge{{From: "source", To: "gated"}},
	}
	if err := s.CreatePipeline(pipeline); err != nil {
		t.Fatalf("create pipeline: %v", err)
	}

	run, err := eng.RunPipeline(pipeline.ID)
	if err == nil {
		t.Fatal("RunPipeline returned nil error, want the gate's block message")
	}
	if !strings.Contains(err.Error(), blockMsg) {
		t.Errorf("err = %q, want it to contain the gate's message %q", err.Error(), blockMsg)
	}
	if run == nil || run.Status != models.RunStatusFailed {
		t.Fatalf("run = %+v, want status failed", run)
	}

	if calls := exec.snapshot(); len(calls) != 0 {
		t.Errorf("executor.Execute was called %d time(s), want 0 — the gate should have blocked the node before dispatch", len(calls))
	}
}

// TestNodeTypeGate_AllowsExecutorDispatchedNodeType is the companion
// positive case: when the gate permits the node type, dispatch to the
// executor still happens normally — the reordering in #64's fix must not
// break the case where gating and execution should both succeed.
func TestNodeTypeGate_AllowsExecutorDispatchedNodeType(t *testing.T) {
	eng, s := newExecCtxTestEngine(t)

	exec := &capturingExecutor{nodeType: "allowed"}
	eng.Executors = []extensions.NodeExecutor{exec}

	setNodeTypeGate(t, func(orgID, nodeType string) string {
		return "" // never blocks
	})

	csvPath := execCtxSourceCSV(t)
	pipeline := &models.Pipeline{
		ID: "p-gate-allows", Name: "Gate Allows Executor", Enabled: true,
		Nodes: []models.Node{
			{ID: "source", Type: models.NodeTypeSourceFile, Name: "Source", Config: map[string]interface{}{"path": csvPath, "format": "csv"}},
			{ID: "allowed", Type: models.NodeType("allowed"), Name: "Allowed", Config: map[string]interface{}{}},
		},
		Edges: []models.Edge{{From: "source", To: "allowed"}},
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
	if calls := exec.snapshot(); len(calls) != 1 {
		t.Errorf("executor.Execute was called %d time(s), want 1", len(calls))
	}
}

// TestNodeTypeGate_StillBlocksBuiltInNodeType is a regression guard: the
// gate must keep blocking built-in (non-executor-dispatched) node types
// exactly as before this reordering.
func TestNodeTypeGate_StillBlocksBuiltInNodeType(t *testing.T) {
	eng, s := newExecCtxTestEngine(t)

	const blockMsg = "sink_file is not available on your plan"
	setNodeTypeGate(t, func(orgID, nodeType string) string {
		if nodeType == string(models.NodeTypeSinkFile) {
			return blockMsg
		}
		return ""
	})

	csvPath := execCtxSourceCSV(t)
	pipeline := &models.Pipeline{
		ID: "p-gate-builtin", Name: "Gate Built-in", Enabled: true,
		Nodes: []models.Node{
			{ID: "source", Type: models.NodeTypeSourceFile, Name: "Source", Config: map[string]interface{}{"path": csvPath, "format": "csv"}},
			{ID: "sink", Type: models.NodeTypeSinkFile, Name: "Sink", Config: map[string]interface{}{"path": csvPath + ".out", "format": "csv"}},
		},
		Edges: []models.Edge{{From: "source", To: "sink"}},
	}
	if err := s.CreatePipeline(pipeline); err != nil {
		t.Fatalf("create pipeline: %v", err)
	}

	run, err := eng.RunPipeline(pipeline.ID)
	if err == nil {
		t.Fatal("RunPipeline returned nil error, want the gate's block message")
	}
	if !strings.Contains(err.Error(), blockMsg) {
		t.Errorf("err = %q, want it to contain the gate's message %q", err.Error(), blockMsg)
	}
	if run == nil || run.Status != models.RunStatusFailed {
		t.Fatalf("run = %+v, want status failed", run)
	}
}
