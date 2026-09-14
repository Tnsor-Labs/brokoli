package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Tnsor-Labs/brokoli/engine"
	"github.com/Tnsor-Labs/brokoli/extensions"
	"github.com/Tnsor-Labs/brokoli/models"
	"github.com/Tnsor-Labs/brokoli/pkg/netguard"
	"github.com/Tnsor-Labs/brokoli/store"
	"github.com/go-chi/chi/v5"
)

// dlqListHandler handles GET /pipelines/{id}/dlq — returns dead letter queue entries.
func dlqListHandler(s store.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := chi.URLParam(r, "id")
		if p, err := s.GetPipeline(id); err == nil {
			if !ValidateOrgAccess(r, p.OrgID) {
				DenyOrgAccess(w)
				return
			}
		} else {
			writeError(w, http.StatusNotFound, "pipeline not found")
			return
		}
		includeResolved := r.URL.Query().Get("include_resolved") == "true"
		limit := 50
		if l := r.URL.Query().Get("limit"); l != "" {
			if n, err := strconv.Atoi(l); err == nil && n > 0 && n <= 500 {
				limit = n
			}
		}
		entries, err := s.ListDLQ(id, includeResolved, limit)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		if entries == nil {
			entries = []store.DLQEntry{}
		}
		writeJSON(w, http.StatusOK, entries)
	}
}

// dlqResolveHandler handles POST /pipelines/{id}/dlq/{dlqId}/resolve — marks a DLQ entry as resolved.
func dlqResolveHandler(s store.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		pipelineID := chi.URLParam(r, "id")
		if p, err := s.GetPipeline(pipelineID); err == nil {
			if !ValidateOrgAccess(r, p.OrgID) {
				DenyOrgAccess(w)
				return
			}
		} else {
			writeError(w, http.StatusNotFound, "pipeline not found")
			return
		}
		dlqID := chi.URLParam(r, "dlqId")
		if err := s.ResolveDLQ(dlqID); err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "resolved"})
	}
}

var webhookLimiter = struct {
	sync.Mutex
	last map[string]time.Time
}{last: make(map[string]time.Time)}

// webhookMinInterval is the shortest gap allowed between two triggers of
// the same pipeline's webhook.
const webhookMinInterval = 10 * time.Second

// claimWebhookSlot reports whether this pipeline may trigger now, and
// records the attempt when it may.
//
// Only ever called for a caller that has already proved it holds the
// pipeline's webhook token. That ordering is the point. The check used
// to run first, on the raw {id} from the URL and before any of the
// checks below it, which meant an unauthenticated caller could hold a
// real pipeline's slot indefinitely by sending one request every ten
// seconds: the legitimate sender then received 429 forever while the
// attacker's own 401s cost it nothing. It also meant the map took an
// entry for any {id} anyone sent, including ids matching no pipeline,
// and nothing ever removed one.
//
// Expired entries are dropped on each successful claim. That sweep is
// O(entries), but it runs at most once per pipeline per interval and
// the map now holds only real, authenticated pipelines, so it stays
// proportional to recently active webhooks rather than to total
// requests ever received.
func claimWebhookSlot(pipelineID string, now time.Time) bool {
	webhookLimiter.Lock()
	defer webhookLimiter.Unlock()

	if last, ok := webhookLimiter.last[pipelineID]; ok && now.Sub(last) < webhookMinInterval {
		return false
	}
	for id, at := range webhookLimiter.last {
		if now.Sub(at) >= webhookMinInterval {
			delete(webhookLimiter.last, id)
		}
	}
	webhookLimiter.last[pipelineID] = now
	return true
}

// webhookTriggerHandler handles POST /pipelines/{id}/webhook — triggers a pipeline run via webhook token.
func webhookTriggerHandler(s store.Store, e *engine.Engine) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := chi.URLParam(r, "id")

		token := r.URL.Query().Get("token")
		if token == "" {
			token = r.Header.Get("X-Webhook-Token")
		}
		p, err := s.GetPipeline(id)
		if err != nil {
			// Same 404 body as the other pre-auth failures so callers cannot
			// tell missing pipelines apart from unconfigured or bad-token ones.
			log.Printf("webhook %q: pipeline not found", id)
			DenyOrgAccess(w)
			return
		}
		if p.WebhookToken == "" {
			log.Printf("webhook %q: webhook not configured", id)
			DenyOrgAccess(w)
			return
		}
		if !engine.ValidateWebhookToken(token, p.WebhookToken) {
			log.Printf("webhook %q: invalid webhook token", id)
			DenyOrgAccess(w)
			return
		}

		// Rate limit: max 1 webhook trigger per pipeline per 10 seconds.
		// Claimed only now that the caller has proved it may trigger this
		// pipeline at all (#534).
		if !claimWebhookSlot(p.ID, time.Now()) {
			writeError(w, http.StatusTooManyRequests, "webhook rate limit exceeded, try again in 10 seconds")
			return
		}
		run, err := e.RunPipeline(p.ID)
		if err != nil {
			// Same as the trigger route: a draft is a state, not a fault.
			if errors.Is(err, engine.ErrPipelineIsDraft) {
				writeError(w, http.StatusConflict, err.Error())
				return
			}
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"run_id": run.ID,
			"status": run.Status,
		})
	}
}

