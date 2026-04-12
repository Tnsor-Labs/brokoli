package sodp

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

const (
	// maxLogEntries caps the number of log entries stored per run in state.
	maxLogEntries = 200
	// maxRecentRuns caps the recent_runs list inside dashboard.{org}.
	maxRecentRuns = 12
	// maxTopFailing caps the top_failing list inside dashboard.{org}.
	maxTopFailing = 5
)

// BridgeEvent mirrors models.Event without importing the models package,
// keeping pkg/sodp dependency-free from the rest of the app.
type BridgeEvent struct {
	Type       string
	RunID      string
	PipelineID string
	OrgID      string
	NodeID     string
	Status     string
	RowCount   int
	DurationMs int64
	Error      string
	Level      string
	Message    string
	Timestamp  time.Time
}

// Bridge converts streaming events from a pipeline engine into SODP state
// mutations. It maintains the following state-key namespace:
//
//	runs.{run_id}                → run-level state (status, pipeline_id, started_at, ...)
//	runs.{run_id}.nodes.{node}   → per-node state
//	runs.{run_id}.logs           → bounded log buffer (append-only via MutateAppend)
//	dashboard.{org}              → aggregated dashboard snapshot the UI watches
//
// The dashboard.{org} key is the per-org snapshot the Dashboard, RunIndicator,
// and similar pages subscribe to. It's recomputed on every terminal event
// (run/node started/completed/failed) by scanning the runs.* state and
// rolling up the aggregates. The recompute is O(active runs) which is fine
// for OSS workloads — typical state stores hold thousands, not millions.
//
// We do NOT recompute on log events: they're high-frequency and don't change
// any aggregate counter. The Dashboard watches dashboard.{org}, the per-run
// detail page watches runs.{run_id}.logs directly.
func Bridge(srv *Server, events <-chan BridgeEvent) {
	go func() {
		for ev := range events {
			bridgeEvent(srv, ev)
		}
	}()
}

func bridgeEvent(srv *Server, ev BridgeEvent) {
	runKey := "runs." + ev.RunID

	switch ev.Type {
	case "run.started":
		srv.Mutate(runKey, map[string]any{
			"status":      "running",
			"pipeline_id": ev.PipelineID,
			"org_id":      ev.OrgID,
			"started_at":  ev.Timestamp.Format(time.RFC3339),
		})

	case "run.completed":
		current, _ := srv.State.Get(runKey)
		merged := mergeValues(current, map[string]any{
			"status":      "success",
			"finished_at": ev.Timestamp.Format(time.RFC3339),
		})
		srv.Mutate(runKey, merged)

	case "run.failed":
		current, _ := srv.State.Get(runKey)
		status := ev.Status
		if status == "" {
			status = "failed"
		}
		merged := mergeValues(current, map[string]any{
			"status":      status,
			"error":       ev.Error,
			"finished_at": ev.Timestamp.Format(time.RFC3339),
		})
		srv.Mutate(runKey, merged)

	case "node.started":
		nodeKey := fmt.Sprintf("%s.nodes.%s", runKey, ev.NodeID)
		state := map[string]any{
			"status":     "running",
			"started_at": ev.Timestamp.Format(time.RFC3339),
		}
		if ev.Status == "retrying" {
			state["status"] = "retrying"
			state["retry_info"] = ev.Error
		}
		srv.Mutate(nodeKey, state)

	case "node.completed":
		nodeKey := fmt.Sprintf("%s.nodes.%s", runKey, ev.NodeID)
		current, _ := srv.State.Get(nodeKey)
		merged := mergeValues(current, map[string]any{
			"status":      "completed",
			"row_count":   ev.RowCount,
			"duration_ms": ev.DurationMs,
			"finished_at": ev.Timestamp.Format(time.RFC3339),
		})
		srv.Mutate(nodeKey, merged)

	case "node.failed":
		nodeKey := fmt.Sprintf("%s.nodes.%s", runKey, ev.NodeID)
		current, _ := srv.State.Get(nodeKey)
		merged := mergeValues(current, map[string]any{
			"status":      "failed",
			"error":       ev.Error,
			"finished_at": ev.Timestamp.Format(time.RFC3339),
		})
		srv.Mutate(nodeKey, merged)

	case "log":
		// Log events go straight to the bounded log buffer for that run.
		// They don't affect any dashboard aggregate, so we skip the
		// dashboard recompute below to keep log throughput cheap.
		logKey := runKey + ".logs"
		entry := map[string]any{
			"node_id":   ev.NodeID,
			"level":     ev.Level,
			"message":   ev.Message,
			"timestamp": ev.Timestamp.Format(time.RFC3339),
		}
		srv.MutateAppend(logKey, entry, maxLogEntries)
		return // skip dashboard recompute
	}

	// Recompute the dashboard snapshot for the org this event belongs to.
	// We pass the latest event timestamp as a hint for the "today" / "24h"
	// windows so the snapshot uses the engine's clock, not wall-clock — this
	// matters in tests that fix the time.
	recomputeDashboard(srv, ev.OrgID, ev.Timestamp)
}

