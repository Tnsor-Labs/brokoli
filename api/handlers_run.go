package api

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"sort"
	"strconv"

	"github.com/go-chi/chi/v5"
	"github.com/Tnsor-Labs/brokoli/engine"
	"github.com/Tnsor-Labs/brokoli/models"
	"github.com/Tnsor-Labs/brokoli/store"

	_ "github.com/go-sql-driver/mysql"
	_ "github.com/jackc/pgx/v5/stdlib"
)

// sanitizeRunError removes potentially sensitive information from run error messages.
func sanitizeRunError(err string) string {
	if err == "" {
		return ""
	}
	// Remove connection strings (postgres://user:pass@host, mysql://...)
	re := regexp.MustCompile(`(postgres|mysql|sqlite|mongodb|redis)://[^\s]+`)
	err = re.ReplaceAllString(err, "$1://****")
	// Remove file paths outside /data and /tmp
	pathRe := regexp.MustCompile(`/(?:home|etc|usr|var|root)/[^\s:]+`)
	err = pathRe.ReplaceAllString(err, "/****/")
	return err
}

type RunHandler struct {
	store  store.Store
	engine *engine.Engine
}

func NewRunHandler(s store.Store, e *engine.Engine) *RunHandler {
	return &RunHandler{store: s, engine: e}
}

// validateRunAccess checks that a run's pipeline belongs to the user's org.
func (h *RunHandler) validateRunAccess(r *http.Request, runID string) bool {
	run, err := h.store.GetRun(runID)
	if err != nil {
		return false
	}
	p, err := h.store.GetPipeline(run.PipelineID)
	if err != nil {
		return false
	}
	return ValidateOrgAccess(r, p.OrgID)
}

func (h *RunHandler) TriggerRun(w http.ResponseWriter, r *http.Request) {
	pipelineID := chi.URLParam(r, "id")

	// Verify pipeline belongs to user's org
	if p, err := h.store.GetPipeline(pipelineID); err == nil {
		if !ValidateOrgAccess(r, p.OrgID) {
			DenyOrgAccess(w)
			return
		}
	}

	// Parse optional params from request body
	var req struct {
		Params map[string]string `json:"params"`
	}
	json.NewDecoder(r.Body).Decode(&req) // ignore error — body may be empty

	// Async: return immediately with run ID. Pipeline executes in background.
	// This prevents client timeouts from creating duplicate runs.
	runID, err := h.engine.RunPipelineAsync(pipelineID, req.Params)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	AuditLog(r, "run", "pipeline", pipelineID, nil, map[string]interface{}{"run_id": runID})
	writeJSON(w, http.StatusAccepted, map[string]interface{}{
		"id":          runID,
		"pipeline_id": pipelineID,
		"status":      "pending",
	})
}

func (h *RunHandler) ListByPipeline(w http.ResponseWriter, r *http.Request) {
	pipelineID := chi.URLParam(r, "id")
	runs, err := h.store.ListRunsByPipeline(pipelineID, 50)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if runs == nil {
		runs = []models.Run{}
	}
	for i := range runs {
		runs[i].PopulateError()
	}

	// Paginated response when ?page= is set
	if r.URL.Query().Get("page") != "" {
		pp := ParsePageParams(r)
		total := len(runs)
		start := pp.Offset()
		end := start + pp.Limit()
		if start > total {
			start = total
		}
		if end > total {
			end = total
		}
		writeJSON(w, http.StatusOK, PaginateSlice(runs[start:end], total, pp))
		return
	}

	writeJSON(w, http.StatusOK, runs)
}

func (h *RunHandler) Get(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	run, err := h.store.GetRun(id)
	if err != nil {
		writeError(w, http.StatusNotFound, "run not found")
		return
	}
	// Verify run's pipeline belongs to user's org
	if p, err := h.store.GetPipeline(run.PipelineID); err == nil {
		if !ValidateOrgAccess(r, p.OrgID) {
			DenyOrgAccess(w)
			return
		}
	}
	run.PopulateError()
	if run.Error != "" {
		run.Error = sanitizeRunError(run.Error)
	}
	writeJSON(w, http.StatusOK, run)
}

func (h *RunHandler) GetLogs(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if !h.validateRunAccess(r, id) {
		DenyOrgAccess(w)
		return
	}
	logs, err := h.store.GetLogs(id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, logs)
}

func (h *RunHandler) ExportLogs(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if !h.validateRunAccess(r, id) {
		DenyOrgAccess(w)
		return
	}
	logs, err := h.store.GetLogs(id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	w.Header().Set("Content-Type", "text/plain")
	w.Header().Set("Content-Disposition", "attachment; filename=run-"+id[:8]+"-logs.txt")
	for _, l := range logs {
		line := l.Timestamp.Format("2006-01-02T15:04:05Z") + " [" + string(l.Level) + "]"
		if l.NodeID != "" {
			line += " [" + l.NodeID + "]"
		}
		line += " " + l.Message + "\n"
		w.Write([]byte(line))
	}
}

func (h *RunHandler) CancelRun(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if !h.validateRunAccess(r, id) {
		DenyOrgAccess(w)
		return
	}
	if err := h.engine.CancelRun(id); err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "cancelled"})
}