// pipelineDepsHandler handles GET /pipelines/{id}/deps — returns dependency status for a pipeline.
func pipelineDepsHandler(s store.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := chi.URLParam(r, "id")
		p, err := s.GetPipeline(id)
		if err != nil {
			writeError(w, http.StatusNotFound, "pipeline not found")
			return
		}
		if !ValidateOrgAccess(r, p.OrgID) {
			DenyOrgAccess(w)
			return
		}
		satisfied, statuses, reason := engine.CheckDependencies(s, p, time.Now().UTC())
		enriched := make([]map[string]interface{}, 0, len(statuses))
		for _, st := range statuses {
			entry := map[string]interface{}{
				"pipeline_id": st.Rule.PipelineID,
				"state":       st.Rule.State,
				"mode":        st.Rule.Mode,
				"satisfied":   st.Satisfied,
				"reason":      st.Reason,
				"missing":     st.Missing,
			}
			if st.UpstreamName != "" {
				entry["name"] = st.UpstreamName
			}
			if st.LastStatus != "" {
				entry["last_status"] = st.LastStatus
			}
			if st.LastRunAt != nil {
				entry["last_run_at"] = st.LastRunAt.Format(time.RFC3339)
			}
			enriched = append(enriched, entry)
		}
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"satisfied": satisfied,
			"reason":    reason,
			"deps":      enriched,
		})
	}
}

// pipelineDependentsHandler handles GET /pipelines/{id}/dependents — lists pipelines that depend on this one.
// Scoped to the caller's org via the lightweight adjacency query (no nodes/edges blob load).
func pipelineDependentsHandler(s store.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := chi.URLParam(r, "id")
		p, err := s.GetPipeline(id)
		if err != nil {
			writeError(w, http.StatusNotFound, "pipeline not found")
			return
		}
		if !ValidateOrgAccess(r, p.OrgID) {
			DenyOrgAccess(w)
			return
		}
		summaries, err := s.ListPipelineDepsByOrg(p.OrgID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		out := make([]map[string]interface{}, 0)
		for _, sum := range summaries {
			for _, rule := range sum.EffectiveDependencies() {
				if rule.PipelineID != id {
					continue
				}
				out = append(out, map[string]interface{}{
					"id":   sum.ID,
					"name": sum.Name,
				})
				break
			}
		}
		writeJSON(w, http.StatusOK, out)
	}
}

// maxGraphNodes caps the dependency-graph payload so a single slow client can't force the
// server to serialize tens of thousands of pipelines into one JSON response.
const maxGraphNodes = 2000

// pipelineDependencyGraphHandler handles GET /pipelines/dependency-graph — returns the
// caller's org dep graph, capped at maxGraphNodes to bound response size.
func pipelineDependencyGraphHandler(s store.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		orgID := GetOrgIDFromRequest(r)
		summaries, err := s.ListPipelineDepsByOrg(orgID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		truncated := false
		if len(summaries) > maxGraphNodes {
			summaries = summaries[:maxGraphNodes]
			truncated = true
		}
		nodes := make([]map[string]interface{}, 0, len(summaries))
		edges := make([]map[string]interface{}, 0)
		inGraph := make(map[string]bool, len(summaries))
		for _, sum := range summaries {
			inGraph[sum.ID] = true
			nodes = append(nodes, map[string]interface{}{
				"id":   sum.ID,
				"name": sum.Name,
			})
		}
		for _, sum := range summaries {
			for _, rule := range sum.EffectiveDependencies() {
				// Only draw edges to nodes that are in the graph — drops dangling references
				// so the client never has to handle edges pointing at nothing.
				if !inGraph[rule.PipelineID] {
					continue
				}
				edges = append(edges, map[string]interface{}{
					"from":  rule.PipelineID,
					"to":    sum.ID,
					"state": rule.State,
					"mode":  rule.Mode,
				})
			}
		}
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"nodes":     nodes,
			"edges":     edges,
			"truncated": truncated,
		})
	}
}

// defaultCalendarDays is the window when the caller does not ask for one.
const defaultCalendarDays = 90

