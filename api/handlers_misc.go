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
		// The org check above is on the PIPELINE in the path. The entry
		// being resolved is named by a separate id, and nothing tied the
		// two together: a caller could pass their own pipeline, which
		// passes the check, and any dlqId at all, including another
		// tenant's. Resolving it marks somebody else's failure dealt
		// with, in their inbox, without them ever seeing it.
		dlqID := chi.URLParam(r, "dlqId")
		if !dlqEntryBelongsToPipeline(s, pipelineID, dlqID) {
			// Not found rather than forbidden, matching every other
			// cross-tenant refusal here: the pair of answers would
			// otherwise confirm which entry ids exist elsewhere.
			writeError(w, http.StatusNotFound, "dead-letter entry not found")
			return
		}
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
		// #241: a webhook is nobody in particular, but it is not the
		// scheduler and it is not a person, and saying so is the point.
		run, err := e.RunPipelineOpts(p.ID, engine.RunOptions{
			TriggeredBy: &models.RunAttribution{Kind: models.RunTriggerKindWebhook},
		})
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
		// An empty org means "no filter" to the store, so this returned
		// every pipeline in the deployment -- names, ids and the
		// dependency structure of every tenant -- to a caller whose org
		// could not be resolved. listPipelinesForRequest already gets
		// this right a few hundred lines away: in multi-tenant mode, a
		// user without an org sees nothing rather than everything.
		if orgID == "" && OrgResolverFunc != nil {
			writeJSON(w, http.StatusOK, map[string]interface{}{
				"nodes": []map[string]interface{}{},
				"edges": []map[string]interface{}{},
			})
			return
		}
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

// dashboardRecentRunsSample is how many runs the "Recent activity" list
// may hold, per pipeline before merging and in total after.
//
// It is a sample and nothing else. Until #608 the same read was also where
// every count came from, at 200 per pipeline, so a pipeline that ran more
// often than that in the reported window silently reported 200 and the
// seven-day series had those 200 rows to spread across seven days. The
// counts are database aggregates now and this bounds only the list.
const dashboardRecentRunsSample = 50

