package engine

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/Tnsor-Labs/brokoli/models"
	"github.com/Tnsor-Labs/brokoli/pkg/netguard"
	"github.com/Tnsor-Labs/brokoli/store"
)

// source_api fetched with a background context, so a node's timeout did
// not reach its requests: a slow API held the request (and a paginated
// fetch went on to further pages) after the attempt had given up. The
// endpoint holds each request for 5s and the node times out after 2s;
// the endpoint now sees the request abandoned at the deadline.
func TestSourceAPIGivesUpWhenItsAttemptTimesOut(t *testing.T) {
	t.Cleanup(netguard.SetOutboundForTesting(netguard.Policy{AllowLoopback: true}))
	for name, extra := range map[string]map[string]interface{}{
		"single request": {},
		"paginated": {
			"records":    "data",
			"pagination": map[string]interface{}{"strategy": "cursor", "cursor_path": "next", "cursor_param": "cursor"},
		},
	} {
		t.Run(name, func(t *testing.T) {
			gaveUp := make(chan bool, 1)
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				select {
				case <-r.Context().Done():
					gaveUp <- true
				case <-time.After(5 * time.Second):
					gaveUp <- false
					w.Header().Set("Content-Type", "application/json")
					_, _ = w.Write([]byte(`{"data":[{"id":1}],"next":null}`))
				}
			}))
			defer srv.Close()

			s, err := store.NewSQLiteStore(filepath.Join(t.TempDir(), "meta.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			cfg := map[string]interface{}{"url": srv.URL, "method": "GET", "timeout": float64(2)}
			for k, v := range extra {
				cfg[k] = v
			}
			now := time.Now().UTC()
			pipe := &models.Pipeline{ID: "api-timeout", Name: "api timeout", CreatedAt: now, UpdatedAt: now,
				Nodes: []models.Node{{ID: "api", Type: models.NodeTypeSourceAPI, Name: "api", Config: cfg}}}
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
		})
	}
}