// calendarHandler handles GET /runs/calendar — returns run calendar data.
//
// Days are UTC calendar days. That is a deliberate choice rather than an
// accident of the SQL: it is stable for every viewer of a shared install,
// whereas the dashboard's local-day buckets follow the server's zone. A
// client rendering a grid must key it in UTC to line up (#611).
func calendarHandler(s store.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		days := defaultCalendarDays
		if d := r.URL.Query().Get("days"); d != "" {
			// Parsed strictly. Sscanf ignored its error and ignored
			// trailing characters, so "abc" silently became the default
			// and "30junk" silently became 30, and an out-of-range value
			// was silently replaced by 90. A client could not tell any of
			// those from a request the server honoured, so it could not
			// know which window it was drawing.
			n, err := strconv.Atoi(strings.TrimSpace(d))
			if err != nil || n < store.MinCalendarDays || n > store.MaxCalendarDays {
				writeError(w, http.StatusBadRequest, fmt.Sprintf(
					"days must be a whole number between %d and %d",
					store.MinCalendarDays, store.MaxCalendarDays))
				return
			}
			days = n
		}

		orgID := GetOrgIDFromRequest(r)
		cal, err := s.GetRunCalendarByOrg(days, orgID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}

		// Fill every day in the window. The query returns only days that
		// have runs, so a quiet period was absent rather than zero and the
		// response carried no indication of the range it covered. A client
		// had to synthesise the missing days from the request it sent, and
		// could not check the two agreed.
		counts := make(map[string]store.CalendarDay, len(cal))
		for _, d := range cal {
			counts[d.Date] = d
		}
		filled := make([]store.CalendarDay, 0, days)
		for _, date := range store.CalendarWindowDates(days) {
			if d, ok := counts[date]; ok {
				filled = append(filled, d)
				continue
			}
			filled = append(filled, store.CalendarDay{Date: date})
		}
		writeJSON(w, http.StatusOK, filled)
	}
}

// listPipelinesForRequest returns pipelines scoped to the user's org or workspace.
func listPipelinesForRequest(s store.Store, r *http.Request) ([]models.Pipeline, error) {
	orgID := GetOrgIDFromRequest(r)
	if orgID != "" {
		return s.ListPipelinesByOrg(orgID)
	}
	// In multi-tenant mode (OrgResolverFunc set), users without an org see nothing
	if OrgResolverFunc != nil {
		return []models.Pipeline{}, nil
	}
	// Community/self-hosted mode: fall back to workspace
	return s.ListPipelinesByWorkspace(GetWorkspaceID(r))
}

// dashboardRunsPerPipeline is how many runs the dashboard reads per
// pipeline. It bounds memory, and it also bounds truth: a pipeline that
// runs more often than this in the reported window is under-counted in
// every aggregate below. The response carries the number so a client can
// say so. Removing the cap means aggregating in SQL (#608).
const dashboardRunsPerPipeline = 200

