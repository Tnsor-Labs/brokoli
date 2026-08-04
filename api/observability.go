package api

import (
	"fmt"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/Tnsor-Labs/brokoli/engine"
	"github.com/Tnsor-Labs/brokoli/store"
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
		_, err := s.ListPipelines()
		if err != nil {
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{
				"status": "unhealthy",
				"error":  err.Error(),
			})
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "healthy"})
	}
}

// LeaderHealthHandler returns a dedicated leader-aware readiness signal:
// HTTP 200 while this instance currently holds scheduler leadership, HTTP
// 503 while it's a standby. This is distinct from brokoli_leader_status in
// PrometheusHandler (a gauge meant for dashboards/alerting scraped
// periodically) — it exists so a Kubernetes readiness probe's `httpGet` can
// target a single well-known path and get a real leader-aware pass/fail
// straight from the HTTP status code, instead of having to exec into the
// container or grep /metrics (see Tnsor-Labs/brokoli-ee#9's HA deployment
// topology work, which this unblocks).
//
// sched is required (non-nil): callers must only register this route when a
// Scheduler actually exists — mirrors PrometheusHandler's sched==nil
// handling, except here there is no sensible "omit" behavior for a
// dedicated single-purpose endpoint, so registration itself is the guard
// (see api.NewMinimalServer, which only wires this route when sched != nil).
func LeaderHealthHandler(sched *engine.Scheduler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if sched.IsLeader() {
			writeJSON(w, http.StatusOK, map[string]string{"status": "leader"})
			return
		}
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"status": "standby"})
	}
}

