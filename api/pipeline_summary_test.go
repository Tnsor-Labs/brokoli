package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Tnsor-Labs/brokoli/models"
	"github.com/Tnsor-Labs/brokoli/store"
)

// summaryStore fails the reads the summary makes, one at a time, the way a
// database blip does.
type summaryStore struct {
	store.Store
	failList, failCounts, failRuns bool
}

var errSummaryBlip = errors.New("database is unavailable")

func (s summaryStore) ListPipelines() ([]models.Pipeline, error) {
	if s.failList {
		return nil, errSummaryBlip
	}
	return s.Store.ListPipelines()
}

func (s summaryStore) ListPipelinesByWorkspace(ws string) ([]models.Pipeline, error) {
	if s.failList {
		return nil, errSummaryBlip
	}
	return s.Store.ListPipelinesByWorkspace(ws)
}

func (s summaryStore) AggregateRunsByPipelineStatus(since time.Time, scope store.RunScope) ([]store.RunAggregate, error) {
	if s.failCounts {
		return nil, errSummaryBlip
	}
	return s.Store.AggregateRunsByPipelineStatus(since, scope)
}

func (s summaryStore) ListRunsByPipeline(id string, limit int) ([]models.Run, error) {
	if s.failRuns {
		return nil, errSummaryBlip
	}
	return s.Store.ListRunsByPipeline(id, limit)
}

type summaryEntry struct {
	ID            string   `json:"id"`
	LastRunStatus string   `json:"last_run_status"`
	RunsTotal     int      `json:"runs_total"`
	RunsSuccess   int      `json:"runs_success"`
	RunsFailed    int      `json:"runs_failed"`
	RunsRunning   int      `json:"runs_running"`
	RunHistory    []string `json:"run_history"`
}

func getSummary(t *testing.T, s store.Store) (int, []summaryEntry) {
	t.Helper()
	rec := httptest.NewRecorder()
	pipelineSummaryHandler(s)(rec, httptest.NewRequest(http.MethodGet, "/api/pipelines/summary", nil))
	var out []summaryEntry
	if rec.Code == http.StatusOK {
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatalf("decode %s: %v", rec.Body.String(), err)
		}
	}
	return rec.Code, out
}

func seedSummaryPipeline(t *testing.T, s store.Store, succeeded, failed int) {
	t.Helper()
	if err := s.CreatePipeline(&models.Pipeline{ID: "busy", PipelineID: "busy", Name: "Busy", WorkspaceID: models.DefaultWorkspaceID, Nodes: []models.Node{}, Edges: []models.Edge{}}); err != nil {
		t.Fatal(err)
	}
	start := time.Now().UTC().Add(-time.Hour)
	for i := 0; i < succeeded+failed; i++ {
		status := models.RunStatusSuccess
		if i >= succeeded {
			status = models.RunStatusFailed
		}
		at := start.Add(time.Duration(i) * time.Second)
		if err := s.CreateRun(&models.Run{ID: fmt.Sprintf("r%03d", i), PipelineID: "busy", Status: status, StartedAt: &at}); err != nil {
			t.Fatal(err)
		}
	}
}

// The counts are every run's, not the newest 200's (#624): a pipeline with
// 250 runs reports 250, and its history and last run come from the newest.
func TestPipelineSummaryCountsEveryRun(t *testing.T) {
	s := newOrgCheckStore(t)
	seedSummaryPipeline(t, s, 205, 45)
	code, got := getSummary(t, s)
	if code != http.StatusOK || len(got) != 1 {
		t.Fatalf("status=%d entries=%v", code, got)
	}
	e := got[0]
	if e.RunsTotal != 250 || e.RunsSuccess != 205 || e.RunsFailed != 45 || e.RunsRunning != 0 {
		t.Errorf("counts total=%d success=%d failed=%d running=%d, want 250, 205, 45, 0", e.RunsTotal, e.RunsSuccess, e.RunsFailed, e.RunsRunning)
	}
	if len(e.RunHistory) != 5 || e.RunHistory[0] != "failed" || e.LastRunStatus != "failed" {
		t.Errorf("history=%v last=%q, want the five newest runs, newest failed", e.RunHistory, e.LastRunStatus)
	}
}

// A failed read answers 500, not a 200 that makes an outage look like an
// empty workspace or a pipeline that never ran (#591).
func TestPipelineSummaryReportsAFailedReadAsAnError(t *testing.T) {
	base := newOrgCheckStore(t)
	seedSummaryPipeline(t, base, 1, 0)
	for name, s := range map[string]summaryStore{
		"pipeline list": {Store: base, failList: true},
		"run counts":    {Store: base, failCounts: true},
		"recent runs":   {Store: base, failRuns: true},
	} {
		if code, got := getSummary(t, s); code != http.StatusInternalServerError {
			t.Errorf("%s failing: status %d (%v), want 500", name, code, got)
		}
	}
}