// dashboardHandler handles GET /dashboard — returns aggregated dashboard data.
//
// The aggregate counts (runs_today, runs_yesterday, runs_running, etc.) are
// computed server-side from a bounded per-pipeline window of recent runs
// (matching what pipelineSummaryHandler already does). The frontend should
// display these directly rather than re-deriving stats from `recent_runs`,
// which is intentionally a small UI sample, not the source of truth for any
// counter.
func dashboardHandler(s store.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		orgID := GetOrgIDFromRequest(r)
		var pipelines []models.Pipeline
		if orgID != "" {
			pipelines, _ = s.ListPipelinesByOrg(orgID)
		} else if OrgResolverFunc != nil {
			pipelines = []models.Pipeline{}
		} else {
			wsID := GetWorkspaceID(r)
			pipelines, _ = s.ListPipelinesByWorkspace(wsID)
		}
		type runEntry struct {
			PipelineID   string `json:"pipeline_id"`
			PipelineName string `json:"pipeline_name"`
			RunID        string `json:"run_id"`
			Status       string `json:"status"`
			Error        string `json:"error,omitempty"`
			StartedAt    string `json:"started_at,omitempty"`
			FinishedAt   string `json:"finished_at,omitempty"`
			// DurationMs is measured from the stored timestamps at full
			// precision. Subtracting the two fields above used to be the
			// only way for a client to get it, and they were emitted at
			// whole-second resolution, so every run shorter than a second
			// read as zero (#607). A run has no duration of its own on the
			// model; only NodeRun carries one.
			DurationMs int64 `json:"duration_ms,omitempty"`

			// startedAtTime is the parsed form, kept so the bucketing below
			// converts once instead of re-parsing the string in four places
			// and slicing it in a fifth.
			startedAtTime time.Time
			hasStartedAt  bool
		}

		// Load a wider per-pipeline window so the aggregates below reflect
		// reality, not the last few entries. We load all of them into one
		// flat list (`allRuns`), then take the head as the small UI sample
		// (`recentRuns`). 200 per pipeline matches pipelineSummaryHandler.
		//
		// This is still a cap, and a pipeline that runs more than 200 times
		// in the window under-reports every count below. Tracked in #608,
		// whose fix is to aggregate in SQL rather than to raise the number.
		var allRuns []runEntry
		for _, p := range pipelines {
			runs, _ := s.ListRunsByPipeline(p.ID, dashboardRunsPerPipeline)
			for _, run := range runs {
				run.PopulateError()
				entry := runEntry{
					PipelineID:   p.ID,
					PipelineName: p.Name,
					RunID:        run.ID,
					Status:       string(run.Status),
					Error:        run.Error,
				}
				if run.StartedAt != nil && run.FinishedAt != nil {
					if d := run.FinishedAt.Sub(*run.StartedAt); d > 0 {
						entry.DurationMs = d.Milliseconds()
					}
				}
				// RFC3339 with fractional seconds. The previous layout had
				// no fractional part, so a sub-second run arrived with
				// identical start and finish times (#607).
				if run.StartedAt != nil {
					entry.StartedAt = run.StartedAt.Format(time.RFC3339Nano)
					entry.startedAtTime = *run.StartedAt
					entry.hasStartedAt = true
				}
				if run.FinishedAt != nil {
					entry.FinishedAt = run.FinishedAt.Format(time.RFC3339Nano)
				}
				allRuns = append(allRuns, entry)
			}
		}
		// Newest first. Compared as instants rather than as strings: at
		// whole-second resolution runs that started in the same second
		// compared equal, so their order came down to which pipeline was
		// read first, and "the pipeline's most recent run" below could name
		// a run that was not the latest. The run id breaks a genuine tie so
		// the order is at least stable between requests.
		sort.SliceStable(allRuns, func(i, j int) bool {
			a, b := allRuns[i], allRuns[j]
			if a.hasStartedAt != b.hasStartedAt {
				return a.hasStartedAt
			}
			if !a.startedAtTime.Equal(b.startedAtTime) {
				return a.startedAtTime.After(b.startedAtTime)
			}
			return a.RunID > b.RunID
		})

		// Compute the real aggregates from the full window, not from the
		// 50-entry recentRuns slice that follows.
		now := time.Now()
		todayStr := now.Format("2006-01-02")
		yesterdayStr := now.AddDate(0, 0, -1).Format("2006-01-02")
		last24hCutoff := now.Add(-24 * time.Hour)

		var runsToday, runsYesterday int
		var runs24hTotal, runs24hSuccess, runs24hFailed int
		var runsRunning int
		// Authoritative list of currently-running run IDs. The frontend uses
		// this to reconcile its client-side liveRunStatuses store: any "running"
		// entry whose ID is NOT in this list is stale (probably from a missed
		// run.completed event during a reconnect window) and should be cleared.
		runningRunIDs := make([]string, 0, 4)
		for _, run := range allRuns {
			if run.hasStartedAt {
				t := run.startedAtTime
				// Timestamps are persisted in UTC; "today" means the
				// server's local day. Convert before bucketing — slicing
				// the UTC string and comparing it against a local date
				// misbucketed every run for the first hours of each
				// local day on any server not running in UTC.
				day := t.Local().Format("2006-01-02")
				if day == todayStr {
					runsToday++
				} else if day == yesterdayStr {
					runsYesterday++
				}
				if !t.Before(last24hCutoff) {
					runs24hTotal++
					switch run.Status {
					case string(models.RunStatusSuccess):
						runs24hSuccess++
					case string(models.RunStatusFailed):
						runs24hFailed++
					}
				}
			}
			if run.Status == string(models.RunStatusRunning) {
				runsRunning++
				runningRunIDs = append(runningRunIDs, run.RunID)
			}
		}

		// Success rate over runs that have finished, not over every run in
		// the window (#606). runs24hTotal includes pending, running,
		// waiting, blocked and cancelled runs, none of which has succeeded
		// or failed yet, so dividing by it meant a pipeline's rate fell
		// while its runs were still in flight and a cancelled run counted
		// as if it were a failure.
		//
		// Null, not 100, when nothing finished. There is no success rate
		// over zero runs, and 100 is the value most likely to be read as
		// "everything is fine" on a fresh install or a quiet weekend.
		runs24hFinished := runs24hSuccess + runs24hFailed
		var successRate24h *int
		if runs24hFinished > 0 {
			rate := int((float64(runs24hSuccess) / float64(runs24hFinished)) * 100)
			successRate24h = &rate
		}

		// Build the small UI sample (recent_runs) from the head of the
		// already-sorted list. Kept around for the "Recent activity" list
		// only — never used as a source of truth for any counter.
		recentRuns := allRuns
		if len(recentRuns) > 50 {
			recentRuns = recentRuns[:50]
		}
		if recentRuns == nil {
			recentRuns = []runEntry{}
		}

		summaries := make([]PipelineSummary, 0, len(pipelines))
		for _, p := range pipelines {
			summaries = append(summaries, toPipelineSummary(p))
		}

		// Compute daily trends (last 7 days) from the full window, not the
		// 50-entry recentRuns sample.
		type dayTrend struct {
			Date    string `json:"date"`
			Success int    `json:"success"`
			Failed  int    `json:"failed"`
			Total   int    `json:"total"`
		}
		trendMap := make(map[string]*dayTrend)
		for i := 6; i >= 0; i-- {
			d := now.AddDate(0, 0, -i).Format("2006-01-02")
			trendMap[d] = &dayTrend{Date: d}
		}
		// Top failing pipelines, over the same 24 hours as the aggregates
		// they sit beside. This used to count every failure in the loaded
		// window regardless of age, so a pipeline that failed forty times
		// last month outranked one that failed twice this morning, and the
		// effective period differed per pipeline because 200 runs is a day
		// for one schedule and a year for another (#610).
		failCounts := make(map[string]int)
		failNames := make(map[string]string)
		for _, r := range allRuns {
			if !r.hasStartedAt {
				continue
			}
			// Local, like every other day bucket here. The keys above are
			// local dates and this used to slice the UTC timestamp string,
			// so on a server east of UTC the first hours of each local day
			// landed on the previous day, and west of UTC late-evening runs
			// were keyed to a tomorrow that is not in the map and vanished
			// from the chart entirely (#609).
			day := r.startedAtTime.Local().Format("2006-01-02")
			if t, ok := trendMap[day]; ok {
				t.Total++
				switch r.Status {
				case string(models.RunStatusSuccess):
					t.Success++
				case string(models.RunStatusFailed):
					t.Failed++
				}
			}
			if r.Status == string(models.RunStatusFailed) && !r.startedAtTime.Before(last24hCutoff) {
				failCounts[r.PipelineID]++
				failNames[r.PipelineID] = r.PipelineName
			}
		}
		trends := make([]dayTrend, 0, 7)
		for i := 6; i >= 0; i-- {
			d := now.AddDate(0, 0, -i).Format("2006-01-02")
			trends = append(trends, *trendMap[d])
		}
		// Top 5 failing
		type failEntry struct {
			PipelineID string `json:"pipeline_id"`
			Name       string `json:"name"`
			FailCount  int    `json:"fail_count"`
		}
		topFailing := make([]failEntry, 0, len(failCounts))
		for pid, count := range failCounts {
			topFailing = append(topFailing, failEntry{pid, failNames[pid], count})
		}
		// Highest count first, ties by pipeline id so map iteration order
		// does not reorder equal entries between requests.
		sort.SliceStable(topFailing, func(i, j int) bool {
			if topFailing[i].FailCount != topFailing[j].FailCount {
				return topFailing[i].FailCount > topFailing[j].FailCount
			}
			return topFailing[i].PipelineID < topFailing[j].PipelineID
		})
		if len(topFailing) > 5 {
			topFailing = topFailing[:5]
		}

		// Per-pipeline 24h rollup. A UI that collapses consecutive runs of
		// the same pipeline into one row needs the true count for that
		// pipeline — counting the entries in `recent_runs` would report the
		// size of the sample (at most 50) rather than the real number, so a
		// pipeline run 10,000 times would read "50". These counts come from
		// the same full per-pipeline window every other aggregate here uses.
		type pipelineRollup struct {
			PipelineID    string `json:"pipeline_id"`
			Name          string `json:"name"`
			Total         int    `json:"total"`
			Success       int    `json:"success"`
			Failed        int    `json:"failed"`
			Running       int    `json:"running"`
			LastStatus    string `json:"last_status,omitempty"`
			LastStartedAt string `json:"last_started_at,omitempty"`
		}
		rollupByPipeline := make(map[string]*pipelineRollup)
		rollupOrder := make([]string, 0, len(pipelines))
		for _, r := range allRuns {
			if !r.hasStartedAt || r.startedAtTime.Before(last24hCutoff) {
				continue
			}
			ru, seen := rollupByPipeline[r.PipelineID]
			if !seen {
				ru = &pipelineRollup{PipelineID: r.PipelineID, Name: r.PipelineName}
				rollupByPipeline[r.PipelineID] = ru
				rollupOrder = append(rollupOrder, r.PipelineID)
				// allRuns is sorted newest-first, so the first entry seen for
				// a pipeline is its most recent run.
				ru.LastStatus = r.Status
				ru.LastStartedAt = r.StartedAt
			}
			ru.Total++
			switch r.Status {
			case string(models.RunStatusSuccess):
				ru.Success++
			case string(models.RunStatusFailed):
				ru.Failed++
			case string(models.RunStatusRunning):
				ru.Running++
			}
		}
		pipelineRollups := make([]pipelineRollup, 0, len(rollupOrder))
		for _, pid := range rollupOrder {
			pipelineRollups = append(pipelineRollups, *rollupByPipeline[pid])
		}

		writeJSON(w, http.StatusOK, map[string]interface{}{
			"pipelines":        summaries,
			"recent_runs":      recentRuns,
			"trends":           trends,
			"top_failing":      topFailing,
			"pipeline_rollups": pipelineRollups,
			// Real aggregate counts. The frontend should read these directly
			// instead of deriving stats from `recent_runs` (which is a small
			// UI sample, not a complete count).
			"runs_today":       runsToday,
			"runs_yesterday":   runsYesterday,
			"runs_running":     runsRunning,
			"running_run_ids":  runningRunIDs,
			"runs_24h_total":   runs24hTotal,
			"runs_24h_success": runs24hSuccess,
			"runs_24h_failed":  runs24hFailed,
			// Finished runs are the denominator of success_rate_24h, sent so
			// a client can render "3 of 4" without recomputing it and
			// disagreeing with the server.
			"runs_24h_finished": runs24hFinished,
			// Null when nothing finished in the window.
			"success_rate_24h": successRate24h,
			// The window top_failing covers, so the panel can label itself
			// rather than imply a period it does not have.
			"top_failing_window_hours": 24,
			// The per-pipeline read limit these aggregates were computed
			// under. A pipeline with more runs than this in the window is
			// under-counted, and a client that knows the cap can say so
			// (#608).
			"runs_per_pipeline_cap": dashboardRunsPerPipeline,
		})
	}
}