// dashboardRunningIDsCap bounds running_run_ids. A client uses it to clear
// stale live entries, so it has to be the whole set in practice; this is a
// guard against a pathological backlog, not a page size.
const dashboardRunningIDsCap = 5000

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
			// TriggeredBy is who started the run (#241). The dashboard's
			// recent-activity list is one of the three places the issue
			// names, and it builds its own row type rather than reusing
			// models.Run, so the field has to be carried here too.
			TriggeredBy *models.RunAttribution `json:"triggered_by,omitempty"`

			startedAtTime time.Time
			hasStartedAt  bool
		}

		names := make(map[string]string, len(pipelines))
		for _, p := range pipelines {
			names[p.ID] = p.Name
		}

		// Which runs this request may see. Runs carry org_id; in community
		// mode there is no org, and the workspace reaches them through the
		// pipeline. This mirrors listPipelinesForRequest, which decided the
		// pipeline list above.
		scope := store.RunScope{OrgID: orgID}
		if orgID == "" && OrgResolverFunc == nil {
			scope.WorkspaceID = GetWorkspaceID(r)
		}

		now := time.Now()
		todayStr := now.Format("2006-01-02")
		yesterdayStr := now.AddDate(0, 0, -1).Format("2006-01-02")
		last24hCutoff := now.Add(-24 * time.Hour)

		// The recent-activity list. A bounded sample by definition, and the
		// only thing here that loads run rows. Every count below is a
		// COUNT(*) in the database (#608), so none of them is limited by
		// what this returns, and a pipeline that runs 1,440 times a day no
		// longer reports 200.
		recentRuns := make([]runEntry, 0, dashboardRecentRunsSample)
		for _, p := range pipelines {
			runs, err := s.ListRunsByPipeline(p.ID, dashboardRecentRunsSample)
			if err != nil {
				// Not fatal: the sample is a convenience and the counts do
				// not come from it. Discarding it silently is what made a
				// pipeline whose runs could not be read indistinguishable
				// from one that had never run.
				log.Printf("dashboard: recent runs for pipeline %q unavailable: %v", p.ID, err)
				continue
			}
			for _, run := range runs {
				run.PopulateError()
				entry := runEntry{
					PipelineID:   p.ID,
					PipelineName: p.Name,
					RunID:        run.ID,
					Status:       string(run.Status),
					Error:        run.Error,
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
				if run.StartedAt != nil && run.FinishedAt != nil {
					if d := run.FinishedAt.Sub(*run.StartedAt); d > 0 {
						entry.DurationMs = d.Milliseconds()
					}
				}
				recentRuns = append(recentRuns, entry)
			}
		}
		// Newest first. Compared as instants rather than as strings: at
		// whole-second resolution runs that started in the same second
		// compared equal, so their order came down to which pipeline was
		// read first. The run id breaks a genuine tie so the order is
		// stable between requests.
		sort.SliceStable(recentRuns, func(i, j int) bool {
			a, b := recentRuns[i], recentRuns[j]
			if a.hasStartedAt != b.hasStartedAt {
				return a.hasStartedAt
			}
			if !a.startedAtTime.Equal(b.startedAtTime) {
				return a.startedAtTime.After(b.startedAtTime)
			}
			return a.RunID > b.RunID
		})
		if len(recentRuns) > dashboardRecentRunsSample {
			recentRuns = recentRuns[:dashboardRecentRunsSample]
		}

		// Who started each of them, in one batched read for the page
		// (#241). Losing this leaves the runs and their counts intact,
		// so it is logged rather than fatal: the dashboard's job is to
		// show what ran.
		if len(recentRuns) > 0 {
			ids := make([]string, 0, len(recentRuns))
			for i := range recentRuns {
				ids = append(ids, recentRuns[i].RunID)
			}
			if byRun, err := s.GetRunAttribution(ids); err == nil {
				for i := range recentRuns {
					if a, ok := byRun[recentRuns[i].RunID]; ok {
						attribution := a
						recentRuns[i].TriggeredBy = &attribution
					}
				}
			} else {
				log.Printf("dashboard: could not read who started these runs: %v", err)
			}
		}

		// ── Counts, from the database ──────────────────────────

		// Per pipeline and status over the last 24 hours. Feeds the 24h
		// totals, the per-pipeline rollup and the failure ranking.
		byPipeline, err := s.AggregateRunsByPipelineStatus(last24hCutoff, scope)
		if err != nil {
			// These are the figures the page is for. Serving zeroes with a
			// 200 would present a failed read as a quiet system, which is
			// the shape #529 exists to stop.
			writeError(w, http.StatusInternalServerError, "run counts unavailable")
			return
		}

		// Per calendar day over the last seven, in the server's local zone,
		// which is what "today" means everywhere else on this endpoint.
		trendStart := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location()).AddDate(0, 0, -6)
		byDay, err := s.AggregateRunsByDayStatus(trendStart, store.OffsetMinutesFor(now), scope)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "run counts unavailable")
			return
		}

		var runs24hTotal, runs24hSuccess, runs24hFailed int
		for _, a := range byPipeline {
			runs24hTotal += a.Count
			switch a.Status {
			case string(models.RunStatusSuccess):
				runs24hSuccess += a.Count
			case string(models.RunStatusFailed):
				runs24hFailed += a.Count
			}
		}

		var runsToday, runsYesterday int
		for _, a := range byDay {
			switch a.Day {
			case todayStr:
				runsToday += a.Count
			case yesterdayStr:
				runsYesterday += a.Count
			}
		}

		// Authoritative list of currently-running run IDs. The frontend uses
		// this to reconcile its client-side liveRunStatuses store: any "running"
		// entry whose ID is NOT in this list is stale (probably from a missed
		// run.completed event during a reconnect window) and should be cleared.
		//
		// Not windowed, deliberately: a run still marked running from before
		// the window is exactly the stale entry a client needs told about.
		runningRunIDs, err := s.ListRunIDsByStatus(string(models.RunStatusRunning), scope, dashboardRunningIDsCap)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "run counts unavailable")
			return
		}
		if runningRunIDs == nil {
			runningRunIDs = []string{}
		}
		runsRunning := len(runningRunIDs)

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

		summaries := make([]PipelineSummary, 0, len(pipelines))
		for _, p := range pipelines {
			summaries = append(summaries, toPipelineSummary(p))
		}

		// Daily trends, last 7 days.
		type dayTrend struct {
			Date    string `json:"date"`
			Success int    `json:"success"`
			Failed  int    `json:"failed"`
			Total   int    `json:"total"`
		}
		trendMap := make(map[string]*dayTrend, 7)
		trends := make([]dayTrend, 0, 7)
		for i := 6; i >= 0; i-- {
			d := now.AddDate(0, 0, -i).Format("2006-01-02")
			trendMap[d] = &dayTrend{Date: d}
		}
		for _, a := range byDay {
			t, ok := trendMap[a.Day]
			if !ok {
				continue
			}
			t.Total += a.Count
			switch a.Status {
			case string(models.RunStatusSuccess):
				t.Success += a.Count
			case string(models.RunStatusFailed):
				t.Failed += a.Count
			}
		}
		for i := 6; i >= 0; i-- {
			trends = append(trends, *trendMap[now.AddDate(0, 0, -i).Format("2006-01-02")])
		}

		// Top failing pipelines, over the same 24 hours as the aggregates
		// they sit beside. This used to count every failure in the loaded
		// window regardless of age, so a pipeline that failed forty times
		// last month outranked one that failed twice this morning, and the
		// effective period differed per pipeline because 200 runs is a day
		// for one schedule and a year for another (#610).
		type failEntry struct {
			PipelineID string `json:"pipeline_id"`
			Name       string `json:"name"`
			FailCount  int    `json:"fail_count"`
		}
		topFailing := make([]failEntry, 0, 4)
		for _, a := range byPipeline {
			if a.Status != string(models.RunStatusFailed) || a.Count == 0 {
				continue
			}
			topFailing = append(topFailing, failEntry{a.PipelineID, names[a.PipelineID], a.Count})
		}
		// Highest count first, ties by pipeline id so the order does not
		// depend on the order rows came back in.
		sort.SliceStable(topFailing, func(i, j int) bool {
			if topFailing[i].FailCount != topFailing[j].FailCount {
				return topFailing[i].FailCount > topFailing[j].FailCount
			}
			return topFailing[i].PipelineID < topFailing[j].PipelineID
		})
		if len(topFailing) > 5 {
			topFailing = topFailing[:5]
		}

		// Per-pipeline 24h rollup, from the same grouped counts. A UI that
		// collapses consecutive runs of one pipeline into a row needs the
		// true count: counting entries in recent_runs would report the size
		// of the sample, so a pipeline run 10,000 times would read 50.
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
		for _, a := range byPipeline {
			ru, seen := rollupByPipeline[a.PipelineID]
			if !seen {
				ru = &pipelineRollup{PipelineID: a.PipelineID, Name: names[a.PipelineID]}
				rollupByPipeline[a.PipelineID] = ru
				rollupOrder = append(rollupOrder, a.PipelineID)
			}
			ru.Total += a.Count
			switch a.Status {
			case string(models.RunStatusSuccess):
				ru.Success += a.Count
			case string(models.RunStatusFailed):
				ru.Failed += a.Count
			case string(models.RunStatusRunning):
				ru.Running += a.Count
			}
		}
		// Last status per pipeline, from the newest-first sample. A grouped
		// row shows it without a second request. A pipeline whose latest run
		// fell outside the sample simply has none, which is honest: the
		// alternative was taking it from a 200-run window and calling a run
		// the latest when it was not.
		for i := len(recentRuns) - 1; i >= 0; i-- {
			if ru, ok := rollupByPipeline[recentRuns[i].PipelineID]; ok {
				ru.LastStatus = recentRuns[i].Status
				ru.LastStartedAt = recentRuns[i].StartedAt
			}
		}
		sort.Strings(rollupOrder)
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
			// Real aggregate counts, from the database. The frontend should
			// read these directly instead of deriving stats from
			// `recent_runs`, which is a bounded UI sample.
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
			// How many runs recent_runs may hold. It is a sample; no count
			// on this response is bounded by it.
			"recent_runs_sample_size": dashboardRecentRunsSample,
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
		// ADR-039: an edge can be attested only against the execution record
		// of the run its profile came from. One batch read covers every run
		// the profiles came from. A store that keeps no provenance attests
		// nothing, and every edge stays declared -- the honest default.
		if provStore, ok := s.(interface {
			GetNodeProvenanceForRuns([]string) (map[string][]models.NodeProvenance, error)
		}); ok && len(profiles) > 0 {
			seenRun := map[string]bool{}
			var runIDs []string
			for _, prof := range profiles {
				if prof.RunID != "" && !seenRun[prof.RunID] {
					seenRun[prof.RunID] = true
					runIDs = append(runIDs, prof.RunID)
				}
			}
			if byRun, err := provStore.GetNodeProvenanceForRuns(runIDs); err == nil {
				// Keys are rebuilt from the pipelines rather than parsed back
				// out of the map, so an id containing the separator cannot
				// attach one node's record to another.
				for _, p := range pipelines {
					for _, n := range p.Nodes {
						key := engine.ProfileKey(p.ID, n.ID)
						prof, ok := profiles[key]
						if !ok {
							continue
						}
						for i := range byRun[prof.RunID] {
							if byRun[prof.RunID][i].NodeID == n.ID {
								rec := byRun[prof.RunID][i]
								prof.Provenance = &rec
								profiles[key] = prof
								break
							}
						}
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
