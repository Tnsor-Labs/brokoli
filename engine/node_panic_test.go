package engine

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Tnsor-Labs/brokoli/extensions"
	"github.com/Tnsor-Labs/brokoli/models"
	"github.com/Tnsor-Labs/brokoli/store"
)

const panicTestNode models.NodeType = "panic_test"

type panickingExecutor struct{}

func (panickingExecutor) Name() string                   { return "panic-test" }
func (panickingExecutor) CanHandle(nodeType string) bool { return nodeType == string(panicTestNode) }
func (panickingExecutor) Execute(extensions.ExecutionContext) (*extensions.ExecutionResult, error) {
	panic("executor exploded")
}

// A node handler runs in a goroutine of its own, under the attempt's
// timeout. A panic there was outside every recover, so one node took the
// whole process down: the server, the scheduler and every other run in it.
// It now fails that node, and the run with it.
func TestANodeThatPanicsFailsTheRunInsteadOfTheProcess(t *testing.T) {
	s, err := store.NewSQLiteStore(filepath.Join(t.TempDir(), "panic.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	now := time.Now().UTC()
	pipe := &models.Pipeline{
		ID: "node-panic", Name: "Node panic", CreatedAt: now, UpdatedAt: now,
		Nodes: []models.Node{
			{ID: "source", Type: routingTestNode, Name: "Source", Config: map[string]interface{}{"source": true}},
			{ID: "boom", Type: panicTestNode, Name: "Boom", Config: map[string]interface{}{}},
		},
		Edges: []models.Edge{{From: "source", To: "boom"}},
	}
	if err := s.CreatePipeline(pipe); err != nil {
		t.Fatalf("create pipeline: %v", err)
	}

	executors := []extensions.NodeExecutor{panickingExecutor{}, newRoutingTestExecutor()}
	run, runErr := NewRunner(s, nil, pipe, nil, nil, executors, nil, "", nil).Execute()
	if runErr == nil || !strings.Contains(runErr.Error(), "panicked") {
		t.Fatalf("run error = %v, want one saying the node panicked", runErr)
	}
	if run == nil || run.Status != models.RunStatusFailed {
		t.Fatalf("run = %+v, want failed", run)
	}
	stored, err := s.GetRun(run.ID)
	if err != nil {
		t.Fatalf("read run: %v", err)
	}
	if stored.Status != models.RunStatusFailed {
		t.Fatalf("stored run status = %s, want failed", stored.Status)
	}
	boom := latestNodeRuns(stored.NodeRuns)["boom"]
	if boom.Status != models.RunStatusFailed || !strings.Contains(boom.Error, "executor exploded") {
		t.Fatalf("node run = %s %q, want failed with the panic's value", boom.Status, boom.Error)
	}
}
