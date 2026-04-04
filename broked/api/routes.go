package api

import (
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/golang-jwt/jwt/v5"
	"github.com/hc12r/broked/crypto"
	"github.com/hc12r/broked/engine"
	"github.com/hc12r/broked/extensions"
	"github.com/hc12r/broked/models"
	"github.com/hc12r/broked/store"
)

// RegisterRoutes sets up all API routes on the given router.
func RegisterRoutes(r chi.Router, s store.Store, e *engine.Engine, hub *Hub, sched *engine.Scheduler, ext *extensions.Registry, userStore *UserStore, cryptoCfg ...*crypto.Config) {
	ph := NewPipelineHandler(s, sched)
	rh := NewRunHandler(s, e)

	// Connection handler (crypto config optional for backward compat)
	var cc *crypto.Config
	if len(cryptoCfg) > 0 && cryptoCfg[0] != nil {
		cc = cryptoCfg[0]
	} else {
		cc = &crypto.Config{Key: make([]byte, 32)} // zero key fallback
	}
	ch := NewConnectionHandler(s, cc)
	vh := NewVariableHandler(s, cc)

	// Wire audit logger from extensions
	if ext != nil && ext.Audit != nil {
		auditLogger = ext.Audit
	}

	// Permission check - enterprise team features provide full RBAC;
	// fallback enforces basic role-based checks for open source.
	requirePerm := func(perm models.Permission) func(http.Handler) http.Handler {
		if ext != nil && ext.Team != nil && ext.Team.Enabled() {
			if mw := ext.Team.PermissionMiddleware(string(perm)); mw != nil {
				return mw.(func(http.Handler) http.Handler)
			}
		}
		// Fallback: basic role-based check for open source
		return func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				claims := r.Context().Value("claims")
				if claims == nil {
					next.ServeHTTP(w, r) // open mode (no users created)
					return
				}
				mc := claims.(*jwt.MapClaims)
				role, _ := (*mc)["role"].(string)
				// Viewers can only access read-only endpoints
				if role == "viewer" && isWritePermission(string(perm)) {
					writeError(w, http.StatusForbidden, "insufficient permissions")
					return
				}
				next.ServeHTTP(w, r)
			})
		}
	}

	r.Route("/api", func(r chi.Router) {
		// Pipelines
		r.Get("/pipelines", ph.List)
		r.With(requirePerm(models.PermPipelinesCreate)).Post("/pipelines", ph.Create)
		r.Get("/pipelines/{id}", ph.Get)
		r.With(requirePerm(models.PermPipelinesEdit)).Put("/pipelines/{id}", ph.Update)
		r.With(requirePerm(models.PermPipelinesDelete)).Delete("/pipelines/{id}", ph.Delete)
		r.With(requirePerm(models.PermPipelinesExport)).Get("/pipelines/{id}/export", ph.Export)
		r.Get("/pipelines/{id}/validate", ph.Validate)
		r.Get("/pipelines/{id}/versions", ph.ListVersions)
		r.With(requirePerm(models.PermPipelinesEdit)).Post("/pipelines/{id}/rollback", ph.Rollback)
		r.With(requirePerm(models.PermPipelinesEdit)).Post("/pipelines/{id}/clone", ph.Clone)
		r.Post("/pipelines/{id}/validate-nodes", ph.ValidateNodes)
		r.With(requirePerm(models.PermPipelinesCreate)).Post("/pipelines/import", ph.Import)

		// Runs
		r.With(requirePerm(models.PermPipelinesRun)).Post("/pipelines/{id}/run", rh.TriggerRun)
		r.With(requirePerm(models.PermPipelinesRun)).Post("/pipelines/{id}/dry-run", rh.DryRun)
		r.With(requirePerm(models.PermPipelinesRun)).Post("/pipelines/{id}/backfill", rh.Backfill)
		r.Get("/pipelines/{id}/runs", rh.ListByPipeline)
		r.Get("/runs/{id}", rh.Get)
		r.With(requirePerm(models.PermRunsResume)).Post("/runs/{id}/resume", rh.ResumeRun)
		r.With(requirePerm(models.PermRunsCancel)).Post("/runs/{id}/cancel", rh.CancelRun)
		r.Get("/runs/{id}/logs", rh.GetLogs)
		r.Get("/runs/{id}/logs/export", rh.ExportLogs)

		// Node profiles
		r.Get("/runs/{id}/nodes/{nodeId}/profile", rh.GetNodeProfile)

		// Dead Letter Queue
		r.Get("/pipelines/{id}/dlq", dlqListHandler(s))
		r.Post("/pipelines/{id}/dlq/{dlqId}/resolve", dlqResolveHandler(s))

		// Webhook trigger
		r.Post("/pipelines/{id}/webhook", webhookTriggerHandler(s, e))

		// Pipeline dependencies status
		r.Get("/pipelines/{id}/deps", pipelineDepsHandler(s))
		r.Get("/runs/{id}/nodes/{nodeId}/preview", rh.GetNodePreview)

		// Connections
		r.Get("/connections", ch.List)
		r.With(requirePerm(models.PermConnectionsCreate)).Post("/connections", ch.Create)
		r.Get("/connections/{connId}", ch.Get)
		r.With(requirePerm(models.PermConnectionsEdit)).Put("/connections/{connId}", ch.Update)
		r.With(requirePerm(models.PermConnectionsDelete)).Delete("/connections/{connId}", ch.Delete)
		r.With(requirePerm(models.PermConnectionsTest)).Post("/connections/{connId}/test", ch.Test)
		r.Get("/connection-types", ConnectionTypes)

		// Calendar
		r.Get("/runs/calendar", calendarHandler(s))

		// Bulk pipeline operations
		r.With(requirePerm(models.PermPipelinesDelete)).Post("/pipelines/bulk", bulkPipelineHandler(s))

		// Variables
		r.Get("/variables", vh.List)
		r.With(requirePerm(models.PermVariablesCreate)).Post("/variables", vh.Set)
		r.Get("/variables/{key}", vh.Get)
		r.With(requirePerm(models.PermVariablesEdit)).Put("/variables/{key}", vh.Set)
		r.With(requirePerm(models.PermVariablesDelete)).Delete("/variables/{key}", vh.Delete)

		// Lineage
		r.Get("/lineage", lineageHandler(s))

		// Scheduler
		r.Get("/scheduler/status", schedulerStatusHandler(sched))

		// Notifications / Slack config
		nh := NewNotificationSettingsHandler(s, ext)
		r.Get("/settings/notifications", nh.Get)
		r.With(requirePerm(models.PermSettingsEdit)).Put("/settings/notifications", nh.Update)
		r.With(requirePerm(models.PermSettingsEdit)).Post("/settings/notifications/test", nh.Test)
		r.With(requirePerm(models.PermSettingsEdit)).Delete("/settings/notifications", nh.Delete)

		// Pipeline list with last run status (single query)
		r.Get("/pipelines/summary", pipelineSummaryHandler(s))

		// Dashboard (single request for all dashboard data)
		r.Get("/dashboard", dashboardHandler(s))

		// Used-by tracking
		r.Get("/connections/{connId}/used-by", connectionUsedByHandler(s))
		r.Get("/variables/{key}/used-by", variableUsedByHandler(s))

		// Search across pipelines, connections, and variables
		r.Get("/search", searchHandler(s))

		// Utilities
		r.Post("/test-connection", rh.TestConnection)
		r.Get("/system/info", systemInfo(s, e))
		r.With(requirePerm(models.PermSettingsEdit)).Post("/system/purge", systemPurge(s))

		// WebSocket
		r.Get("/ws", hub.HandleWS)

		// Impact analysis (stub — returns downstream dependencies)
		r.Get("/pipelines/{id}/impact", func(w http.ResponseWriter, r *http.Request) {
			_ = chi.URLParam(r, "id")
			writeJSON(w, http.StatusOK, map[string]interface{}{
				"downstream": []interface{}{},
			})
		})

		// Enterprise: Git Sync API
		if ext != nil && ext.GitSync != nil {
			r.Get("/git/config", func(w http.ResponseWriter, r *http.Request) {
				writeJSON(w, http.StatusOK, ext.GitSync.Config())
			})
			r.Get("/git/status", func(w http.ResponseWriter, r *http.Request) {
				writeJSON(w, http.StatusOK, ext.GitSync.Status())
			})
			r.With(requirePerm(models.PermGitSyncPull)).Post("/git/pull", func(w http.ResponseWriter, r *http.Request) {
				count, err := ext.GitSync.Pull()
				if err != nil {
					writeError(w, http.StatusInternalServerError, err.Error())
					return
				}
				writeJSON(w, http.StatusOK, map[string]interface{}{
					"imported": count,
				})
			})
			r.With(requirePerm(models.PermGitSyncPush)).Post("/git/push/{id}", func(w http.ResponseWriter, r *http.Request) {
				id := chi.URLParam(r, "id")
				if err := ext.GitSync.Push(id); err != nil {
					writeError(w, http.StatusInternalServerError, err.Error())
					return
				}
				writeJSON(w, http.StatusOK, map[string]interface{}{
					"pushed": id,
				})
			})
		}

		// Enterprise: Audit log API
		if ext != nil && ext.Audit != nil {
			r.Get("/audit", func(w http.ResponseWriter, r *http.Request) {
				filter := extensions.AuditFilter{Limit: 500}
				if u := r.URL.Query().Get("user_id"); u != "" {
					filter.UserID = u
				}
				if a := r.URL.Query().Get("action"); a != "" {
					filter.Action = a
				}
				if res := r.URL.Query().Get("resource"); res != "" {
					filter.Resource = res
				}
				entries, err := ext.Audit.Query(filter)
				if err != nil {
					writeError(w, http.StatusInternalServerError, err.Error())
					return
				}
				if entries == nil {
					entries = []extensions.AuditEntry{}
				}
				if r.URL.Query().Get("detail") != "true" {
					type AuditSummary struct {
						ID         string `json:"id"`
						Timestamp  string `json:"timestamp"`
						UserID     string `json:"user_id"`
						Username   string `json:"username"`
						Action     string `json:"action"`
						Resource   string `json:"resource"`
						ResourceID string `json:"resource_id"`
						IP         string `json:"ip"`
						HasDiff    bool   `json:"has_diff"`
					}
					summaries := make([]AuditSummary, len(entries))
					for i, e := range entries {
						summaries[i] = AuditSummary{
							ID: e.ID, Timestamp: e.Timestamp.Format("2006-01-02T15:04:05Z07:00"),
							UserID: e.UserID, Username: e.Username,
							Action: e.Action, Resource: e.Resource,
							ResourceID: e.ResourceID, IP: e.IP,
							HasDiff: len(e.Before) > 0 || len(e.After) > 0,
						}
					}
					writeJSON(w, http.StatusOK, summaries)
					return
				}
				writeJSON(w, http.StatusOK, entries)
			})
			r.Get("/audit/{id}", func(w http.ResponseWriter, r *http.Request) {
				id := chi.URLParam(r, "id")
				entries, err := ext.Audit.Query(extensions.AuditFilter{Limit: 500})
				if err != nil {
					writeError(w, http.StatusInternalServerError, err.Error())
					return
				}
				for _, e := range entries {
					if e.ID == id {
						writeJSON(w, http.StatusOK, e)
						return
					}
				}
				writeError(w, http.StatusNotFound, "audit entry not found")
			})
		}

		// Enterprise: License info
		if ext != nil && ext.License != nil {
			r.Get("/license", func(w http.ResponseWriter, r *http.Request) {
				info, err := ext.License.Validate()
				if err != nil {
					writeError(w, http.StatusInternalServerError, err.Error())
					return
				}
				writeJSON(w, http.StatusOK, info)
			})
		}

		// Platform features (enterprise: orgs, admin, tickets, announcements)
		if ext != nil && ext.Platform != nil && ext.Platform.Enabled() {
			ext.Platform.RegisterRoutes(r, s, userStore)
		}

		// Team features (enterprise: workspaces, roles, permissions, RBAC)
		if ext != nil && ext.Team != nil && ext.Team.Enabled() {
			ext.Team.RegisterRoutes(r, s)
		}
	})
}

