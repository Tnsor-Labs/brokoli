package api

import (
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/hc12r/broked/crypto"
	"github.com/hc12r/broked/engine"
	"github.com/hc12r/broked/store"
)

// RegisterRoutes sets up all API routes on the given router.
func RegisterRoutes(r chi.Router, s store.Store, e *engine.Engine, hub *Hub, sched *engine.Scheduler, cryptoCfg ...*crypto.Config) {
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

	r.Route("/api", func(r chi.Router) {
		// Pipelines
		r.Get("/pipelines", ph.List)
		r.Post("/pipelines", ph.Create)
		r.Get("/pipelines/{id}", ph.Get)
		r.Put("/pipelines/{id}", ph.Update)
		r.Delete("/pipelines/{id}", ph.Delete)
		r.Get("/pipelines/{id}/export", ph.Export)
		r.Get("/pipelines/{id}/validate", ph.Validate)
		r.Get("/pipelines/{id}/versions", ph.ListVersions)
		r.Post("/pipelines/{id}/rollback", ph.Rollback)
		r.Post("/pipelines/{id}/clone", ph.Clone)
		r.Post("/pipelines/{id}/validate-nodes", ph.ValidateNodes)
		r.Post("/pipelines/import", ph.Import)

		// Runs
		r.Post("/pipelines/{id}/run", rh.TriggerRun)
		r.Post("/pipelines/{id}/dry-run", rh.DryRun)
		r.Post("/pipelines/{id}/backfill", rh.Backfill)
		r.Get("/pipelines/{id}/runs", rh.ListByPipeline)
		r.Get("/runs/{id}", rh.Get)
		r.Post("/runs/{id}/resume", rh.ResumeRun)
		r.Get("/runs/{id}/logs", rh.GetLogs)
		r.Get("/runs/{id}/logs/export", rh.ExportLogs)
		r.Get("/runs/{id}/nodes/{nodeId}/preview", rh.GetNodePreview)

		// Connections
		r.Get("/connections", ch.List)
		r.Post("/connections", ch.Create)
		r.Get("/connections/{connId}", ch.Get)
		r.Put("/connections/{connId}", ch.Update)
		r.Delete("/connections/{connId}", ch.Delete)
		r.Post("/connections/{connId}/test", ch.Test)
		r.Get("/connection-types", ConnectionTypes)

		// Calendar
		r.Get("/runs/calendar", func(w http.ResponseWriter, r *http.Request) {
			days := 90
			if d := r.URL.Query().Get("days"); d != "" {
				fmt.Sscanf(d, "%d", &days)
			}
			if days < 1 || days > 365 {
				days = 90
			}
			cal, err := s.GetRunCalendar(days)
			if err != nil {
				writeError(w, http.StatusInternalServerError, err.Error())
				return
			}
			if cal == nil {
				cal = []store.CalendarDay{}
			}
			writeJSON(w, http.StatusOK, cal)
		})

		// Bulk pipeline operations
		r.Post("/pipelines/bulk", func(w http.ResponseWriter, r *http.Request) {
			var req struct {
				IDs    []string `json:"ids"`
				Action string   `json:"action"` // delete, enable, disable
			}
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				writeError(w, http.StatusBadRequest, "invalid JSON")
				return
			}
			count := 0
			for _, id := range req.IDs {
				switch req.Action {
				case "delete":
					if err := s.DeletePipeline(id); err == nil {
						count++
					}
				case "enable", "disable":
					if p, err := s.GetPipeline(id); err == nil {
						p.Enabled = req.Action == "enable"
						if s.UpdatePipeline(p) == nil {
							count++
						}
					}
				}
			}
			writeJSON(w, http.StatusOK, map[string]interface{}{
				"affected": count,
				"action":   req.Action,
			})
		})

		// Variables
		r.Get("/variables", vh.List)
		r.Post("/variables", vh.Set)
		r.Get("/variables/{key}", vh.Get)
		r.Put("/variables/{key}", vh.Set)
		r.Delete("/variables/{key}", vh.Delete)

		// Lineage
		r.Get("/lineage", func(w http.ResponseWriter, r *http.Request) {
			pipelines, err := s.ListPipelines()
			if err != nil {
				writeError(w, http.StatusInternalServerError, err.Error())
				return
			}
			graph := engine.BuildLineageGraph(pipelines)
			writeJSON(w, http.StatusOK, graph)
		})

		// Scheduler
		r.Get("/scheduler/status", func(w http.ResponseWriter, r *http.Request) {
			if sched == nil {
				writeJSON(w, http.StatusOK, []engine.ScheduleInfo{})
				return
			}
			status := sched.Status()
			if status == nil {
				status = []engine.ScheduleInfo{}
			}
			writeJSON(w, http.StatusOK, status)
		})

		// Utilities
		r.Post("/test-connection", rh.TestConnection)
		r.Get("/system/info", systemInfo(s, e))
		r.Post("/system/purge", systemPurge(s))

		// WebSocket
		r.Get("/ws", hub.HandleWS)
	})
}

func systemInfo(s store.Store, e *engine.Engine) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		size, _ := s.GetDBSize()
		pipelines, _ := s.ListPipelines()
		active, maxC := e.GetQueueInfo()
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"version":            "0.1.0-dev",
			"db_size":            size,
			"db_size_mb":         fmt.Sprintf("%.2f MB", float64(size)/1024/1024),
			"pipelines":          len(pipelines),
			"active_runs":        active,
			"max_concurrent_runs": maxC,
		})
	}
}

func systemPurge(s store.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Days int `json:"days"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Days <= 0 {
			req.Days = 30
		}
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