// pipelineSummaryHandler handles GET /pipelines/summary — returns pipelines with run stats.
func pipelineSummaryHandler(s store.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		pipelinesList, _ := listPipelinesForRequest(s, r)
		type PipelineWithRun struct {
			PipelineSummary
			LastRunStatus string   `json:"last_run_status"`
			LastRunAt     string   `json:"last_run_at,omitempty"`
			LastRunError  string   `json:"last_run_error,omitempty"`
			RunsTotal     int      `json:"runs_total"`
			RunsSuccess   int      `json:"runs_success"`
			RunsFailed    int      `json:"runs_failed"`
			RunsRunning   int      `json:"runs_running"`
			RunHistory    []string `json:"run_history"`
		}
		results := make([]PipelineWithRun, 0, len(pipelinesList))
		for _, p := range pipelinesList {
			pr := PipelineWithRun{PipelineSummary: toPipelineSummary(p)}
			runs, _ := s.ListRunsByPipeline(p.ID, 200)
			pr.RunsTotal = len(runs)
			for _, run := range runs {
				switch string(run.Status) {
				case "completed", "success":
					pr.RunsSuccess++
				case "failed":
					pr.RunsFailed++
				case "running":
					pr.RunsRunning++
				}
			}
			for i := 0; i < len(runs) && i < 5; i++ {
				status := string(runs[i].Status)
				if status == "completed" || status == "success" {
					status = "succeeded"
				}
				pr.RunHistory = append(pr.RunHistory, status)
			}
			if len(runs) > 0 {
				pr.LastRunStatus = string(runs[0].Status)
				if runs[0].StartedAt != nil {
					pr.LastRunAt = runs[0].StartedAt.Format("2006-01-02T15:04:05Z07:00")
				}
				runs[0].PopulateError()
				pr.LastRunError = runs[0].Error
			}
			results = append(results, pr)
		}
		writeJSON(w, http.StatusOK, results)
	}
}

