package api

import (
	"fmt"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/hc12r/broked/engine"
	"github.com/hc12r/broked/store"
)

// Metrics tracks request and run metrics for Prometheus exposition.
type Metrics struct {
	RequestsTotal   atomic.Int64
	RequestDuration atomic.Int64 // total nanoseconds
	RunsTotal       atomic.Int64
	RunsSucceeded   atomic.Int64
	RunsFailed      atomic.Int64
	startTime       time.Time
}

// NewMetrics creates a new metrics collector.
func NewMetrics() *Metrics {
	return &Metrics{startTime: time.Now()}
}

// MetricsMiddleware records request counts and durations.
func MetricsMiddleware(m *Metrics) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			next.ServeHTTP(w, r)
			m.RequestsTotal.Add(1)
			m.RequestDuration.Add(int64(time.Since(start)))
		})
	}
}

// HealthHandler returns 200 if the server is alive and DB is reachable.
func HealthHandler(s store.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// Quick DB check
		_, err := s.ListPipelines()
		if err != nil {
			w.WriteHeader(http.StatusServiceUnavailable)
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{
				"status": "unhealthy",
				"error":  err.Error(),
			})
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "healthy"})
	}
}

// PrometheusHandler returns metrics in Prometheus text exposition format.
func PrometheusHandler(m *Metrics, s store.Store, e *engine.Engine) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		active, maxC := e.GetQueueInfo()
		uptime := time.Since(m.startTime).Seconds()
		avgDuration := float64(0)
		total := m.RequestsTotal.Load()
		if total > 0 {
			avgDuration = float64(m.RequestDuration.Load()) / float64(total) / 1e6 // ms
		}
		dbSize, _ := s.GetDBSize()

		w.Header().Set("Content-Type", "text/plain; version=0.0.4")
		fmt.Fprintf(w, "# HELP broked_uptime_seconds Time since server started.\n")
		fmt.Fprintf(w, "# TYPE broked_uptime_seconds gauge\n")
		fmt.Fprintf(w, "broked_uptime_seconds %.2f\n", uptime)
		fmt.Fprintf(w, "# HELP broked_http_requests_total Total HTTP requests.\n")
		fmt.Fprintf(w, "# TYPE broked_http_requests_total counter\n")
		fmt.Fprintf(w, "broked_http_requests_total %d\n", total)
		fmt.Fprintf(w, "# HELP broked_http_request_duration_avg_ms Average request duration in ms.\n")
		fmt.Fprintf(w, "# TYPE broked_http_request_duration_avg_ms gauge\n")
		fmt.Fprintf(w, "broked_http_request_duration_avg_ms %.2f\n", avgDuration)
		fmt.Fprintf(w, "# HELP broked_pipeline_runs_total Total pipeline runs triggered.\n")
		fmt.Fprintf(w, "# TYPE broked_pipeline_runs_total counter\n")
		fmt.Fprintf(w, "broked_pipeline_runs_total %d\n", e.RunsTotal)
		fmt.Fprintf(w, "# HELP broked_pipeline_runs_succeeded Total successful runs.\n")
		fmt.Fprintf(w, "# TYPE broked_pipeline_runs_succeeded counter\n")
		fmt.Fprintf(w, "broked_pipeline_runs_succeeded %d\n", e.RunsSucceeded)
		fmt.Fprintf(w, "# HELP broked_pipeline_runs_failed Total failed runs.\n")
		fmt.Fprintf(w, "# TYPE broked_pipeline_runs_failed counter\n")
		fmt.Fprintf(w, "broked_pipeline_runs_failed %d\n", e.RunsFailed)
		fmt.Fprintf(w, "# HELP broked_active_runs Current number of active runs.\n")
		fmt.Fprintf(w, "# TYPE broked_active_runs gauge\n")
		fmt.Fprintf(w, "broked_active_runs %d\n", active)
		fmt.Fprintf(w, "# HELP broked_max_concurrent_runs Max concurrent runs allowed.\n")
		fmt.Fprintf(w, "# TYPE broked_max_concurrent_runs gauge\n")
		fmt.Fprintf(w, "broked_max_concurrent_runs %d\n", maxC)
		fmt.Fprintf(w, "# HELP broked_db_size_bytes SQLite database size in bytes.\n")
		fmt.Fprintf(w, "# TYPE broked_db_size_bytes gauge\n")
		fmt.Fprintf(w, "broked_db_size_bytes %d\n", dbSize)
	}
}