func (h *RunHandler) Backfill(w http.ResponseWriter, r *http.Request) {
	pipelineID := chi.URLParam(r, "id")
	var req struct {
		StartDate string `json:"start_date"`
		EndDate   string `json:"end_date"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.StartDate == "" || req.EndDate == "" {
		writeError(w, http.StatusBadRequest, "start_date and end_date required (YYYY-MM-DD)")
		return
	}

	runIDs, err := h.engine.Backfill(pipelineID, req.StartDate, req.EndDate)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"runs":  runIDs,
			"error": err.Error(),
		})
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]interface{}{
		"runs":  runIDs,
		"count": len(runIDs),
	})
}

func (h *RunHandler) DryRun(w http.ResponseWriter, r *http.Request) {
	pipelineID := chi.URLParam(r, "id")
	pipe, err := h.store.GetPipeline(pipelineID)
	if err != nil {
		writeError(w, http.StatusNotFound, "pipeline not found")
		return
	}

	results, err := h.engine.DryRun(pipe, 10)
	if err != nil {
		// Still return partial results
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"error":   err.Error(),
			"results": results,
		})
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"results": results,
	})
}

func (h *RunHandler) ResumeRun(w http.ResponseWriter, r *http.Request) {
	runID := chi.URLParam(r, "id")
	if !h.validateRunAccess(r, runID) {
		DenyOrgAccess(w)
		return
	}
	run, err := h.engine.ResumeRun(runID)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusAccepted, run)
}

func (h *RunHandler) TestConnection(w http.ResponseWriter, r *http.Request) {
	var req struct {
		URI string `json:"uri"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.URI == "" {
		writeError(w, http.StatusBadRequest, "uri is required")
		return
	}

	// Try to open and ping
	driver, dsn, err := engine.DetectDriver(req.URI)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]interface{}{"success": false, "error": err.Error()})
		return
	}

	db, err := sql.Open(driver, dsn)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]interface{}{"success": false, "error": err.Error()})
		return
	}
	defer db.Close()

	if err := db.Ping(); err != nil {
		writeJSON(w, http.StatusOK, map[string]interface{}{"success": false, "error": err.Error()})
		return
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{"success": true, "driver": driver})
}

func (h *RunHandler) GetNodeProfile(w http.ResponseWriter, r *http.Request) {
	runID := chi.URLParam(r, "id")
	if !h.validateRunAccess(r, runID) {
		DenyOrgAccess(w)
		return
	}
	nodeID := chi.URLParam(r, "nodeId")
	profile, schema, drift, err := h.store.GetNodeProfile(runID, nodeID)
	if err != nil {
		writeError(w, http.StatusNotFound, "profile not found")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	fmt.Fprintf(w, `{"profile":%s,"schema":%s,"drift":%s}`, profile, schema, drift)
}

func (h *RunHandler) GetNodePreview(w http.ResponseWriter, r *http.Request) {
	runID := chi.URLParam(r, "id")
	if !h.validateRunAccess(r, runID) {
		DenyOrgAccess(w)
		return
	}
	nodeID := chi.URLParam(r, "nodeId")

	columns, rows, err := h.store.GetNodePreview(runID, nodeID)
	if err != nil {
		writeError(w, http.StatusNotFound, "no preview available")
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"columns": columns,
		"rows":    rows,
	})
}

// NodeStats returns historical execution durations per node for sparkline charts.
// GET /api/pipelines/{id}/node-stats?runs=10
func (h *RunHandler) NodeStats(w http.ResponseWriter, r *http.Request) {
	pipelineID := chi.URLParam(r, "id")

	numRuns := 10
	if n := r.URL.Query().Get("runs"); n != "" {
		if parsed, err := strconv.Atoi(n); err == nil && parsed > 0 && parsed <= 50 {
			numRuns = parsed
		}
	}

	runs, err := h.store.ListRunsByPipeline(pipelineID, numRuns)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	type nodeStat struct {
		Durations []int64 `json:"durations"`
		Avg       int64   `json:"avg"`
		P95       int64   `json:"p95"`
	}
	nodes := make(map[string]*nodeStat)

	for _, run := range runs {
		nodeRuns, err := h.store.ListNodeRunsByRun(run.ID)
		if err != nil {
			continue
		}
		// For each node, take the final attempt's duration (highest attempt with success)
		best := make(map[string]*models.NodeRun) // nodeID → best attempt
		for i := range nodeRuns {
			nr := &nodeRuns[i]
			if nr.Status != models.RunStatusSuccess {
				continue
			}
			if prev, ok := best[nr.NodeID]; !ok || nr.Attempt > prev.Attempt {
				best[nr.NodeID] = nr
			}
		}
		for nodeID, nr := range best {
			stat, ok := nodes[nodeID]
			if !ok {
				stat = &nodeStat{}
				nodes[nodeID] = stat
			}
			stat.Durations = append(stat.Durations, nr.DurationMs)
		}
	}

	// Compute avg and p95
	for _, stat := range nodes {
		if len(stat.Durations) == 0 {
			continue
		}
		sorted := make([]int64, len(stat.Durations))
		copy(sorted, stat.Durations)
		sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
		var sum int64
		for _, d := range sorted {
			sum += d
		}
		stat.Avg = sum / int64(len(sorted))
		p95idx := int(float64(len(sorted)) * 0.95)
		if p95idx >= len(sorted) {
			p95idx = len(sorted) - 1
		}
		stat.P95 = sorted[p95idx]
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{"nodes": nodes})
}