func systemInfo(s store.Store, e *engine.Engine) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		size, _ := s.GetDBSize()
		pipelines, _ := s.ListPipelines()
		active, maxC := e.GetQueueInfo()
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"version":             "0.1.0-dev",
			"db_size":             size,
			"db_size_mb":          fmt.Sprintf("%.2f MB", float64(size)/1024/1024),
			"pipelines":           len(pipelines),
			"active_runs":         active,
			"max_concurrent_runs": maxC,
		})
	}
}

// isWritePermission returns true for permissions that modify data.
// Read-only permissions that viewers can access return false.
func isWritePermission(perm string) bool {
	readPerms := map[string]bool{
		"pipelines.view":   true,
		"runs.view":        true,
		"connections.view": true,
		"variables.view":   true,
		"settings.view":    true,
	}
	return !readPerms[perm]
}

func systemPurge(s store.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Days int `json:"days"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Days <= 0 {
			req.Days = 30
		}

		// Scope purge by org if available (multi-tenant isolation)
		orgID := GetOrgIDFromRequest(r)
		if orgID != "" && orgID != "default" {
			deleted, err := s.PurgeRunsOlderThanByOrg(req.Days, orgID)
			if err != nil {
				writeError(w, http.StatusInternalServerError, err.Error())
				return
			}
			writeJSON(w, http.StatusOK, map[string]interface{}{
				"deleted": deleted,
				"days":    req.Days,
				"org_id":  orgID,
			})
			return
		}

		// Community edition / default org — purge all (single tenant)
		deleted, err := s.PurgeRunsOlderThan(req.Days)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"deleted": deleted,
			"days":    req.Days,
		})
	}
}
