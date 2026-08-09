package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Tnsor-Labs/brokoli/engine"
	"github.com/Tnsor-Labs/brokoli/store"
)

// TestPrometheusHandlerReportsEventAndAttemptMetrics exercises the metrics
// gaps Tnsor-Labs/brokoli#11 fills for event append/replay
// (Tnsor-Labs/brokoli#6) and node-attempt/queue-depth
// (Tnsor-Labs/brokoli#7): PrometheusHandler must expose the new counters
// and gauges, reflecting whatever the Engine has actually recorded.
func TestPrometheusHandlerReportsEventAndAttemptMetrics(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "metrics-gaps.db")
	s, err := store.NewSQLiteStore(dbPath)
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}
	t.Cleanup(func() { s.Close() })

	eng := engine.NewEngine(s)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = eng.Close(ctx)
	})
	// Directly set the counters PrometheusHandler reads — these are plain
	// exported int64 fields updated via atomic.AddInt64 by engine/runner.go
	// and engine/recovery.go call sites (see engine/engine.go's doc
	// comments on RunsTotal et al. for why the field is a plain int64
	// rather than atomic.Int64: consistent with the pre-existing
	// RunsTotal/RunsSucceeded/RunsFailed/RunsRecovered pattern this extends).
	eng.EventsAppended = 42
	eng.EventsAppendFailed = 3
	eng.EventsReplayed = 17
	eng.AttemptsStarted = 10
	eng.AttemptsSucceeded = 7
	eng.AttemptsFailed = 2
	eng.AttemptsRetried = 1
	eng.RunsQueueWaiting = 5

	metrics := NewMetrics()
	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	rec := httptest.NewRecorder()
	PrometheusHandler(metrics, s, eng, nil)(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	body := rec.Body.String()

	wantLines := []string{
		"brokoli_run_events_appended_total 42\n",
		"brokoli_run_events_append_failed_total 3\n",
		"brokoli_run_events_replayed_total 17\n",
		"brokoli_node_attempts_started_total 10\n",
		"brokoli_node_attempts_succeeded_total 7\n",
		"brokoli_node_attempts_failed_total 2\n",
		"brokoli_node_attempts_retried_total 1\n",
		"brokoli_runs_queue_waiting 5\n",
	}
	for _, want := range wantLines {
		if !strings.Contains(body, want) {
			t.Errorf("expected metrics body to contain %q, got:\n%s", want, body)
		}
	}
}
