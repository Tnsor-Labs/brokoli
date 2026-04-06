package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/Tnsor-Labs/brokoli/engine"
	"github.com/Tnsor-Labs/brokoli/extensions"
	"github.com/Tnsor-Labs/brokoli/models"
	"github.com/Tnsor-Labs/brokoli/store"
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
		deps := make([]map[string]interface{}, 0)
		for _, depID := range p.DependsOn {
			dep := map[string]interface{}{"pipeline_id": depID, "satisfied": false}
			if dp, err := s.GetPipeline(depID); err == nil {
				dep["name"] = dp.Name
				runs, _ := s.ListRunsByPipeline(depID, 1)
				if len(runs) > 0 {
					dep["last_status"] = runs[0].Status
					dep["satisfied"] = runs[0].Status == "completed"
				}
			}
			deps = append(deps, dep)
		}
		writeJSON(w, http.StatusOK, deps)
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
func dashboardHandler(s store.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		orgID := GetOrgIDFromRequest(r)
		var pipelines []models.Pipeline
		if orgID != "" {
			pipelines, _ = s.ListPipelinesByOrg(orgID)
		} else if OrgResolverFunc != nil {
			// Multi-tenant: user with no org sees empty dashboard
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
		var recentRuns []runEntry
		for _, p := range pipelines {
			runs, _ := s.ListRunsByPipeline(p.ID, 3)
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
				recentRuns = append(recentRuns, entry)
			}
		}
		// Sort by started_at desc
		for i := 0; i < len(recentRuns); i++ {
			for j := i + 1; j < len(recentRuns); j++ {
				if recentRuns[j].StartedAt > recentRuns[i].StartedAt {
					recentRuns[i], recentRuns[j] = recentRuns[j], recentRuns[i]
				}
			}
		}
		if recentRuns == nil {
			recentRuns = []runEntry{}
		}
		// Truncate
		if len(recentRuns) > 50 {
			recentRuns = recentRuns[:50]
		}

		summaries := make([]PipelineSummary, 0, len(pipelines))
		for _, p := range pipelines {
			summaries = append(summaries, toPipelineSummary(p))
		}

		// Compute daily trends (last 7 days)
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
		// Top failing pipelines
		failCounts := make(map[string]int)
		failNames := make(map[string]string)
		for _, r := range recentRuns {
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