// dashboardKey returns the per-org dashboard state key. The community / single-
// tenant case uses "dashboard.default".
func dashboardKey(orgID string) string {
	if orgID == "" || orgID == "default" {
		return "dashboard.default"
	}
	return "dashboard." + orgID
}

// recomputeDashboard scans the runs.* namespace and writes a fresh aggregate
// snapshot to dashboard.{org}. This is the source of truth the UI watches.
//
// We compute everything from the current state store rather than maintaining
// running counters because:
//   1. Snapshots are always correct — no drift from missed events.
//   2. Cost is O(active runs) which is bounded by maxKeys.
//   3. The state store's diff machinery (StateStore.Apply) sends only the
//      changed fields on the wire, so a snapshot rewrite is cheap to broadcast.
func recomputeDashboard(srv *Server, orgID string, asOf time.Time) {
	if asOf.IsZero() {
		asOf = time.Now().UTC()
	}
	todayStr := asOf.Format("2006-01-02")
	yesterdayStr := asOf.AddDate(0, 0, -1).Format("2006-01-02")
	last24hCutoff := asOf.Add(-24 * time.Hour)

	type runRow struct {
		RunID      string
		PipelineID string
		Status     string
		StartedAt  string
		FinishedAt string
		Error      string
	}

	// Snapshot all runs.* keys (the snapshot is read-locked, no contention).
	all := srv.State.Snapshot("runs")
	rows := make([]runRow, 0, len(all))
	for k, entry := range all {
		// Only top-level run keys: "runs.{id}" — skip "runs.{id}.nodes.{n}" etc.
		// A top-level run has exactly one dot and no further segments.
		if !strings.HasPrefix(k, "runs.") {
			continue
		}
		rest := k[len("runs."):]
		if strings.Contains(rest, ".") {
			continue
		}
		m, ok := entry.Value.(map[string]any)
		if !ok {
			continue
		}
		// Per-org filter. Runs without an org_id stamp are visible only to
		// the default snapshot (community mode + legacy data).
		runOrg, _ := m["org_id"].(string)
		if orgID == "" || orgID == "default" {
			if runOrg != "" && runOrg != "default" {
				continue
			}
		} else if runOrg != orgID {
			continue
		}
		row := runRow{RunID: rest}
		row.PipelineID, _ = m["pipeline_id"].(string)
		row.Status, _ = m["status"].(string)
		row.StartedAt, _ = m["started_at"].(string)
		row.FinishedAt, _ = m["finished_at"].(string)
		row.Error, _ = m["error"].(string)
		rows = append(rows, row)
	}

	// Sort newest-first by started_at so the recent_runs slice is the head.
	sort.Slice(rows, func(i, j int) bool { return rows[i].StartedAt > rows[j].StartedAt })

	// Roll up aggregates.
	var runsToday, runsYesterday int
	var runs24hTotal, runs24hSuccess, runs24hFailed int
	var runsRunning int
	runningIDs := make([]string, 0, 4)
	failCounts := make(map[string]int)
	failPipelineIDs := make([]string, 0)

	// 7-day trend buckets (most recent 7 days inclusive of today).
	// We store these as map[string]any rather than a Go struct so the
	// msgpack-encoded keys are exactly the lowercase field names the JS UI
	// reads. A struct here would need explicit `msgpack:` tags — without them
	// vmihailenco/msgpack uses the Go field name (capitalized), and the
	// JS-side `day.date` would be undefined.
	type trendBucket = map[string]any
	trendByDay := make(map[string]trendBucket, 7)
	trendOrder := make([]string, 0, 7)
	for i := 6; i >= 0; i-- {
		d := asOf.AddDate(0, 0, -i).Format("2006-01-02")
		trendByDay[d] = trendBucket{
			"date":    d,
			"success": 0,
			"failed":  0,
			"total":   0,
		}
		trendOrder = append(trendOrder, d)
	}

	for _, r := range rows {
		if len(r.StartedAt) >= 10 {
			day := r.StartedAt[:10]
			if day == todayStr {
				runsToday++
			} else if day == yesterdayStr {
				runsYesterday++
			}
		}
		if r.StartedAt != "" {
			if t, err := time.Parse(time.RFC3339, r.StartedAt); err == nil && !t.Before(last24hCutoff) {
				runs24hTotal++
				switch r.Status {
				case "success", "completed":
					runs24hSuccess++
				case "failed":
					runs24hFailed++
				}
			}
		}
		if r.Status == "running" {
			runsRunning++
			runningIDs = append(runningIDs, r.RunID)
		}
		if r.Status == "failed" && r.PipelineID != "" {
			if _, seen := failCounts[r.PipelineID]; !seen {
				failPipelineIDs = append(failPipelineIDs, r.PipelineID)
			}
			failCounts[r.PipelineID]++
		}
		// Bucket by day for the 7-day trend chart
		if len(r.StartedAt) >= 10 {
			day := r.StartedAt[:10]
			if t, ok := trendByDay[day]; ok {
				t["total"] = t["total"].(int) + 1
				switch r.Status {
				case "success", "completed":
					t["success"] = t["success"].(int) + 1
				case "failed":
					t["failed"] = t["failed"].(int) + 1
				}
			}
		}
	}

	// Materialize the trend slice in calendar order
	trends := make([]map[string]any, 0, len(trendOrder))
	for _, d := range trendOrder {
		trends = append(trends, trendByDay[d])
	}

	successRate24h := 100
	if runs24hTotal > 0 {
		successRate24h = int((float64(runs24hSuccess) / float64(runs24hTotal)) * 100)
	}

	// Top failing pipelines, descending by fail count.
	sort.Slice(failPipelineIDs, func(i, j int) bool {
		return failCounts[failPipelineIDs[i]] > failCounts[failPipelineIDs[j]]
	})
	if len(failPipelineIDs) > maxTopFailing {
		failPipelineIDs = failPipelineIDs[:maxTopFailing]
	}
	topFailing := make([]map[string]any, 0, len(failPipelineIDs))
	for _, pid := range failPipelineIDs {
		topFailing = append(topFailing, map[string]any{
			"pipeline_id": pid,
			"fail_count":  failCounts[pid],
		})
	}

	// Recent runs slice (head of newest-first list, no error bodies — those
	// live on runs.{id} for the per-run detail page to fetch).
	limit := maxRecentRuns
	if len(rows) < limit {
		limit = len(rows)
	}
	recent := make([]map[string]any, 0, limit)
	for _, r := range rows[:limit] {
		recent = append(recent, map[string]any{
			"run_id":      r.RunID,
			"pipeline_id": r.PipelineID,
			"status":      r.Status,
			"started_at":  r.StartedAt,
			"finished_at": r.FinishedAt,
		})
	}

	snapshot := map[string]any{
		"updated_at":       asOf.Format(time.RFC3339),
		"runs_today":       runsToday,
		"runs_yesterday":   runsYesterday,
		"runs_running":     runsRunning,
		"running_run_ids":  runningIDs,
		"runs_24h_total":   runs24hTotal,
		"runs_24h_success": runs24hSuccess,
		"runs_24h_failed":  runs24hFailed,
		"success_rate_24h": successRate24h,
		"recent_runs":      recent,
		"top_failing":      topFailing,
		"trends":           trends,
	}
	srv.Mutate(dashboardKey(orgID), snapshot)
}