// searchHandler handles GET /search — searches across pipelines, connections, and variables.
func searchHandler(s store.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		q := strings.ToLower(r.URL.Query().Get("q"))
		limit := 30
		if l := r.URL.Query().Get("limit"); l != "" {
			if n, err := strconv.Atoi(l); err == nil && n > 0 && n <= 100 {
				limit = n
			}
		}

		type SearchResult struct {
			Type        string `json:"type"`
			ID          string `json:"id"`
			Name        string `json:"name"`
			Description string `json:"description,omitempty"`
			Meta        string `json:"meta,omitempty"`
		}

		var results []SearchResult
		wsID := GetWorkspaceID(r)

		// Search pipelines
		if pipes, err := listPipelinesForRequest(s, r); err == nil {
			for _, p := range pipes {
				if strings.Contains(strings.ToLower(p.Name), q) || strings.Contains(strings.ToLower(p.Description), q) {
					results = append(results, SearchResult{Type: "pipeline", ID: p.ID, Name: p.Name, Description: p.Description, Meta: p.Schedule})
				}
				if len(results) >= limit {
					break
				}
			}
		}

		// Search connections
		if len(results) < limit {
			if conns, err := s.ListConnectionsByWorkspace(wsID); err == nil {
				for _, c := range conns {
					if strings.Contains(strings.ToLower(c.ConnID), q) || strings.Contains(strings.ToLower(c.Description), q) || strings.Contains(strings.ToLower(string(c.Type)), q) {
						results = append(results, SearchResult{Type: "connection", ID: c.ID, Name: c.ConnID, Description: c.Description, Meta: string(c.Type)})
					}
					if len(results) >= limit {
						break
					}
				}
			}
		}

		// Search variables
		if len(results) < limit {
			if vars, err := s.ListVariablesByWorkspace(wsID); err == nil {
				for _, v := range vars {
					if strings.Contains(strings.ToLower(v.Key), q) || strings.Contains(strings.ToLower(v.Description), q) {
						results = append(results, SearchResult{Type: "variable", ID: v.Key, Name: v.Key, Description: v.Description, Meta: string(v.Type)})
					}
					if len(results) >= limit {
						break
					}
				}
			}
		}

		if results == nil {
			results = []SearchResult{}
		}
		writeJSON(w, http.StatusOK, results)
	}
}

