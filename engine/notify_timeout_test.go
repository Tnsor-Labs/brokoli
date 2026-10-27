package engine

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Tnsor-Labs/brokoli/models"
	"github.com/Tnsor-Labs/brokoli/pkg/netguard"
	"github.com/Tnsor-Labs/brokoli/store"
)

// A notify node posted with no context and a 10s client timeout, so its
// attempt's deadline did not reach the request: a slow endpoint held the
// attempt past its timeout. The request now carries the attempt's
// deadline, and the endpoint sees it give up when the node times out.
func TestNotifyGivesUpWhenItsAttemptTimesOut(t *testing.T) {
	t.Cleanup(netguard.SetOutboundForTesting(netguard.Policy{AllowLoopback: true}))
	gaveUp := make(chan bool, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// The server notices a client that gave up only once the body has
		// been read.
		_, _ = io.ReadAll(r.Body)
		select {
		case <-r.Context().Done():
			gaveUp <- true
		case <-time.After(5 * time.Second):
			gaveUp <- false
			w.WriteHeader(http.StatusOK)
		}
	}))
	defer srv.Close()

	dir := t.TempDir()
	csv := filepath.Join(dir, "in.csv")
	if err := os.WriteFile(csv, []byte("id\n1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	s, err := store.NewSQLiteStore(filepath.Join(dir, "meta.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	now := time.Now().UTC()
	pipe := &models.Pipeline{ID: "notify-timeout", Name: "notify timeout", CreatedAt: now, UpdatedAt: now,
		Nodes: []models.Node{
			{ID: "src", Type: models.NodeTypeSourceFile, Name: "src", Config: map[string]interface{}{"path": csv, "format": "csv"}},
			{ID: "notify", Type: models.NodeTypeNotify, Name: "notify", Config: map[string]interface{}{
				"notify_type": "webhook", "webhook_url": srv.URL, "message": "done", "timeout": float64(2),
			}},
		},
		Edges: []models.Edge{{From: "src", To: "notify"}},
	}
	if err := s.CreatePipeline(pipe); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	_, _ = NewRunner(s, nil, pipe, nil, nil, nil, nil, "", nil).Execute()
	select {
	case cancelled := <-gaveUp:
		if !cancelled {
			t.Fatal("the request ran to the endpoint's 5s reply: the attempt's 2s deadline never reached it")
		}
		if waited := time.Since(start); waited > 4*time.Second {
			t.Fatalf("the request gave up after %s, want about 2s", waited)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the endpoint never heard from the node")
	}
}
