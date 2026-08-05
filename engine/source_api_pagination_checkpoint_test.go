package engine

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"testing"

	"github.com/Tnsor-Labs/brokoli/models"
)

// TestSourceAPI_PaginationCheckpoint_ResumesAcrossNodeRetry is the full-
// pipeline acceptance test for issue #41 M2: a paginated source_api node
// with checkpoint_every set persists progress as it goes, and a node-level
// retry (triggered here by a transient failure on one page, after
// page-level retry is exhausted) resumes from the last checkpoint instead
// of re-fetching pages already retrieved.
func TestSourceAPI_PaginationCheckpoint_ResumesAcrossNodeRetry(t *testing.T) {
	const totalRecords = 10
	const pageSize = 2

	var mu sync.Mutex
	requestCounts := map[int]int{}
	// offset=4 fails its first 3 requests (exhausting page-level retry
	// within one node attempt: max_retries=2 → attempts 0,1,2, all on the
	// FIRST node attempt), then succeeds from the 4th request — which
	// lands on the SECOND node attempt, after checkpoint resume skips
	// straight past offsets 0 and 2.
	const offsetFourFailUntilRequest = 3

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))

		mu.Lock()
		requestCounts[offset]++
		count := requestCounts[offset]
		mu.Unlock()

		if offset == 4 && count <= offsetFourFailUntilRequest {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}

		items := []map[string]interface{}{}
		for i := offset; i < offset+pageSize && i < totalRecords; i++ {
			items = append(items, map[string]interface{}{"id": i})
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"items":    items,
			"end_flag": offset+pageSize >= totalRecords,
		})
	}))
	defer server.Close()

	eng, s := newResumeTestEngine(t)

	pipeline := &models.Pipeline{
		ID: "p-source-api-pagination-checkpoint", Name: "Source API Pagination Checkpoint", Enabled: true,
		Nodes: []models.Node{
			{
				ID: "source", Type: models.NodeTypeSourceAPI, Name: "Checkpointed Source",
				Config: map[string]interface{}{
					"url":         server.URL,
					"records":     "items",
					"max_retries": float64(2),
					"retry_delay": float64(10), // node-level retry delay, milliseconds
					"pagination": map[string]interface{}{
						"strategy":  "offset",
						"page_size": float64(pageSize),
						"end_flag":  "end_flag",
					},
					"execution": map[string]interface{}{
						"checkpoint_every": float64(1),
					},
				},
			},
		},
	}
	if err := s.CreatePipeline(pipeline); err != nil {
		t.Fatalf("create pipeline: %v", err)
	}

	run, err := eng.RunPipeline(pipeline.ID)
	if err != nil {
		t.Fatalf("run pipeline: %v", err)
	}
	if run.Status != models.RunStatusSuccess {
		t.Fatalf("run status = %s, want success (error: %s)", run.Status, run.Error)
	}

	nodeRuns, err := s.ListNodeRunsByRun(run.ID)
	if err != nil {
		t.Fatalf("list node runs: %v", err)
	}
	var sourceRun *models.NodeRun
	for i := range nodeRuns {
		if nodeRuns[i].NodeID == "source" && nodeRuns[i].Status == models.RunStatusSuccess {
			sourceRun = &nodeRuns[i]
		}
	}
	if sourceRun == nil {
		t.Fatalf("no successful node run found for source among: %+v", nodeRuns)
	}
	if sourceRun.RowCount != totalRecords {
		t.Errorf("source node row count = %d, want %d", sourceRun.RowCount, totalRecords)
	}

	mu.Lock()
	defer mu.Unlock()
	// Offsets 0 and 2 succeeded on the first node attempt and must NOT be
	// re-fetched on the retry — that's the entire point of checkpointing.
	if requestCounts[0] != 1 {
		t.Errorf("offset=0 requested %d times, want exactly 1 (checkpoint should have skipped re-fetching it on retry)", requestCounts[0])
	}
	if requestCounts[2] != 1 {
		t.Errorf("offset=2 requested %d times, want exactly 1 (checkpoint should have skipped re-fetching it on retry)", requestCounts[2])
	}
	// offset=4 failed 3 times, then succeeded on request 4.
	if requestCounts[4] != offsetFourFailUntilRequest+1 {
		t.Errorf("offset=4 requested %d times, want %d", requestCounts[4], offsetFourFailUntilRequest+1)
	}
	if requestCounts[6] != 1 || requestCounts[8] != 1 {
		t.Errorf("offset=6/8 requested %d/%d times, want 1/1", requestCounts[6], requestCounts[8])
	}

	// The checkpoint must be cleared once the node succeeds — a later run
	// must not see stale interim state from this one.
	if _, _, err := eng.PaginationCheckpointStore.LoadCheckpoint(run.ID, "source"); err == nil {
		t.Error("expected no checkpoint to remain after a successful fetch, but LoadCheckpoint found one")
	}
}
