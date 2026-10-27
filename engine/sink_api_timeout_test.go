package engine

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Tnsor-Labs/brokoli/extensions"
	"github.com/Tnsor-Labs/brokoli/models"
	"github.com/Tnsor-Labs/brokoli/pkg/netguard"
	"github.com/Tnsor-Labs/brokoli/store"
)

// sink_api sent its batches under the run's context, so a node's timeout
// did not stop it: after the attempt timed out and the node had failed,
// the loop went on sending the remaining batches for as long as the run
// lasted. Three batches to an endpoint that takes 1.5s each, a 2s timeout,
// no retry, and a sibling node that keeps the run going for 6s: the
// batches after the timeout are no longer sent.
func TestSinkAPIStopsSendingWhenItsAttemptTimesOut(t *testing.T) {
	t.Cleanup(netguard.SetOutboundForTesting(netguard.Policy{AllowLoopback: true}))
	var received atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		received.Add(1)
		time.Sleep(1500 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	dir := t.TempDir()
	csv := filepath.Join(dir, "in.csv")
	if err := os.WriteFile(csv, []byte("id\n1\n2\n3\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	s, err := store.NewSQLiteStore(filepath.Join(dir, "meta.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	now := time.Now().UTC()
	pipe := &models.Pipeline{ID: "sink-api-timeout", Name: "sink api timeout", CreatedAt: now, UpdatedAt: now,
		Nodes: []models.Node{
			{ID: "src", Type: models.NodeTypeSourceFile, Name: "src", Config: map[string]interface{}{"path": csv, "format": "csv"}},
			{ID: "busy", Type: sleeperNode, Name: "busy", Config: map[string]interface{}{}},
			{ID: "sink", Type: models.NodeTypeSinkAPI, Name: "sink", Config: map[string]interface{}{
				"url": srv.URL, "method": "POST", "batch_size": float64(1), "timeout": float64(2),
			}},
		},
		Edges: []models.Edge{{From: "src", To: "sink"}, {From: "src", To: "busy"}},
	}
	if err := s.CreatePipeline(pipe); err != nil {
		t.Fatal(err)
	}
	if _, err := NewRunner(s, nil, pipe, nil, nil, []extensions.NodeExecutor{sleeperExecutor{6 * time.Second}}, nil, "", nil).Execute(); err == nil {
		t.Fatal("the node did not time out")
	}
	time.Sleep(2 * time.Second) // time for a batch already in flight to land
	if n := received.Load(); n > 2 {
		t.Fatalf("the endpoint received %d requests, want at most 2: batches were sent after the attempt timed out", n)
	}
}

const sleeperNode models.NodeType = "sleeper_test"

// sleeperExecutor keeps a run going: it sleeps, then passes its input on.
type sleeperExecutor struct{ d time.Duration }

func (sleeperExecutor) Name() string                   { return "sleeper-test" }
func (sleeperExecutor) CanHandle(nodeType string) bool { return nodeType == string(sleeperNode) }
func (e sleeperExecutor) Execute(ctx extensions.ExecutionContext) (*extensions.ExecutionResult, error) {
	time.Sleep(e.d)
	return &extensions.ExecutionResult{}, nil
}
