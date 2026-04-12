package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Tnsor-Labs/brokoli/engine"
	"github.com/Tnsor-Labs/brokoli/extensions"
	"github.com/Tnsor-Labs/brokoli/models"
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

// webhookTriggerHandler handles POST /pipelines/{id}/webhook — triggers a pipeline run via webhook token.
func webhookTriggerHandler(s store.Store, e *engine.Engine) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := chi.URLParam(r, "id")

		// Rate limit: max 1 webhook trigger per pipeline per 10 seconds
		webhookLimiter.Lock()
		if last, ok := webhookLimiter.last[id]; ok && time.Since(last) < 10*time.Second {
			webhookLimiter.Unlock()
			writeError(w, http.StatusTooManyRequests, "webhook rate limit exceeded — try again in 10 seconds")
			return
		}
		webhookLimiter.last[id] = time.Now()
		webhookLimiter.Unlock()

		token := r.URL.Query().Get("token")
		if token == "" {
			token = r.Header.Get("X-Webhook-Token")
		}
		p, err := s.GetPipeline(id)
		if err != nil {
			writeError(w, http.StatusNotFound, "pipeline not found")
			return
		}
		if p.WebhookToken == "" {
			writeError(w, http.StatusForbidden, "webhook not configured for this pipeline")
			return
		}
		if token != p.WebhookToken {
			writeError(w, http.StatusUnauthorized, "invalid webhook token")
			return
		}
		run, err := e.RunPipeline(p.ID)
		if err != nil {
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

// calendarHandler handles GET /runs/calendar — returns run calendar data.
func calendarHandler(s store.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		days := 90
		if d := r.URL.Query().Get("days"); d != "" {
			fmt.Sscanf(d, "%d", &days)
		}
		if days < 1 || days > 365 {
			days = 90
		}

		orgID := GetOrgIDFromRequest(r)
		cal, err := s.GetRunCalendarByOrg(days, orgID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		if cal == nil {
			cal = []store.CalendarDay{}
		}
		writeJSON(w, http.StatusOK, cal)
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
		}

		// Load a wider per-pipeline window so the aggregates below reflect
		// reality, not the last few entries. We load all of them into one
		// flat list (`allRuns`), then take the head as the small UI sample
		// (`recentRuns`). 200 per pipeline matches pipelineSummaryHandler.
		var allRuns []runEntry
		for _, p := range pipelines {
			runs, _ := s.ListRunsByPipeline(p.ID, 200)
			for _, run := range runs {
				run.PopulateError()
				entry := runEntry{
					PipelineID:   p.ID,
					PipelineName: p.Name,
					RunID:        run.ID,
					Status:       string(run.Status),
					Error:        run.Error,
				}
				if run.StartedAt != nil {
					entry.StartedAt = run.StartedAt.Format("2006-01-02T15:04:05Z07:00")
				}
				if run.FinishedAt != nil {
					entry.FinishedAt = run.FinishedAt.Format("2006-01-02T15:04:05Z07:00")
				}
				allRuns = append(allRuns, entry)
			}
		}
		// Sort all runs by started_at desc so head = most recent.
		for i := 0; i < len(allRuns); i++ {
			for j := i + 1; j < len(allRuns); j++ {
				if allRuns[j].StartedAt > allRuns[i].StartedAt {
					allRuns[i], allRuns[j] = allRuns[j], allRuns[i]
				}
			}
		}

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
			if len(run.StartedAt) >= 10 {
				day := run.StartedAt[:10]
				if day == todayStr {
					runsToday++
				} else if day == yesterdayStr {
					runsYesterday++
				}
			}
			if run.StartedAt != "" {
				if t, err := time.Parse("2006-01-02T15:04:05Z07:00", run.StartedAt); err == nil && !t.Before(last24hCutoff) {
					runs24hTotal++
					switch run.Status {
					case "success", "completed":
						runs24hSuccess++
					case "failed":
						runs24hFailed++
					}
				}
			}
			if run.Status == "running" {
				runsRunning++
				runningRunIDs = append(runningRunIDs, run.RunID)
			}
		}

		var successRate24h int
		if runs24hTotal > 0 {
			successRate24h = int((float64(runs24hSuccess) / float64(runs24hTotal)) * 100)
		} else {
			successRate24h = 100 // no runs in window — neutral default
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
			d := time.Now().AddDate(0, 0, -i).Format("2006-01-02")
			trendMap[d] = &dayTrend{Date: d}
		}
		// Top failing pipelines, also from the full window.
		failCounts := make(map[string]int)
		failNames := make(map[string]string)
		for _, r := range allRuns {
			if len(r.StartedAt) >= 10 {
				day := r.StartedAt[:10]
				if t, ok := trendMap[day]; ok {
					t.Total++
					if r.Status == "success" || r.Status == "completed" {
						t.Success++
					} else if r.Status == "failed" {
						t.Failed++
					}
				}
			}
			if r.Status == "failed" {
				failCounts[r.PipelineID]++
				failNames[r.PipelineID] = r.PipelineName
			}
		}
		trends := make([]dayTrend, 0, 7)
		for i := 6; i >= 0; i-- {
			d := time.Now().AddDate(0, 0, -i).Format("2006-01-02")
			trends = append(trends, *trendMap[d])
		}
		// Top 5 failing
		type failEntry struct {
			PipelineID string `json:"pipeline_id"`
			Name       string `json:"name"`
			FailCount  int    `json:"fail_count"`
		}
		var topFailing []failEntry
		for pid, count := range failCounts {
			topFailing = append(topFailing, failEntry{pid, failNames[pid], count})
		}
		// Sort by fail count desc
		for i := 0; i < len(topFailing); i++ {
			for j := i + 1; j < len(topFailing); j++ {
				if topFailing[j].FailCount > topFailing[i].FailCount {
					topFailing[i], topFailing[j] = topFailing[j], topFailing[i]
				}
			}
		}
		if len(topFailing) > 5 {
			topFailing = topFailing[:5]
		}
		if topFailing == nil {
			topFailing = []failEntry{}
		}

		writeJSON(w, http.StatusOK, map[string]interface{}{
			"pipelines":   summaries,
			"recent_runs": recentRuns,
			"trends":      trends,
			"top_failing": topFailing,
			// Real aggregate counts. The frontend should read these directly
			// instead of deriving stats from `recent_runs` (which is a small
			// UI sample, not a complete count).
			"runs_today":        runsToday,
			"runs_yesterday":    runsYesterday,
			"runs_running":      runsRunning,
			"running_run_ids":   runningRunIDs,
			"runs_24h_total":    runs24hTotal,
			"runs_24h_success":  runs24hSuccess,
			"runs_24h_failed":   runs24hFailed,
			"success_rate_24h":  successRate24h,
		})
	}
}

// pipelineSummaryHandler handles GET /pipelines/summary — returns pipelines with run stats.
func pipelineSummaryHandler(s store.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		pipelinesList, _ := listPipelinesForRequest(s, r)
		type PipelineWithRun struct {
			PipelineSummary
			LastRunStatus string `json:"last_run_status"`
			LastRunAt     string `json:"last_run_at,omitempty"`
			LastRunError  string `json:"last_run_error,omitempty"`
			RunsTotal     int    `json:"runs_total"`
			RunsSuccess   int    `json:"runs_success"`
			RunsFailed    int    `json:"runs_failed"`
			RunsRunning   int    `json:"runs_running"`
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
	resp, err := http.Post(webhook, "application/json", strings.NewReader(string(data)))
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
		graph := engine.BuildLineageGraph(pipelines)
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
