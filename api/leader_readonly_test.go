package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Tnsor-Labs/brokoli/engine"
	"github.com/Tnsor-Labs/brokoli/models"
	"github.com/Tnsor-Labs/brokoli/store"
	"github.com/go-chi/chi/v5"
)

// fakeLeaderElector is a minimal store.LeaderElector for exercising
// read-only API behavior against a non-leader Scheduler
// (Tnsor-Labs/brokoli#10 point 5), without needing a real database.
type fakeLeaderElector struct{ leader bool }

func (f *fakeLeaderElector) Acquire(context.Context) error { return nil }
func (f *fakeLeaderElector) Run(ctx context.Context)       { <-ctx.Done() }
func (f *fakeLeaderElector) IsLeader() bool                { return f.leader }
func (f *fakeLeaderElector) FencingGeneration() int64      { return 0 }
func (f *fakeLeaderElector) Acquisitions() int64           { return 0 }
func (f *fakeLeaderElector) Releases() int64               { return 0 }
func (f *fakeLeaderElector) ElectionFailures() int64       { return 0 }

func newLeaderReadOnlyTestStore(t *testing.T) store.Store {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "leader-readonly.db")
	s, err := store.NewSQLiteStore(dbPath)
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

// TestSchedulerStatusHandlerServesReadsWhenNotLeader is the regression test
// point 5 of Tnsor-Labs/brokoli#10 asks for: a standby instance (not
// leader) must still serve the read-only /scheduler/status endpoint
// exactly like a leader would — schedulerStatusHandler only ever calls
// sched.Status(), which never consults leadership state.
func TestSchedulerStatusHandlerServesReadsWhenNotLeader(t *testing.T) {
	s := newLeaderReadOnlyTestStore(t)
	now := time.Now().UTC()
	p := &models.Pipeline{
		ID: "read-only-pipeline", Name: "Read Only Pipeline",
		Nodes: []models.Node{}, Edges: []models.Edge{},
		Schedule: "0 0 * * *", Enabled: true,
		CreatedAt: now, UpdatedAt: now,
	}
	if err := s.CreatePipeline(p); err != nil {
		t.Fatalf("CreatePipeline: %v", err)
	}

	eng := engine.NewEngine(s)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = eng.Close(ctx)
	})
	sched := engine.NewScheduler(eng, s, &fakeLeaderElector{leader: false})
	if err := sched.Register(p.ID, p.Name, p.Schedule, ""); err != nil {
		t.Fatalf("Register: %v", err)
	}
	if sched.IsLeader() {
		t.Fatal("test setup bug: scheduler reports leader=true, expected false")
	}

	req := httptest.NewRequest(http.MethodGet, "/scheduler/status", nil)
	rec := httptest.NewRecorder()
	schedulerStatusHandler(sched)(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 from a non-leader instance, got %d: %s", rec.Code, rec.Body.String())
	}
	var infos []engine.ScheduleInfo
	if err := json.Unmarshal(rec.Body.Bytes(), &infos); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(infos) != 1 || infos[0].PipelineID != p.ID {
		t.Fatalf("expected schedule status for %s despite not being leader, got %+v", p.ID, infos)
	}
}

// TestRunLogsHandlerServesReadsWhenNotLeaderOrScheduler proves the run-logs
// read path (GET /runs/{id}/logs) works with no Scheduler at all — the
// zero-value case standbys/API-only pods actually run in
// (RunMode=="api"/"worker" never construct a Scheduler, see
// cmd/serve.go), confirming RunHandler has no leadership dependency
// whatsoever, not even an optional one.
func TestRunLogsHandlerServesReadsWhenNotLeaderOrScheduler(t *testing.T) {
	s := newLeaderReadOnlyTestStore(t)
	now := time.Now().UTC()
	p := &models.Pipeline{ID: "logs-pipeline", Name: "Logs Pipeline", Nodes: []models.Node{}, Edges: []models.Edge{}, CreatedAt: now, UpdatedAt: now}
	if err := s.CreatePipeline(p); err != nil {
		t.Fatalf("CreatePipeline: %v", err)
	}
	run := &models.Run{ID: "run-1", PipelineID: p.ID, Status: models.RunStatusSuccess, StartedAt: &now}
	if err := s.CreateRun(run); err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	if err := s.AppendLog(&models.LogEntry{RunID: run.ID, Level: models.LogLevelInfo, Message: "hello", Timestamp: now}); err != nil {
		t.Fatalf("AppendLog: %v", err)
	}

	eng := engine.NewEngine(s)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = eng.Close(ctx)
	})
	rh := NewRunHandler(s, eng)

	r := chi.NewRouter()
	r.Get("/runs/{id}/logs", rh.GetLogs)

	req := httptest.NewRequest(http.MethodGet, "/runs/"+run.ID+"/logs", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "hello") {
		t.Fatalf("expected log message in response, got %s", rec.Body.String())
	}
}

// TestPrometheusHandlerReportsLeaderStatus checks the observability surface
// added for Tnsor-Labs/brokoli#10 point 6: brokoli_leader_status reflects
// the Scheduler's current leadership, and is omitted entirely when sched is
// nil (API-only/worker-only instances that never run a Scheduler).
func TestPrometheusHandlerReportsLeaderStatus(t *testing.T) {
	s := newLeaderReadOnlyTestStore(t)
	eng := engine.NewEngine(s)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = eng.Close(ctx)
	})
	metrics := NewMetrics()

	t.Run("not leader", func(t *testing.T) {
		sched := engine.NewScheduler(eng, s, &fakeLeaderElector{leader: false})
		req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
		rec := httptest.NewRecorder()
		PrometheusHandler(metrics, s, eng, sched)(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d", rec.Code)
		}
		body := rec.Body.String()
		if !strings.Contains(body, "brokoli_leader_status 0\n") {
			t.Errorf("expected brokoli_leader_status 0, got body:\n%s", body)
		}
	})

	t.Run("leader", func(t *testing.T) {
		sched := engine.NewScheduler(eng, s, &fakeLeaderElector{leader: true})
		req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
		rec := httptest.NewRecorder()
		PrometheusHandler(metrics, s, eng, sched)(rec, req)

		body := rec.Body.String()
		if !strings.Contains(body, "brokoli_leader_status 1\n") {
			t.Errorf("expected brokoli_leader_status 1, got body:\n%s", body)
		}
	})

	t.Run("no scheduler component", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
		rec := httptest.NewRecorder()
		PrometheusHandler(metrics, s, eng, nil)(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("expected 200 even with no scheduler, got %d", rec.Code)
		}
		body := rec.Body.String()
		if strings.Contains(body, "brokoli_leader_status") {
			t.Errorf("expected no leader metrics when sched is nil, got body:\n%s", body)
		}
	})
}
