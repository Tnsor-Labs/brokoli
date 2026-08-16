package engine

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Tnsor-Labs/brokoli/extensions"
	"github.com/Tnsor-Labs/brokoli/models"
	"github.com/Tnsor-Labs/brokoli/store"
)

type recordingJobQueue struct {
	mu         sync.Mutex
	jobs       []extensions.RunJob
	enqueueErr error
}

func (q *recordingJobQueue) Enqueue(job extensions.RunJob) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.enqueueErr != nil {
		return q.enqueueErr
	}
	q.jobs = append(q.jobs, job)
	return nil
}

func (q *recordingJobQueue) Dequeue() (extensions.RunJob, error) {
	return extensions.RunJob{}, extensions.ErrQueueClosed
}
func (q *recordingJobQueue) Ack(string) error         { return nil }
func (q *recordingJobQueue) Fail(string, error) error { return nil }
func (q *recordingJobQueue) Len() int                 { return len(q.jobs) }
func (q *recordingJobQueue) Close() error             { return nil }

func newQueuedRunTestEngine(t *testing.T, queue extensions.JobQueue) (*Engine, *store.SQLiteStore, string) {
	t.Helper()
	s, err := store.NewSQLiteStore(filepath.Join(t.TempDir(), "queued.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })

	csvPath := filepath.Join(t.TempDir(), "input.csv")
	if err := os.WriteFile(csvPath, []byte("id,name\n1,brokoli\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	pipeline := &models.Pipeline{
		ID:   "queued-pipeline",
		Name: "Queued pipeline",
		Nodes: []models.Node{{
			ID:     "source",
			Type:   models.NodeTypeSourceFile,
			Name:   "Source",
			Config: map[string]interface{}{"path": csvPath, "format": "csv"},
		}},
		CreatedAt: time.Now().UTC(),
		UpdatedAt: time.Now().UTC(),
	}
	if err := s.CreatePipeline(pipeline); err != nil {
		t.Fatal(err)
	}

	eng := drainEngineOnCleanup(t, NewEngine(s))
	eng.JobQueue = queue
	return eng, s, pipeline.ID
}

func TestRunPipelineAsyncPersistsQueuedRunIdentity(t *testing.T) {
	queue := &recordingJobQueue{}
	eng, s, pipelineID := newQueuedRunTestEngine(t, queue)

	runID, err := eng.RunPipelineAsync(pipelineID, map[string]string{"date": "2026-08-01"})
	if err != nil {
		t.Fatal(err)
	}
	if len(queue.jobs) != 1 {
		t.Fatalf("queued jobs = %d, want 1", len(queue.jobs))
	}
	job := queue.jobs[0]
	if job.ID != runID || job.RunID != runID {
		t.Fatalf("job identity = ID %q RunID %q, want %q", job.ID, job.RunID, runID)
	}
	run, err := s.GetRun(runID)
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != models.RunStatusPending {
		t.Fatalf("accepted run status = %s, want pending", run.Status)
	}
	if run.Params["date"] != "2026-08-01" {
		t.Fatalf("accepted run params = %v", run.Params)
	}
}

func TestRunPipelineAsyncWithCapabilitiesCarriesPlacementRequirements(t *testing.T) {
	queue := &recordingJobQueue{}
	eng, _, pipelineID := newQueuedRunTestEngine(t, queue)

	if _, err := eng.RunPipelineAsyncWithCapabilities(pipelineID, []string{"gpu", "linux"}); err != nil {
		t.Fatal(err)
	}
	if len(queue.jobs) != 1 {
		t.Fatalf("queued jobs = %d, want 1", len(queue.jobs))
	}
	if got := queue.jobs[0].RequiredCapabilities; len(got) != 2 || got[0] != "gpu" || got[1] != "linux" {
		t.Fatalf("required capabilities = %v, want [gpu linux]", got)
	}
}

func TestRunPipelineAsyncLocalBypassesPipelineQueue(t *testing.T) {
	queue := &recordingJobQueue{}
	eng, s, pipelineID := newQueuedRunTestEngine(t, queue)

	runID, err := eng.RunPipelineAsyncLocal(pipelineID)
	if err != nil {
		t.Fatal(err)
	}
	if len(queue.jobs) != 0 {
		t.Fatalf("queued jobs = %d, want local execution to bypass pipeline queue", len(queue.jobs))
	}

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		run, getErr := s.GetRun(runID)
		if getErr == nil && run.Status == models.RunStatusSuccess {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	run, err := s.GetRun(runID)
	if err != nil {
		t.Fatal(err)
	}
	t.Fatalf("local run status = %s, want success", run.Status)
}

func TestExecuteQueuedRunUsesAcceptedIDAndDeduplicatesDelivery(t *testing.T) {
	queue := &recordingJobQueue{}
	eng, s, pipelineID := newQueuedRunTestEngine(t, queue)
	runID, err := eng.RunPipelineAsync(pipelineID)
	if err != nil {
		t.Fatal(err)
	}

	var wg sync.WaitGroup
	results := make(chan *models.Run, 2)
	errs := make(chan error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			run, executeErr := eng.ExecuteQueuedRun(runID, pipelineID, nil)
			results <- run
			errs <- executeErr
		}()
	}
	wg.Wait()
	close(results)
	close(errs)
	for executeErr := range errs {
		if executeErr != nil && !strings.Contains(executeErr.Error(), "concurrently claimed") && !strings.Contains(executeErr.Error(), "already running") {
			t.Fatalf("ExecuteQueuedRun: %v", executeErr)
		}
	}
	completed := 0
	for run := range results {
		if run == nil {
			continue
		}
		completed++
		if run.ID != runID {
			t.Fatalf("executed run = %+v, want ID %s", run, runID)
		}
	}
	if completed == 0 {
		t.Fatal("neither delivery returned the accepted run")
	}

	stored, err := s.GetRun(runID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Status != models.RunStatusSuccess {
		t.Fatalf("stored status = %s, want success", stored.Status)
	}
	if len(stored.NodeRuns) != 1 {
		t.Fatalf("node runs = %d, want exactly 1", len(stored.NodeRuns))
	}
	if stored.NodeRuns[0].RunID != runID {
		t.Fatalf("node run ID = %s, want %s", stored.NodeRuns[0].RunID, runID)
	}

	redelivered, err := eng.ExecuteQueuedRun(runID, pipelineID, nil)
	if err != nil {
		t.Fatal(err)
	}
	if redelivered.ID != runID || len(redelivered.NodeRuns) != 1 {
		t.Fatalf("redelivered run = %+v", redelivered)
	}
}

func TestRunPipelineAsyncMarksPublicationFailureTerminal(t *testing.T) {
	queue := &recordingJobQueue{enqueueErr: errors.New("queue unavailable")}
	eng, s, pipelineID := newQueuedRunTestEngine(t, queue)

	if _, err := eng.RunPipelineAsync(pipelineID); err == nil {
		t.Fatal("expected enqueue error")
	}
	runs, err := s.ListRunsByPipeline(pipelineID, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 1 || runs[0].Status != models.RunStatusFailed {
		t.Fatalf("runs after enqueue failure = %+v", runs)
	}
	if !strings.Contains(runs[0].Error, "queue unavailable") {
		t.Fatalf("publication failure error = %q", runs[0].Error)
	}
}

func TestExecuteQueuedRunPersistsPreExecutionFailure(t *testing.T) {
	queue := &recordingJobQueue{}
	eng, s, pipelineID := newQueuedRunTestEngine(t, queue)
	runID, err := eng.RunPipelineAsync(pipelineID)
	if err != nil {
		t.Fatal(err)
	}
	pipeline, err := s.GetPipeline(pipelineID)
	if err != nil {
		t.Fatal(err)
	}
	pipeline.Nodes = nil
	if err := s.UpdatePipeline(pipeline); err != nil {
		t.Fatal(err)
	}

	run, err := eng.ExecuteQueuedRun(runID, pipelineID, nil)
	if err == nil {
		t.Fatal("expected validation error")
	}
	if run == nil || run.Status != models.RunStatusFailed || run.Error == "" {
		t.Fatalf("failed accepted run = %+v", run)
	}
	persisted, getErr := s.GetRun(runID)
	if getErr != nil {
		t.Fatal(getErr)
	}
	if persisted.Status != models.RunStatusFailed || persisted.Error == "" {
		t.Fatalf("persisted pre-execution failure = %+v", persisted)
	}
}

func TestExecuteQueuedRunRejectsOrphanedDelivery(t *testing.T) {
	queue := &recordingJobQueue{}
	eng, _, pipelineID := newQueuedRunTestEngine(t, queue)

	run, err := eng.ExecuteQueuedRun("missing-run", pipelineID, nil)
	if run != nil || !errors.Is(err, ErrInvalidQueuedRun) {
		t.Fatalf("orphaned delivery = run %+v, error %v", run, err)
	}
}
