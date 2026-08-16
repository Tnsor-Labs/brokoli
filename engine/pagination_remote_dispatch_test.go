package engine

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Tnsor-Labs/brokoli/extensions"
	"github.com/Tnsor-Labs/brokoli/models"
	"github.com/Tnsor-Labs/brokoli/store"
)

func TestSourceAPIRemotePaginationFallsBackWhenTerminationNeedsMetadata(t *testing.T) {
	r := &Runner{instanceJobQueue: newChannelJobQueue()}
	node := models.Node{ID: "source", Config: map[string]interface{}{}}
	_, handled, err := r.runSourceAPIRemotePages(node, "https://example.com", "rest", map[string]interface{}{
		"strategy":  "offset",
		"page_size": float64(2),
		"end_flag":  "done",
	}, nil)
	if err != nil {
		t.Fatalf("runSourceAPIRemotePages: %v", err)
	}
	if handled {
		t.Fatal("metadata-driven termination should remain on the in-process path")
	}
}

func TestBuildRemotePageSpecNumbered(t *testing.T) {
	spec := buildRemotePageSpec("numbered", map[string]interface{}{
		"start":      float64(3),
		"page_param": "page_number",
	}, 2)
	if spec.instanceKey != "page-5" || spec.params["page_number"] != "5" {
		t.Fatalf("spec = %+v, want page-5 with page_number=5", spec)
	}
}

func TestPipeline_SourceAPIPaginationRemoteDispatch(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
		w.Header().Set("Content-Type", "application/json")
		switch offset {
		case 0:
			_, _ = w.Write([]byte(`{"results":[{"id":1},{"id":2}]}`))
		case 2:
			_, _ = w.Write([]byte(`{"results":[{"id":3}]}`))
		case 4, 6:
			_, _ = w.Write([]byte(`{"results":[]}`))
		default:
			t.Errorf("unexpected offset %d", offset)
			_, _ = w.Write([]byte(`{"results":[]}`))
		}
	}))
	defer server.Close()

	eng, s := newResumeTestEngine(t)
	dir := t.TempDir()
	outPath := filepath.Join(dir, "out.csv")
	pipeline := &models.Pipeline{
		ID: "remote-pagination-pipeline", Name: "Remote pagination", Enabled: true,
		CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
		Nodes: []models.Node{
			{
				ID: "source", Type: models.NodeTypeSourceAPI, Name: "Source",
				Config: map[string]interface{}{
					"url":     server.URL,
					"records": "results",
					"pagination": map[string]interface{}{
						"strategy":  "offset",
						"page_size": float64(2),
					},
					"execution": map[string]interface{}{"max_concurrency": float64(2)},
				},
			},
			{ID: "sink", Type: models.NodeTypeSinkFile, Name: "Sink", Config: map[string]interface{}{"path": outPath, "format": "csv"}},
		},
		Edges: []models.Edge{{From: "source", To: "sink"}},
	}
	if err := s.CreatePipeline(pipeline); err != nil {
		t.Fatalf("CreatePipeline: %v", err)
	}

	eng.InstanceJobQueue = newChannelJobQueue()
	go runFakeInstanceWorkerLoop(t, eng.InstanceJobQueue.(*channelJobQueue), s, eng.ArtifactStore)
	t.Cleanup(func() { _ = eng.InstanceJobQueue.Close() })

	run, err := eng.RunPipeline(pipeline.ID)
	if err != nil {
		t.Fatalf("RunPipeline: %v", err)
	}
	if run.Status != models.RunStatusSuccess {
		t.Fatalf("run status = %s, want success (error: %s)", run.Status, run.Error)
	}

	output, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatalf("read output: %v", err)
	}
	for _, want := range []string{"1", "2", "3"} {
		if !strings.Contains(string(output), want) {
			t.Errorf("output %q does not contain id %s", output, want)
		}
	}
	for _, key := range []string{"page-0", "page-1", "page-2", "page-3"} {
		attempt, err := s.GetExecutionAttempt(run.ID, "source", key, 0)
		if err != nil {
			t.Fatalf("GetExecutionAttempt(%s): %v", key, err)
		}
		if attempt.Status != models.AttemptStatusCompleted {
			t.Errorf("attempt %s status = %s, want completed", key, attempt.Status)
		}
	}
}

type sourcePageRetryQueue struct {
	store     store.Store
	artifacts ArtifactStore
}

func (q *sourcePageRetryQueue) Enqueue(job extensions.RunJob) error {
	go func() { _ = ExecuteInstanceJob(q.store, q.artifacts, job) }()
	return nil
}
func (q *sourcePageRetryQueue) Dequeue() (extensions.RunJob, error) {
	return extensions.RunJob{}, extensions.ErrQueueClosed
}
func (q *sourcePageRetryQueue) Ack(string) error         { return nil }
func (q *sourcePageRetryQueue) Fail(string, error) error { return nil }
func (q *sourcePageRetryQueue) Len() int                 { return 0 }
func (q *sourcePageRetryQueue) Close() error             { return nil }

func TestPipeline_SourceAPIPaginationRemoteDispatchRetriesPage(t *testing.T) {
	var firstRequest atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		offset := r.URL.Query().Get("offset")
		if offset == "0" && firstRequest.CompareAndSwap(false, true) {
			http.Error(w, "transient page failure", http.StatusBadGateway)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if offset == "0" {
			_, _ = w.Write([]byte(`{"results":[{"id":1}]}`))
			return
		}
		_, _ = w.Write([]byte(`{"results":[]}`))
	}))
	defer server.Close()

	eng, s := newResumeTestEngine(t)
	dir := t.TempDir()
	outPath := filepath.Join(dir, "out.csv")
	pipeline := &models.Pipeline{
		ID: "remote-pagination-retry", Name: "Remote pagination retry", Enabled: true,
		CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
		Nodes: []models.Node{
			{ID: "source", Type: models.NodeTypeSourceAPI, Name: "Source", Config: map[string]interface{}{
				"url": server.URL, "records": "results", "max_retries": float64(0),
				"pagination": map[string]interface{}{"strategy": "offset", "page_size": float64(1)},
				"execution":  map[string]interface{}{"page_max_retries": float64(1)},
			}},
			{ID: "sink", Type: models.NodeTypeSinkFile, Name: "Sink", Config: map[string]interface{}{"path": outPath, "format": "csv"}},
		},
		Edges: []models.Edge{{From: "source", To: "sink"}},
	}
	if err := s.CreatePipeline(pipeline); err != nil {
		t.Fatalf("CreatePipeline: %v", err)
	}

	eng.InstanceJobQueue = &sourcePageRetryQueue{store: s, artifacts: eng.ArtifactStore}
	run, err := eng.RunPipeline(pipeline.ID)
	if err != nil {
		t.Fatalf("RunPipeline: %v", err)
	}
	if run.Status != models.RunStatusSuccess {
		t.Fatalf("run status = %s, want success (error: %s)", run.Status, run.Error)
	}

	failed, err := s.GetExecutionAttempt(run.ID, "source", "page-0", 0)
	if err != nil {
		t.Fatalf("GetExecutionAttempt(page-0): %v", err)
	}
	if failed.Status != models.AttemptStatusFailed {
		t.Errorf("first page attempt status = %s, want failed", failed.Status)
	}
	succeeded, err := s.GetExecutionAttempt(run.ID, "source", "page-0", 1)
	if err != nil {
		t.Fatalf("GetExecutionAttempt(page-0 retry): %v", err)
	}
	if succeeded.Status != models.AttemptStatusCompleted {
		t.Errorf("retry page attempt status = %s, want completed", succeeded.Status)
	}
}