// NotificationSettingsHandler groups handlers for notification settings CRUD.
type NotificationSettingsHandler struct {
	store store.Store
	ext   *extensions.Registry
}

// NewNotificationSettingsHandler creates a new NotificationSettingsHandler.
func NewNotificationSettingsHandler(s store.Store, ext *extensions.Registry) *NotificationSettingsHandler {
	return &NotificationSettingsHandler{store: s, ext: ext}
}

// Get handles GET /settings/notifications.
func (h *NotificationSettingsHandler) Get(w http.ResponseWriter, r *http.Request) {
	webhook, _ := h.store.GetSetting("slack_webhook")
	channel, _ := h.store.GetSetting("slack_channel")
	username, _ := h.store.GetSetting("slack_username")
	// Mask webhook URL for security — only show last 8 chars
	maskedWebhook := ""
	if webhook != "" {
		if len(webhook) > 12 {
			maskedWebhook = "****" + webhook[len(webhook)-8:]
		} else {
			maskedWebhook = "****"
		}
	}
	// Teams
	teamsWH, _ := h.store.GetSetting("teams_webhook")
	maskedTeams := ""
	if teamsWH != "" {
		if len(teamsWH) > 12 {
			maskedTeams = "****" + teamsWH[len(teamsWH)-8:]
		} else {
			maskedTeams = "****"
		}
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"webhook_configured":   webhook != "",
		"webhook_masked":       maskedWebhook,
		"channel":              channel,
		"username":             username,
		"teams_configured":     teamsWH != "",
		"teams_webhook_masked": maskedTeams,
	})
}

