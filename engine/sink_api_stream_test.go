package engine

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/Tnsor-Labs/brokoli/models"
	"github.com/Tnsor-Labs/brokoli/pkg/netguard"
)

// captureAPI records what a sink_api actually posted, so the two paths can be
// compared on the requests they made rather than on whether they errored.
type captureAPI struct {
	mu       sync.Mutex
	bodies   []string
	requests int
}

func (c *captureAPI) handler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		c.mu.Lock()
		c.bodies = append(c.bodies, string(b))
		c.requests++
		c.mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}
}

// A sink_api that streams its input must send exactly what a sink_api reading
// the whole dataset sends: the same batches, the same size, the same order,
// the same bytes. The batch size is a contract with the receiving API — how
// many records it will accept in one request — and it must not change because
// the data happened to arrive by reference.
func TestSinkAPIStreamedSendsTheSameRequests(t *testing.T) {
	const rows = 250
	for _, batchSize := range []int{1, 7, 100, 250, 251} {
		t.Run(fmt.Sprintf("batch=%d", batchSize), func(t *testing.T) {
			run := func(streaming bool) (*captureAPI, bool) {
				cap := &captureAPI{}
				srv := httptest.NewServer(cap.handler())
				defer srv.Close()

				eng, st := newExecCtxTestEngine(t)
				eng.ArtifactStore = NewLocalDiskArtifactStore(filepath.Join(t.TempDir(), "artifacts"))
				if streaming {
					eng.SpillThresholdBytes = 1
					eng.StreamThresholdBytes = 1
				} else {
					eng.StreamThresholdBytes = -1
				}
				// The guard blocks loopback by default, and the test server is
				// loopback. Allowing it here is the only way to exercise the
				// real request path.
				restore := netguard.SetOutboundForTesting(netguard.Policy{AllowLoopback: true})
				defer restore()

				p := &models.Pipeline{
					ID: fmt.Sprintf("p-sinkapi-%v-%d", streaming, batchSize), Enabled: true,
					Name: "sink_api streaming",
					Nodes: []models.Node{
						{ID: "gen", Type: models.NodeTypeCode, Name: "gen",
							Capabilities: []string{models.CapabilitySource, models.CapabilityDatasetOutput},
							Config: map[string]interface{}{"script": fmt.Sprintf(`
output_data = {"columns": ["id", "name"], "rows": [{"id": i, "name": "n" + str(i)} for i in range(%d)]}
`, rows)}},
						{ID: "out", Type: models.NodeTypeSinkAPI, Name: "out",
							Config: map[string]interface{}{
								"url": srv.URL, "batch_size": float64(batchSize)}},
					},
					Edges: []models.Edge{{From: "gen", To: "out"}},
				}
				if err := st.CreatePipeline(p); err != nil {
					t.Fatal(err)
				}
				r, err := eng.RunPipeline(p.ID)
				if err != nil {
					t.Fatalf("run: %v", err)
				}
				if r.Status != models.RunStatusSuccess {
					t.Fatalf("status %s: %s", r.Status, r.Error)
				}
				logs, err := st.GetLogs(r.ID)
				if err != nil {
					t.Fatal(err)
				}
				didStream := false
				for _, l := range logs {
					if l.NodeID == "out" && strings.Contains(l.Message, "never materialized") {
						didStream = true
					}
				}
				return cap, didStream
			}

			materialized, controlStreamed := run(false)
			streamed, didStream := run(true)

			// Without this the test passes just as happily when both runs
			// materialise, which is the failure mode it exists to rule out.
			if controlStreamed {
				t.Fatal("the control run streamed; it must take the materialising path")
			}
			if !didStream {
				t.Fatal("the streaming run did not stream, so this comparison proves nothing")
			}

			if materialized.requests == 0 {
				t.Fatal("the control run posted nothing; the comparison would be vacuous")
			}
			if streamed.requests != materialized.requests {
				t.Fatalf("request count differs: streamed %d, materialized %d",
					streamed.requests, materialized.requests)
			}
			for i := range materialized.bodies {
				if streamed.bodies[i] != materialized.bodies[i] {
					t.Fatalf("request %d differs:\n streamed %s\n batch    %s",
						i, truncate(streamed.bodies[i]), truncate(materialized.bodies[i]))
				}
			}

			// And the batch size really was honoured, rather than both paths
			// agreeing on some other size.
			var first []map[string]interface{}
			if err := json.Unmarshal([]byte(materialized.bodies[0]), &first); err != nil {
				t.Fatal(err)
			}
			want := batchSize
			if want > rows {
				want = rows
			}
			if len(first) != want {
				t.Errorf("first batch had %d rows, want %d", len(first), want)
			}
		})
	}
}

func truncate(s string) string {
	if len(s) > 120 {
		return s[:120] + "..."
	}
	return s
}