// PrometheusHandler returns metrics in Prometheus text exposition format.
// sched may be nil (API-only/worker-only instances that don't run a
// scheduler component don't participate in leader election at all — see
// cmd/serve.go, which only constructs a Scheduler for "all"/"scheduler"
// mode); the leader-status metrics are simply omitted in that case.
func PrometheusHandler(m *Metrics, s store.Store, e *engine.Engine, sched *engine.Scheduler) http.HandlerFunc {
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
		fmt.Fprintf(w, "# HELP brokoli_uptime_seconds Time since server started.\n")
		fmt.Fprintf(w, "# TYPE brokoli_uptime_seconds gauge\n")
		fmt.Fprintf(w, "brokoli_uptime_seconds %.2f\n", uptime)
		fmt.Fprintf(w, "# HELP brokoli_http_requests_total Total HTTP requests.\n")
		fmt.Fprintf(w, "# TYPE brokoli_http_requests_total counter\n")
		fmt.Fprintf(w, "brokoli_http_requests_total %d\n", total)
		fmt.Fprintf(w, "# HELP brokoli_http_request_duration_avg_ms Average request duration in ms.\n")
		fmt.Fprintf(w, "# TYPE brokoli_http_request_duration_avg_ms gauge\n")
		fmt.Fprintf(w, "brokoli_http_request_duration_avg_ms %.2f\n", avgDuration)
		fmt.Fprintf(w, "# HELP brokoli_pipeline_runs_total Total pipeline runs triggered.\n")
		fmt.Fprintf(w, "# TYPE brokoli_pipeline_runs_total counter\n")
		fmt.Fprintf(w, "brokoli_pipeline_runs_total %d\n", e.RunsTotal)
		fmt.Fprintf(w, "# HELP brokoli_pipeline_runs_succeeded Total successful runs.\n")
		fmt.Fprintf(w, "# TYPE brokoli_pipeline_runs_succeeded counter\n")
		fmt.Fprintf(w, "brokoli_pipeline_runs_succeeded %d\n", e.RunsSucceeded)
		fmt.Fprintf(w, "# HELP brokoli_pipeline_runs_failed Total failed runs.\n")
		fmt.Fprintf(w, "# TYPE brokoli_pipeline_runs_failed counter\n")
		fmt.Fprintf(w, "brokoli_pipeline_runs_failed %d\n", e.RunsFailed)
		fmt.Fprintf(w, "# HELP brokoli_runs_recovered_total Non-terminal runs reconciled to a definite outcome by startup recovery.\n")
		fmt.Fprintf(w, "# TYPE brokoli_runs_recovered_total counter\n")
		fmt.Fprintf(w, "brokoli_runs_recovered_total %d\n", e.RunsRecovered)
		fmt.Fprintf(w, "# HELP brokoli_runs_recovery_failed_total Non-terminal runs startup recovery had no recoverable path for and forced to failed.\n")
		fmt.Fprintf(w, "# TYPE brokoli_runs_recovery_failed_total counter\n")
		fmt.Fprintf(w, "brokoli_runs_recovery_failed_total %d\n", e.RunsRecoveryFailed)
		fmt.Fprintf(w, "# HELP brokoli_execution_attempts_reclaimed_total Execution attempts whose expired lease was reclaimed by startup recovery.\n")
		fmt.Fprintf(w, "# TYPE brokoli_execution_attempts_reclaimed_total counter\n")
		fmt.Fprintf(w, "brokoli_execution_attempts_reclaimed_total %d\n", e.AttemptsReclaimed)

		// Event append/replay (Tnsor-Labs/brokoli#6) and node-attempt/
		// queue-depth (Tnsor-Labs/brokoli#7) metrics gaps, filling the same
		// pattern as the recovery/leader counters above — pulled directly
		// from Engine fields updated via atomic.AddInt64 exactly like
		// RunsTotal/RunsSucceeded/RunsFailed already are.
		fmt.Fprintf(w, "# HELP brokoli_run_events_appended_total Run/node-attempt lifecycle events successfully appended to the durable event log.\n")
		fmt.Fprintf(w, "# TYPE brokoli_run_events_appended_total counter\n")
		fmt.Fprintf(w, "brokoli_run_events_appended_total %d\n", e.EventsAppended)
		fmt.Fprintf(w, "# HELP brokoli_run_events_append_failed_total Run/node-attempt lifecycle event appends that failed (logged, not fatal to the run).\n")
		fmt.Fprintf(w, "# TYPE brokoli_run_events_append_failed_total counter\n")
		fmt.Fprintf(w, "brokoli_run_events_append_failed_total %d\n", e.EventsAppendFailed)
		fmt.Fprintf(w, "# HELP brokoli_run_events_replayed_total Individual run events consumed by event-log replay/projection (currently: startup recovery).\n")
		fmt.Fprintf(w, "# TYPE brokoli_run_events_replayed_total counter\n")
		fmt.Fprintf(w, "brokoli_run_events_replayed_total %d\n", e.EventsReplayed)
		fmt.Fprintf(w, "# HELP brokoli_node_attempts_started_total Node-level execution attempts started.\n")
		fmt.Fprintf(w, "# TYPE brokoli_node_attempts_started_total counter\n")
		fmt.Fprintf(w, "brokoli_node_attempts_started_total %d\n", e.AttemptsStarted)
		fmt.Fprintf(w, "# HELP brokoli_node_attempts_succeeded_total Node-level execution attempts that succeeded.\n")
		fmt.Fprintf(w, "# TYPE brokoli_node_attempts_succeeded_total counter\n")
		fmt.Fprintf(w, "brokoli_node_attempts_succeeded_total %d\n", e.AttemptsSucceeded)
		fmt.Fprintf(w, "# HELP brokoli_node_attempts_failed_total Node-level execution attempts that failed.\n")
		fmt.Fprintf(w, "# TYPE brokoli_node_attempts_failed_total counter\n")
		fmt.Fprintf(w, "brokoli_node_attempts_failed_total %d\n", e.AttemptsFailed)
		fmt.Fprintf(w, "# HELP brokoli_node_attempts_retried_total Node-level execution attempts that were retries (attempt > 0).\n")
		fmt.Fprintf(w, "# TYPE brokoli_node_attempts_retried_total counter\n")
		fmt.Fprintf(w, "brokoli_node_attempts_retried_total %d\n", e.AttemptsRetried)
		fmt.Fprintf(w, "# HELP brokoli_runs_queue_waiting Current number of run dispatches blocked waiting for a concurrency slot (dispatch backlog depth).\n")
		fmt.Fprintf(w, "# TYPE brokoli_runs_queue_waiting gauge\n")
		fmt.Fprintf(w, "brokoli_runs_queue_waiting %d\n", e.RunsQueueWaiting)

		fmt.Fprintf(w, "# HELP brokoli_active_runs Current number of active runs.\n")
		fmt.Fprintf(w, "# TYPE brokoli_active_runs gauge\n")
		fmt.Fprintf(w, "brokoli_active_runs %d\n", active)
		fmt.Fprintf(w, "# HELP brokoli_max_concurrent_runs Max concurrent runs allowed.\n")
		fmt.Fprintf(w, "# TYPE brokoli_max_concurrent_runs gauge\n")
		fmt.Fprintf(w, "brokoli_max_concurrent_runs %d\n", maxC)
		fmt.Fprintf(w, "# HELP brokoli_db_size_bytes SQLite database size in bytes.\n")
		fmt.Fprintf(w, "# TYPE brokoli_db_size_bytes gauge\n")
		fmt.Fprintf(w, "brokoli_db_size_bytes %d\n", dbSize)

		// Scheduler leadership (Tnsor-Labs/brokoli#10) — omitted entirely
		// for instances with no scheduler component (sched == nil), since
		// they never participate in leader election. Values are pulled
		// live from the Scheduler/LeaderElector at scrape time, the same
		// way brokoli_active_runs above is pulled from e.GetQueueInfo()
		// rather than tracked as a separate atomic counter.
		if sched != nil {
			isLeader := 0
			if sched.IsLeader() {
				isLeader = 1
			}
			fmt.Fprintf(w, "# HELP brokoli_leader_status 1 if this instance currently holds scheduler leadership, 0 otherwise.\n")
			fmt.Fprintf(w, "# TYPE brokoli_leader_status gauge\n")
			fmt.Fprintf(w, "brokoli_leader_status %d\n", isLeader)
			fmt.Fprintf(w, "# HELP brokoli_leader_fencing_generation Fencing generation last observed while holding leadership.\n")
			fmt.Fprintf(w, "# TYPE brokoli_leader_fencing_generation gauge\n")
			fmt.Fprintf(w, "brokoli_leader_fencing_generation %d\n", sched.LeaderFencingGeneration())
			fmt.Fprintf(w, "# HELP brokoli_leader_acquisitions_total Total number of times this instance acquired scheduler leadership.\n")
			fmt.Fprintf(w, "# TYPE brokoli_leader_acquisitions_total counter\n")
			fmt.Fprintf(w, "brokoli_leader_acquisitions_total %d\n", sched.LeaderAcquisitions())
			fmt.Fprintf(w, "# HELP brokoli_leader_releases_total Total number of times this instance voluntarily released scheduler leadership.\n")
			fmt.Fprintf(w, "# TYPE brokoli_leader_releases_total counter\n")
			fmt.Fprintf(w, "brokoli_leader_releases_total %d\n", sched.LeaderReleases())
			fmt.Fprintf(w, "# HELP brokoli_leader_election_failures_total Total number of leader election attempts that failed with a coordination-backend error (not counting normal loss-to-another-instance).\n")
			fmt.Fprintf(w, "# TYPE brokoli_leader_election_failures_total counter\n")
			fmt.Fprintf(w, "brokoli_leader_election_failures_total %d\n", sched.LeaderElectionFailures())
		}
	}
}