// Update handles PUT /settings/notifications.
func (h *NotificationSettingsHandler) Update(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Webhook      string `json:"webhook"`
		Channel      string `json:"channel"`
		Username     string `json:"username"`
		TeamsWebhook string `json:"teams_webhook"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	if req.Webhook != "" {
		h.store.SetSetting("slack_webhook", req.Webhook)
	}
	h.store.SetSetting("slack_channel", req.Channel)
	h.store.SetSetting("slack_username", req.Username)
	if req.TeamsWebhook != "" {
		h.store.SetSetting("teams_webhook", req.TeamsWebhook)
	}

	// Reconfigure the notifier if extensions support it
	if h.ext != nil && h.ext.Notifier != nil {
		// The notifier reads from env, but we also support DB-stored config
		// For now just log it — the notifier will pick up env vars on next restart
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "saved"})
}

// Test handles POST /settings/notifications/test.
func (h *NotificationSettingsHandler) Test(w http.ResponseWriter, r *http.Request) {
	webhook, _ := h.store.GetSetting("slack_webhook")
	if webhook == "" {
		writeError(w, http.StatusBadRequest, "no webhook URL configured")
		return
	}
	channel, _ := h.store.GetSetting("slack_channel")
	username, _ := h.store.GetSetting("slack_username")
	if username == "" {
		username = "Brokoli"
	}

	// Send test message
	payload := map[string]interface{}{
		"username": username,
		"attachments": []map[string]interface{}{{
			"color":  "#0d9488",
			"title":  "Brokoli Test Notification",
			"text":   "If you see this, Slack alerts are working correctly.",
			"footer": "Brokoli Orchestrator",
		}},
	}
	if channel != "" {
		payload["channel"] = channel
	}
	data, _ := json.Marshal(payload)
	client := netguard.Outbound().Client(10 * time.Second)
	resp, err := client.Post(webhook, "application/json", strings.NewReader(string(data)))
	if err != nil {
		writeError(w, http.StatusBadGateway, "webhook request failed: "+err.Error())
		return
	}
	resp.Body.Close()
	if resp.StatusCode >= 400 {
		writeError(w, http.StatusBadGateway, fmt.Sprintf("Slack returned HTTP %d", resp.StatusCode))
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "sent"})
}

// Delete handles DELETE /settings/notifications.
func (h *NotificationSettingsHandler) Delete(w http.ResponseWriter, r *http.Request) {
	h.store.SetSetting("slack_webhook", "")
	h.store.SetSetting("slack_channel", "")
	h.store.SetSetting("slack_username", "")
	h.store.SetSetting("teams_webhook", "")
	writeJSON(w, http.StatusOK, map[string]string{"status": "cleared"})
}

// lineageHandler handles GET /lineage — returns pipeline lineage graph.
func lineageHandler(s store.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		pipelines, err := listPipelinesForRequest(s, r)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		profiles := make(map[string]engine.LineageProfile)
		if profileStore, ok := s.(store.NodeProfileStore); ok {
			pipelineIDs := make([]string, len(pipelines))
			for i, pipeline := range pipelines {
				pipelineIDs[i] = pipeline.ID
			}
			// One batched query for every pipeline's nodes, not one query
			// per node — see GetLatestNodeProfilesForPipelines.
			records, recordsErr := profileStore.GetLatestNodeProfilesForPipelines(pipelineIDs)
			if recordsErr == nil {
				for key, record := range records {
					var profile engine.DataProfile
					if json.Unmarshal([]byte(record.ProfileJSON), &profile) != nil || len(profile.Columns) == 0 {
						continue
					}
					var schema engine.SchemaSnapshot
					_ = json.Unmarshal([]byte(record.SchemaJSON), &schema)
					profiles[key] = engine.LineageProfile{
						Profile:    &profile,
						Schema:     &schema,
						RunID:      record.RunID,
						ObservedAt: &record.ObservedAt,
					}
				}
			}
		}
		graph := engine.BuildLineageGraphWithProfiles(pipelines, profiles)
		writeJSON(w, http.StatusOK, graph)
	}
}

// schedulerStatusHandler handles GET /scheduler/status.
func schedulerStatusHandler(sched *engine.Scheduler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if sched == nil {
			writeJSON(w, http.StatusOK, []engine.ScheduleInfo{})
			return
		}
		status := sched.Status()
		if status == nil {
			status = []engine.ScheduleInfo{}
		}
		writeJSON(w, http.StatusOK, status)
	}
}

// bulkPipelineHandler handles POST /pipelines/bulk — bulk operations on pipelines.
func bulkPipelineHandler(s store.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			IDs    []string `json:"ids"`
			Action string   `json:"action"` // delete, enable, disable
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "invalid JSON")
			return
		}
		if len(req.IDs) > 100 {
			writeError(w, http.StatusBadRequest, "max 100 operations per request")
			return
		}
		type BulkResultItem struct {
			ID    string `json:"id"`
			Error string `json:"error,omitempty"`
		}
		var succeeded []string
		var failed []BulkResultItem

		for _, id := range req.IDs {
			p, e := s.GetPipeline(id)
			if e != nil {
				failed = append(failed, BulkResultItem{ID: id, Error: "not found"})
				continue
			}
			if !ValidateOrgAccess(r, p.OrgID) {
				failed = append(failed, BulkResultItem{ID: id, Error: "access denied"})
				continue
			}
			var err error
			switch req.Action {
			case "delete":
				err = s.DeletePipeline(id)
			case "enable", "disable":
				p.Enabled = req.Action == "enable"
				err = s.UpdatePipeline(p)
			}
			if err != nil {
				failed = append(failed, BulkResultItem{ID: id, Error: err.Error()})
			} else {
				succeeded = append(succeeded, id)
			}
		}

		writeJSON(w, http.StatusOK, map[string]interface{}{
			"action":    req.Action,
			"succeeded": len(succeeded),
			"failed":    len(failed),
			"errors":    failed,
		})
	}
}

// connectionUsedByHandler handles GET /connections/{connId}/used-by.
func connectionUsedByHandler(s store.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		connID := chi.URLParam(r, "connId")
		pipelines, _ := listPipelinesForRequest(s, r)
		var usedBy []map[string]string
		for _, p := range pipelines {
			for _, n := range p.Nodes {
				if cid, ok := n.Config["conn_id"].(string); ok && cid == connID {
					usedBy = append(usedBy, map[string]string{"pipeline_id": p.ID, "pipeline_name": p.Name, "node_id": n.ID, "node_name": n.Name})
				}
			}
		}
		if usedBy == nil {
			usedBy = []map[string]string{}
		}
		writeJSON(w, http.StatusOK, usedBy)
	}
}

// variableUsedByHandler handles GET /variables/{key}/used-by.
func variableUsedByHandler(s store.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		varKey := chi.URLParam(r, "key")
		pipelines, _ := listPipelinesForRequest(s, r)
		pattern := "${var." + varKey + "}"
		var usedBy []map[string]string
		for _, p := range pipelines {
			for _, n := range p.Nodes {
				// Check all config values for variable references
				for _, v := range n.Config {
					if str, ok := v.(string); ok && len(str) > 0 {
						if strings.Contains(str, pattern) || strings.Contains(str, "{{ var."+varKey+" }}") {
							usedBy = append(usedBy, map[string]string{"pipeline_id": p.ID, "pipeline_name": p.Name, "node_id": n.ID, "node_name": n.Name})
							break
						}
					}
				}
			}
		}
		if usedBy == nil {
			usedBy = []map[string]string{}
		}
		writeJSON(w, http.StatusOK, usedBy)
	}
}
