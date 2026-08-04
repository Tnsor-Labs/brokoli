package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Tnsor-Labs/brokoli/engine"
)

// TestLeaderHealthHandlerReflectsLeadership exercises the dedicated
// readiness endpoint: a Kubernetes readiness probe's `httpGet` should
// be able to target /health/leader
// directly and get 200 while this instance holds scheduler leadership, 503
// while it's a standby — instead of having to exec into the container or
// grep /metrics for brokoli_leader_status.
func TestLeaderHealthHandlerReflectsLeadership(t *testing.T) {
	s := newLeaderReadOnlyTestStore(t)
	eng := engine.NewEngine(s)

	t.Run("leader returns 200", func(t *testing.T) {
		sched := engine.NewScheduler(eng, s, &fakeLeaderElector{leader: true})
		req := httptest.NewRequest(http.MethodGet, "/health/leader", nil)
		rec := httptest.NewRecorder()
		LeaderHealthHandler(sched)(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("expected 200 for leader, got %d: %s", rec.Code, rec.Body.String())
		}
	})

	t.Run("standby returns 503", func(t *testing.T) {
		sched := engine.NewScheduler(eng, s, &fakeLeaderElector{leader: false})
		req := httptest.NewRequest(http.MethodGet, "/health/leader", nil)
		rec := httptest.NewRecorder()
		LeaderHealthHandler(sched)(rec, req)

		if rec.Code != http.StatusServiceUnavailable {
			t.Fatalf("expected 503 for standby, got %d: %s", rec.Code, rec.Body.String())
		}
	})
}

// TestNewMinimalServerRoutes verifies the reduced route set NewMinimalServer
// wires for --mode scheduler / --mode worker (cmd/serve.go): /health and
// /metrics always, /health/leader only when a Scheduler is actually passed
// in (worker mode passes nil — workers never participate in leader
// election).
func TestNewMinimalServerRoutes(t *testing.T) {
	s := newLeaderReadOnlyTestStore(t)
	eng := engine.NewEngine(s)

	t.Run("with scheduler: health, metrics, and leader routes all present", func(t *testing.T) {
		sched := engine.NewScheduler(eng, s, &fakeLeaderElector{leader: true})
		srv := NewMinimalServer(0, s, eng, sched)

		for _, path := range []string{"/health", "/metrics", "/health/leader"} {
			req := httptest.NewRequest(http.MethodGet, path, nil)
			rec := httptest.NewRecorder()
			srv.router.ServeHTTP(rec, req)
			if rec.Code == http.StatusNotFound {
				t.Errorf("expected %s to be registered, got 404", path)
			}
		}

		req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
		rec := httptest.NewRecorder()
		srv.router.ServeHTTP(rec, req)
		if !strings.Contains(rec.Body.String(), "brokoli_leader_status 1\n") {
			t.Errorf("expected scheduler-mode /metrics to report leader status, got:\n%s", rec.Body.String())
		}
	})

	t.Run("without scheduler (worker mode): health and metrics present, leader route absent", func(t *testing.T) {
		srv := NewMinimalServer(0, s, eng, nil)

		for _, path := range []string{"/health", "/metrics"} {
			req := httptest.NewRequest(http.MethodGet, path, nil)
			rec := httptest.NewRecorder()
			srv.router.ServeHTTP(rec, req)
			if rec.Code == http.StatusNotFound {
				t.Errorf("expected %s to be registered, got 404", path)
			}
		}

		req := httptest.NewRequest(http.MethodGet, "/health/leader", nil)
		rec := httptest.NewRecorder()
		srv.router.ServeHTTP(rec, req)
		if rec.Code != http.StatusNotFound {
			t.Errorf("expected /health/leader to be absent in worker mode (nil sched), got %d", rec.Code)
		}

		req = httptest.NewRequest(http.MethodGet, "/metrics", nil)
		rec = httptest.NewRecorder()
		srv.router.ServeHTTP(rec, req)
		if strings.Contains(rec.Body.String(), "brokoli_leader_status") {
			t.Errorf("expected no leader metrics with nil sched, got:\n%s", rec.Body.String())
		}
	})

	t.Run("does not register UI, auth, or API routes", func(t *testing.T) {
		srv := NewMinimalServer(0, s, eng, nil)
		for _, path := range []string{"/", "/api/pipelines", "/api/auth/login"} {
			req := httptest.NewRequest(http.MethodGet, path, nil)
			rec := httptest.NewRecorder()
			srv.router.ServeHTTP(rec, req)
			if rec.Code != http.StatusNotFound {
				t.Errorf("expected %s to be absent from the minimal server, got %d", path, rec.Code)
			}
		}
	})
}
