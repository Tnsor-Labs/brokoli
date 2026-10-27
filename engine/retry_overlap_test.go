package engine

import (
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Tnsor-Labs/brokoli/extensions"
	"github.com/Tnsor-Labs/brokoli/models"
	"github.com/Tnsor-Labs/brokoli/pkg/common"
	"github.com/Tnsor-Labs/brokoli/store"
)

const slowNode models.NodeType = "slow_test"

// slowExecutor ignores its context, as a handler blocked in a call that
// takes no context does, and records how many of its executions overlap.
type slowExecutor struct {
	sleeps    []time.Duration // per call; the last repeats
	calls     atomic.Int32
	active    atomic.Int32
	maxActive atomic.Int32
	mu        sync.Mutex
}

func (e *slowExecutor) Name() string                   { return "slow-test" }
func (e *slowExecutor) CanHandle(nodeType string) bool { return nodeType == string(slowNode) }
func (e *slowExecutor) Execute(ctx extensions.ExecutionContext) (*extensions.ExecutionResult, error) {
	n := int(e.calls.Add(1)) - 1
	now := e.active.Add(1)
	defer e.active.Add(-1)
	e.mu.Lock()
	if now > e.maxActive.Load() {
		e.maxActive.Store(now)
	}
	e.mu.Unlock()
	d := e.sleeps[len(e.sleeps)-1]
	if n < len(e.sleeps) {
		d = e.sleeps[n]
	}
	time.Sleep(d)
	return &extensions.ExecutionResult{OutputData: &common.DataSet{Columns: []string{"x"}, Rows: []common.DataRow{{"x": 1}}}}, nil
}

func runSlowNode(t *testing.T, exec *slowExecutor) (*models.Run, error) {
	t.Helper()
	s, err := store.NewSQLiteStore(filepath.Join(t.TempDir(), "slow.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	now := time.Now().UTC()
	pipe := &models.Pipeline{ID: "slow", Name: "slow", CreatedAt: now, UpdatedAt: now,
		Nodes: []models.Node{
			{ID: "source", Type: routingTestNode, Name: "Source", Config: map[string]interface{}{"source": true}},
			{ID: "slow", Type: slowNode, Name: "Slow", Config: map[string]interface{}{
				"timeout": float64(2), "max_retries": float64(2), "retry_delay": float64(10),
			}},
		},
		Edges: []models.Edge{{From: "source", To: "slow"}},
	}
	if err := s.CreatePipeline(pipe); err != nil {
		t.Fatal(err)
	}
	return NewRunner(s, nil, pipe, nil, nil, []extensions.NodeExecutor{exec, newRoutingTestExecutor()}, nil, "", nil).Execute()
}

// A timed-out attempt is abandoned, not stopped: the select moves on and
// the handler keeps running. The retry used to start alongside it, so a
// sink or migrate could write twice. An attempt still running after its
// timeout plus a grace period now fails the node without a retry.
func TestATimedOutAttemptThatKeepsRunningIsNotRetried(t *testing.T) {
	exec := &slowExecutor{sleeps: []time.Duration{20 * time.Second}}
	_, err := runSlowNode(t, exec)
	if err == nil {
		t.Fatal("the node succeeded")
	}
	if !strings.Contains(err.Error(), "still running") {
		t.Fatalf("error = %v, want one saying the attempt is still running", err)
	}
	if c := exec.calls.Load(); c != 1 {
		t.Fatalf("executed %d times, want 1 (no retry while the first is running)", c)
	}
	if m := exec.maxActive.Load(); m != 1 {
		t.Fatalf("%d executions overlapped", m)
	}
}

// An attempt that stops shortly after its timeout is retried as before,
// but only once it has stopped.
func TestATimedOutAttemptIsRetriedOnlyAfterItStops(t *testing.T) {
	exec := &slowExecutor{sleeps: []time.Duration{3 * time.Second, 10 * time.Millisecond}}
	run, err := runSlowNode(t, exec)
	if err != nil {
		t.Fatalf("run failed: %v", err)
	}
	if run.Status != models.RunStatusSuccess {
		t.Fatalf("run status = %s", run.Status)
	}
	if c := exec.calls.Load(); c != 2 {
		t.Fatalf("executed %d times, want 2", c)
	}
	if m := exec.maxActive.Load(); m != 1 {
		t.Fatalf("%d executions overlapped: the retry started before the timed-out attempt stopped", m)
	}
}
