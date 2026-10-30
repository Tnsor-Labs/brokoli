package engine

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/Tnsor-Labs/brokoli/models"
)

func TestRemotePageRetryDelayFollowsTheConfiguredBackoff(t *testing.T) {
	for _, tc := range []struct {
		backoff string
		attempt int
		want    time.Duration
	}{
		{"exponential", 0, time.Second},
		{"exponential", 3, 8 * time.Second},
		{"exponential", 50, 32 * time.Second},
		{"linear", 2, 3 * time.Second},
		{"none", 4, 0},
		// The node inspector offers fixed; page retries treated it as
		// exponential.
		{"fixed", 0, time.Second},
		{"Fixed", 7, time.Second},
	} {
		if got := remotePageRetryDelay(tc.backoff, tc.attempt); got != tc.want {
			t.Errorf("remotePageRetryDelay(%q, %d) = %v, want %v", tc.backoff, tc.attempt, got, tc.want)
		}
	}
}

func TestJitterRemotePageRetrySpreadsWithinTheUpperHalf(t *testing.T) {
	if got := jitterRemotePageRetry(0); got != 0 {
		t.Errorf("no backoff jittered to %v, want 0", got)
	}
	d := 8 * time.Second
	seen := map[time.Duration]bool{}
	lowest, highest := d, time.Duration(0)
	for range 500 {
		got := jitterRemotePageRetry(d)
		if got < d/2 || got > d {
			t.Fatalf("jittered wait %v outside [%v, %v]", got, d/2, d)
		}
		seen[got] = true
		lowest, highest = min(lowest, got), max(highest, got)
	}
	// 500 uniform draws leave less than a 1e-50 chance of any of these.
	if len(seen) < 400 || lowest > d/2+d/8 || highest < d-d/8 {
		t.Errorf("%d distinct waits in [%v, %v]; want them spread across [%v, %v]", len(seen), lowest, highest, d/2, d)
	}
}

// A cancel must end a page's retries. The run is reported cancelled at
// once, but the page's goroutine went on: it finished its time.Sleep,
// claimed the next attempt against the cancelled run, failed it and slept
// again, through the whole schedule (1+2+4+8+16+32 seconds here), with
// each attempt sent to the instance queue.
func TestCancelStopsARemotePageRetrying(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "always failing", http.StatusBadGateway)
	}))
	defer server.Close()

	eng, s := newResumeTestEngine(t)
	pipeline := &models.Pipeline{
		ID: "remote-pagination-cancel", Name: "Remote pagination cancel", Enabled: true,
		CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
		Nodes: []models.Node{
			{ID: "source", Type: models.NodeTypeSourceAPI, Name: "Source", Config: map[string]interface{}{
				"url": server.URL, "records": "results", "max_retries": float64(0),
				"pagination": map[string]interface{}{"strategy": "offset", "page_size": float64(1)},
				"execution":  map[string]interface{}{"page_max_retries": float64(6), "page_retry_backoff": "exponential"},
			}},
			{ID: "sink", Type: models.NodeTypeSinkFile, Name: "Sink", Config: map[string]interface{}{"path": filepath.Join(t.TempDir(), "out.csv"), "format": "csv"}},
		},
		Edges: []models.Edge{{From: "source", To: "sink"}},
	}
	if err := s.CreatePipeline(pipeline); err != nil {
		t.Fatalf("CreatePipeline: %v", err)
	}
	eng.InstanceJobQueue = &sourcePageRetryQueue{store: s, artifacts: eng.ArtifactStore}

	type result struct {
		run *models.Run
		err error
	}
	done := make(chan result, 1)
	go func() {
		run, err := eng.RunPipeline(pipeline.ID)
		done <- result{run, err}
	}()

	// Cancel once the first attempt has failed, so the page is waiting to
	// retry.
	var runID string
	deadline := time.Now().Add(15 * time.Second)
	for runID == "" {
		if time.Now().After(deadline) {
			t.Fatal("the first page attempt never failed")
		}
		if runs, err := s.ListRunsByPipeline(pipeline.ID, 1); err == nil && len(runs) == 1 {
			if a, err := s.GetExecutionAttempt(runs[0].ID, "source", "page-0", 0); err == nil && a.Status == models.AttemptStatusFailed {
				runID = runs[0].ID
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err := eng.CancelRun(runID); err != nil {
		t.Fatalf("CancelRun: %v", err)
	}

	select {
	case res := <-done:
		if res.run == nil {
			t.Fatalf("RunPipeline returned no run (err %v)", res.err)
		}
		if res.run.Status != models.RunStatusCancelled {
			t.Errorf("run status = %s, want cancelled (error: %s)", res.run.Status, res.run.Error)
		}
		// The first retry waits at most 1s; give it that and more before
		// looking for one.
		time.Sleep(2500 * time.Millisecond)
		if a, err := s.GetExecutionAttempt(runID, "source", "page-0", 1); err == nil {
			t.Errorf("the page was retried after the run was cancelled: attempt 1 %s (%s)", a.Status, a.Error)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("the cancelled run never returned")
	}
}
