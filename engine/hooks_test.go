package engine

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Tnsor-Labs/brokoli/extensions"
	"github.com/Tnsor-Labs/brokoli/models"
	"github.com/Tnsor-Labs/brokoli/pkg/netguard"
	"github.com/Tnsor-Labs/brokoli/store"
)

// hookRecorder is a pipeline's hook endpoint: it keeps each call's payload
// in arrival order.
type hookRecorder struct {
	mu    sync.Mutex
	calls []map[string]string
	url   string
}

func newHookRecorder(t *testing.T) *hookRecorder {
	t.Helper()
	t.Cleanup(netguard.SetOutboundForTesting(netguard.Policy{AllowLoopback: true}))
	rec := &hookRecorder{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload map[string]string
		_ = json.NewDecoder(r.Body).Decode(&payload)
		rec.mu.Lock()
		rec.calls = append(rec.calls, payload)
		rec.mu.Unlock()
	}))
	t.Cleanup(srv.Close)
	rec.url = srv.URL
	return rec
}

func (h *hookRecorder) hooks() map[string]models.Hook {
	hook := models.Hook{Type: "webhook", URL: h.url, Enabled: true}
	return map[string]models.Hook{
		"on_start": hook, "on_success": hook, "on_failure": hook, "on_node_failure": hook,
	}
}

func (h *hookRecorder) events() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	var out []string
	for _, c := range h.calls {
		out = append(out, c["event"])
	}
	return out
}

func (h *hookRecorder) call(event string) map[string]string {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, c := range h.calls {
		if c["event"] == event {
			return c
		}
	}
	return nil
}

func hookTestStore(t *testing.T, pipe *models.Pipeline) *store.SQLiteStore {
	t.Helper()
	s, err := store.NewSQLiteStore(filepath.Join(t.TempDir(), "hooks.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	now := time.Now().UTC()
	pipe.CreatedAt, pipe.UpdatedAt = now, now
	if err := s.CreatePipeline(pipe); err != nil {
		t.Fatalf("create pipeline: %v", err)
	}
	return s
}

// A run that failed never called its on_failure hook: every failure leaves
// through failRun, which did not fire it, and the block that did could not
// be reached. on_node_failure, accepted by both IR schemas, was fired
// nowhere. A failing node now calls on_node_failure, and the run then
// calls on_failure.
func TestAFailedRunCallsItsFailureHooks(t *testing.T) {
	rec := newHookRecorder(t)
	pipe := &models.Pipeline{
		ID: "hooks-fail", Name: "Hooks fail", Hooks: rec.hooks(),
		Nodes: []models.Node{
			{ID: "source", Type: routingTestNode, Name: "Source", Config: map[string]interface{}{"source": true}},
			{ID: "bad", Type: routingTestNode, Name: "Bad", Config: map[string]interface{}{"fail": true}},
		},
		Edges: []models.Edge{{From: "source", To: "bad"}},
	}
	s := hookTestStore(t, pipe)

	run, err := NewRunner(s, nil, pipe, nil, nil, []extensions.NodeExecutor{newRoutingTestExecutor()}, nil, "", nil).Execute()
	if err == nil || run.Status != models.RunStatusFailed {
		t.Fatalf("run = %v, %v; want failed", run.Status, err)
	}

	if got, want := strings.Join(rec.events(), ","), "on_start,on_node_failure,on_failure"; got != want {
		t.Fatalf("hooks called = %s, want %s", got, want)
	}
	node := rec.call("on_node_failure")
	if node["node_id"] != "bad" || node["node"] != "Bad" || !strings.Contains(node["error"], "bad executed") {
		t.Fatalf("on_node_failure payload = %v, want the node and its error", node)
	}
	if failure := rec.call("on_failure"); !strings.Contains(failure["error"], "bad executed") || failure["run_id"] != run.ID {
		t.Fatalf("on_failure payload = %v, want the run and its error", failure)
	}
}

// A cancelled run calls on_failure, as before, and no node failed on its
// own, so on_node_failure is not called for the node the cancel stopped.
func TestACancelledRunDoesNotCallTheNodeFailureHook(t *testing.T) {
	rec := newHookRecorder(t)
	exec := &blockingUntilCancelExecutor{nodeType: "blocking", startedCh: make(chan struct{}), observed: make(chan error, 1)}
	pipe := &models.Pipeline{
		ID: "hooks-cancel", Name: "Hooks cancel", Hooks: rec.hooks(),
		Nodes: []models.Node{
			{ID: "source", Type: routingTestNode, Name: "Source", Config: map[string]interface{}{"source": true}},
			{ID: "blocking", Type: models.NodeType("blocking"), Name: "Blocking"},
		},
		Edges: []models.Edge{{From: "source", To: "blocking"}},
	}
	s := hookTestStore(t, pipe)

	runner := NewRunner(s, nil, pipe, nil, nil, []extensions.NodeExecutor{exec, newRoutingTestExecutor()}, nil, "", nil)
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = runner.Execute()
	}()
	select {
	case <-exec.startedCh:
	case <-time.After(10 * time.Second):
		t.Fatal("the blocking node never started")
	}
	runner.Cancel()
	select {
	case <-done:
	case <-time.After(15 * time.Second):
		t.Fatal("the cancelled run never finished")
	}

	if got, want := strings.Join(rec.events(), ","), "on_start,on_failure"; got != want {
		t.Fatalf("hooks called = %s, want %s", got, want)
	}
}

// A run that succeeds calls on_start and on_success and nothing else.
func TestASuccessfulRunCallsOnlyItsSuccessHooks(t *testing.T) {
	rec := newHookRecorder(t)
	pipe := &models.Pipeline{
		ID: "hooks-ok", Name: "Hooks ok", Hooks: rec.hooks(),
		Nodes: []models.Node{
			{ID: "source", Type: routingTestNode, Name: "Source", Config: map[string]interface{}{"source": true}},
		},
	}
	s := hookTestStore(t, pipe)

	run, err := NewRunner(s, nil, pipe, nil, nil, []extensions.NodeExecutor{newRoutingTestExecutor()}, nil, "", nil).Execute()
	if err != nil || run.Status != models.RunStatusSuccess {
		t.Fatalf("run = %v, %v; want success", run.Status, err)
	}
	if got, want := strings.Join(rec.events(), ","), "on_start,on_success"; got != want {
		t.Fatalf("hooks called = %s, want %s", got, want)
	}
}
