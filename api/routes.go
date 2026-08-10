package api

import (
	"encoding/json"
	"log"
	"net/http"

	"github.com/Tnsor-Labs/brokoli/crypto"
	"github.com/Tnsor-Labs/brokoli/engine"
	"github.com/Tnsor-Labs/brokoli/extensions"
	"github.com/Tnsor-Labs/brokoli/models"
	"github.com/Tnsor-Labs/brokoli/pkg/plugins"
	"github.com/Tnsor-Labs/brokoli/pkg/sodp"
	"github.com/Tnsor-Labs/brokoli/store"
	"github.com/go-chi/chi/v5"
	"github.com/golang-jwt/jwt/v5"
)

// RegisterRoutes sets up all API routes on the given router.
func RegisterRoutes(r chi.Router, s store.Store, e *engine.Engine, ws *sodp.Server, sched *engine.Scheduler, ext *extensions.Registry, userStore *UserStore, cryptoCfg ...*crypto.Config) {
	var executors []extensions.NodeExecutor
	if e != nil {
		executors = e.Executors
	}
	ph := NewPipelineHandler(s, sched, executors...)
	rh := NewRunHandler(s, e)

	// The plugin manager is one of the engine's executors; find the
	// concrete type so the plugin API can install/remove/serve against
	// the same instance the engine resolves node types through (that
	// shared instance is what makes hot reload work).
	var pluginMgr *plugins.Manager
	for _, ex := range executors {
		if pm, ok := ex.(*plugins.Manager); ok {
			pluginMgr = pm
			break
		}
	}
	plugh := NewPluginHandler(pluginMgr)

	// Connection handler (crypto config optional for backward compat)
	var cc *crypto.Config
	if len(cryptoCfg) > 0 && cryptoCfg[0] != nil {
		cc = cryptoCfg[0]
	} else {
		cc = &crypto.Config{Key: make([]byte, 32)} // zero key fallback
	}
	ch := NewConnectionHandler(s, cc)
	vh := NewVariableHandler(s, cc)
	th := NewTemplateHandler(s)
	ah := NewAlertHandler(s)

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
		// Sample data for the built-in pipeline templates (public, no auth
		// required — see AuthMiddleware/WorkspaceMiddleware's "/api/samples/"
		// exemption).
		r.Get("/samples/data/{file}", samplesDataHandler())

		// Pipeline templates offered at pipeline-creation time
		// (store.Store's pipeline_templates table, seeded from
		// pkg/templates.Builtin). Listing requires no permission; curation
		// is admin-only (requireAdmin, not requirePerm — see its doc
		// comment for why) since templates are global, not per-org.
		r.Get("/templates", th.List)
		r.With(requireAdmin).Post("/templates", th.Create)
		r.With(requireAdmin).Put("/templates/{id}", th.Update)
		r.With(requireAdmin).Delete("/templates/{id}", th.Delete)

		// Pipelines
		r.Get("/pipelines", ph.List)
		r.With(requirePerm(models.PermPipelinesCreate)).Post("/pipelines", ph.Create)
		r.Get("/pipelines/{id}", ph.Get)
		r.With(requirePerm(models.PermPipelinesEdit)).Put("/pipelines/{id}", ph.Update)
		r.With(requirePerm(models.PermPipelinesDelete)).Delete("/pipelines/{id}", ph.Delete)
		r.With(requirePerm(models.PermPipelinesExport)).Get("/pipelines/{id}/export", ph.Export)
		r.Get("/pipelines/{id}/validate", ph.Validate)
		// Stateless: validates a request-body IR document without
		// persisting anything (#109 M2). Auth-only, like its by-id
		// sibling — validation reads no tenant data.
		r.Post("/pipelines/validate", ph.ValidateDocument)
		r.Get("/pipelines/{id}/versions", ph.ListVersions)
		r.With(requirePerm(models.PermPipelinesEdit)).Post("/pipelines/{id}/rollback", ph.Rollback)
		r.With(requirePerm(models.PermPipelinesEdit)).Post("/pipelines/{id}/clone", ph.Clone)
		r.Post("/pipelines/{id}/validate-nodes", ph.ValidateNodes)
		// Physical plan explanation, before execution (#90 M2).
		r.Get("/pipelines/{id}/plan", ph.Plan)

		// Plugin management (#110 M2). Install runs plugin code on the
		// host, so create/remove need settings.edit; list and archive
		// fetch are read-only (archive fetch is how workers pull by
		// digest). Node types hot-reload on install/remove.
		r.Get("/plugins", plugh.List)
		r.With(requirePerm(models.PermSettingsEdit)).Post("/plugins", plugh.Install)
		r.With(requirePerm(models.PermSettingsEdit)).Delete("/plugins/{name}", plugh.Remove)
		r.Get("/plugins/{name}/archive", plugh.Archive)
		r.With(requirePerm(models.PermPipelinesCreate)).Post("/pipelines/import", ph.Import)

		// Runs
		r.With(requirePerm(models.PermPipelinesRun)).Post("/pipelines/{id}/run", rh.TriggerRun)
		r.With(requirePerm(models.PermPipelinesRun)).Post("/pipelines/{id}/dry-run", rh.DryRun)
		r.With(requirePerm(models.PermPipelinesRun)).Post("/pipelines/{id}/backfill", rh.Backfill)
		r.Get("/pipelines/{id}/runs", rh.ListByPipeline)
		r.Get("/pipelines/{id}/node-stats", rh.NodeStats)
		r.Get("/runs/{id}", rh.Get)
		r.With(requirePerm(models.PermRunsResume)).Post("/runs/{id}/resume", rh.ResumeRun)
		r.With(requirePerm(models.PermRunsCancel)).Post("/runs/{id}/cancel", rh.CancelRun)
		r.Get("/runs/{id}/logs", rh.GetLogs)
		// Physical plan as persisted for this run (#90 M2, ADR-015).
		r.Get("/runs/{id}/plan", rh.GetPlan)
		// Physical instances that executed in this run (#90 M2, ADR-015).
		r.Get("/runs/{id}/instances", rh.GetInstances)
		r.Get("/runs/{id}/logs/export", rh.ExportLogs)

		// Durable run lifecycle event log
		r.Get("/runs/{id}/events", rh.GetEvents)

		// Alert inbox — persisted, readable notifications. Listing and
		// mutating are org-scoped inside the handler; no extra permission
		// is required to read or dismiss your own org's alerts.
		r.Get("/alerts", ah.List)
		r.Post("/alerts/{id}/read", ah.MarkRead)
		r.Post("/alerts/read-all", ah.MarkAllRead)
		r.Delete("/alerts/{id}", ah.Dismiss)

		// Dead letter queue across every pipeline in the org
		r.Get("/dlq", ah.ListDLQ)

		// Node profiles
		r.Get("/runs/{id}/nodes/{nodeId}/profile", rh.GetNodeProfile)

		// Dead Letter Queue
		r.Get("/pipelines/{id}/dlq", dlqListHandler(s))
		r.Post("/pipelines/{id}/dlq/{dlqId}/resolve", dlqResolveHandler(s))

		// Webhook trigger
		r.Post("/pipelines/{id}/webhook", webhookTriggerHandler(s, e))

		// Pipeline dependencies status + graph
		r.Get("/pipelines/{id}/deps", pipelineDepsHandler(s))
		r.Get("/pipelines/{id}/dependents", pipelineDependentsHandler(s))
		r.Get("/pipelines/dependency-graph", pipelineDependencyGraphHandler(s))
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
		r.Get("/capabilities", CapabilitiesHandler)
		r.With(requirePerm(models.PermSettingsEdit)).Post("/system/purge", systemPurge(s, e))

		// WebSocket (SODP binary protocol)
		r.Get("/ws", ws.HandleWS)

		// Impact analysis (stub — returns downstream dependencies)
		r.Get("/pipelines/{id}/impact", func(w http.ResponseWriter, r *http.Request) {
			_ = chi.URLParam(r, "id")
			writeJSON(w, http.StatusOK, map[string]interface{}{
				"downstream": []interface{}{},
			})
		})

		// Enterprise: Git Sync API (requires "gitsync" feature)
		if ext != nil && ext.GitSync != nil {
			r.With(RequireFeature("gitsync")).Get("/git/config", func(w http.ResponseWriter, r *http.Request) {
				writeJSON(w, http.StatusOK, ext.GitSync.Config())
			})
			r.With(RequireFeature("gitsync")).Get("/git/status", func(w http.ResponseWriter, r *http.Request) {
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

		// Enterprise: Audit log API (requires "audit" feature)
		if ext != nil && ext.Audit != nil {
			r.With(RequireFeature("audit")).Get("/audit", func(w http.ResponseWriter, r *http.Request) {
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
			ext.Platform.RegisterRoutes(r, s, userStore, e)
		}

		// Team features (enterprise: workspaces, roles, permissions, RBAC)
		if ext != nil && ext.Team != nil && ext.Team.Enabled() {
			ext.Team.RegisterRoutes(r, s)
		}
	})
}

func systemInfo(s store.Store, e *engine.Engine) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		active, maxC := e.GetQueueInfo()
		writeJSON(w, http.StatusOK, map[string]interface{}{
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

// requireAdmin gates a route to the "admin"/"superadmin" role only,
// matching /api/auth/admin-reset-password's existing inline check.
// Needed in addition to requirePerm(models.PermTemplatesManage):
// requirePerm's open-source fallback only blocks the "viewer" role from
// write permissions (see isWritePermission above) — it does not enforce
// true role-exclusivity in community mode, only the enterprise Team
// extension's real RBAC does that. Template curation is meant to be
// admin-only in every edition, so it needs its own explicit check
// rather than relying on requirePerm alone.
func requireAdmin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		claims, _ := r.Context().Value("claims").(*jwt.MapClaims)
		if claims == nil {
			writeError(w, http.StatusUnauthorized, "authentication required")
			return
		}
		role, _ := (*claims)["role"].(string)
		if role != "admin" && role != "superadmin" {
			writeError(w, http.StatusForbidden, "admin role required")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func systemPurge(s store.Store, e *engine.Engine) http.HandlerFunc {
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
			runIDs, err := s.ListRunIDsOlderThanByOrg(req.Days, orgID)
			if err != nil {
				writeError(w, http.StatusInternalServerError, err.Error())
				return
			}
			deleted, err := s.PurgeRunsOlderThanByOrg(req.Days, orgID)
			if err != nil {
				writeError(w, http.StatusInternalServerError, err.Error())
				return
			}
			purgeRunFiles(e, runIDs)
			writeJSON(w, http.StatusOK, map[string]interface{}{
				"deleted": deleted,
				"days":    req.Days,
				"org_id":  orgID,
			})
			return
		}

		// Community edition / default org — purge all (single tenant)
		runIDs, err := s.ListRunIDsOlderThan(req.Days)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		deleted, err := s.PurgeRunsOlderThan(req.Days)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		purgeRunFiles(e, runIDs)
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"deleted": deleted,
			"days":    req.Days,
		})
	}
}

// purgeRunFiles removes each run's local-disk artifacts and pagination
// checkpoints after its DB rows are gone (Tnsor-Labs/brokoli#49) — neither
// store keeps track of which runs exist on its own, so system/purge is
// what tells them a run is gone. A per-run deletion failure is logged, not
// fatal to the purge: the DB rows are already gone either way, and this
// only means some disk usage lingers until the next purge retries the same
// (now already-DB-purged) run ID — same "log, don't fail" policy
// ArtifactStore.WriteArtifact's callers already use for storage-adjacent
// side effects. e is nil-safe (both stores default to non-nil in
// engine.NewEngine, but a test or embedder could still construct an Engine
// without them).
func purgeRunFiles(e *engine.Engine, runIDs []string) {
	if e == nil {
		return
	}
	for _, runID := range runIDs {
		if e.ArtifactStore != nil {
			if err := e.ArtifactStore.DeleteRunArtifacts(runID); err != nil {
				// runID came back from ListRunIDsOlderThan(By/Org) — a
				// server-generated run ID already in the database, never
				// raw request input — so this isn't a log-injection sink
				// despite matching gosec's generic taint pattern.
				// #nosec G706
				log.Printf("system/purge: failed to delete artifacts for run %s: %v", runID, err)
			}
		}
		if e.PaginationCheckpointStore != nil {
			if err := e.PaginationCheckpointStore.DeleteRunCheckpoints(runID); err != nil {
				// #nosec G706 -- see identical justification above.
				log.Printf("system/purge: failed to delete pagination checkpoints for run %s: %v", runID, err)
			}
		}
	}
}
