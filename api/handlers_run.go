package api

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"time"

	"github.com/Tnsor-Labs/brokoli/engine"
	"github.com/Tnsor-Labs/brokoli/models"
	"github.com/Tnsor-Labs/brokoli/store"
	"github.com/go-chi/chi/v5"

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

	// Parse optional params from request body. Params is the legacy untyped
	// override map; Parameters is the typed run-parameter payload validated
	// against the pipeline's declarations (ADR-032 section 3).
	var req struct {
		Params     map[string]string      `json:"params"`
		Parameters map[string]interface{} `json:"parameters"`
	}
	json.NewDecoder(r.Body).Decode(&req) // ignore error — body may be empty

	// Async: return immediately with run ID. Pipeline executes in background.
	// This prevents client timeouts from creating duplicate runs.
	runID, err := h.engine.RunPipelineAsyncWithParameters(pipelineID, req.Params, req.Parameters)
	if err != nil {
		if errors.Is(err, engine.ErrParameterResolution) {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
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

// defaultRunPageSize is what a caller gets when it asks for a listing
// without saying how much it wants. It matches the cap this endpoint used
// to hard-code, so an existing unparameterised caller sees no change.
const defaultRunPageSize = 50

// maxRunPageSize bounds what a caller can ask for in one request, so a
// pipeline with a very long history cannot be turned into an unbounded
// response by a single query parameter.
const maxRunPageSize = 500

// runPageSize reads ?limit=, falling back to the default and clamping to
// the maximum.
func runPageSize(r *http.Request) int {
	n, err := strconv.Atoi(r.URL.Query().Get("limit"))
	if err != nil || n <= 0 {
		return defaultRunPageSize
	}
	if n > maxRunPageSize {
		return maxRunPageSize
	}
	return n
}

// ListByPipeline returns a page of a pipeline's run history.
//
// Three response shapes, chosen by query parameter:
//
//	(none)            a plain array, the newest defaultRunPageSize runs
//	?after= / ?limit= store.CursorResult — keyset, walks the whole history
//	?page=            the offset-paginated envelope, with a true total
//
// The bare form is unchanged so existing callers keep working. The cursor
// form is the one to use for browsing history: it walks by run ID, which is
// a UUIDv7 and therefore already in creation order, so paging costs the
// same whether it is the first page or the ten-thousandth and needs no
// COUNT (Tnsor-Labs/brokoli#79).
func (h *RunHandler) ListByPipeline(w http.ResponseWriter, r *http.Request) {
	pipelineID := chi.URLParam(r, "id")

	// Verify pipeline belongs to user's org
	if p, err := h.store.GetPipeline(pipelineID); err == nil {
		if !ValidateOrgAccess(r, p.OrgID) {
			DenyOrgAccess(w)
			return
		}
	}

	q := r.URL.Query()
	limit := runPageSize(r)

	// Offset pagination, kept for callers that page by number. It used to
	// slice an already-truncated 50 runs in memory, so its total was capped
	// at 50 and any page past the second came back empty however many runs
	// existed. Both the page and the total now come from the database.
	if q.Get("page") != "" {
		pp := ParsePageParams(r)
		runs, total, err := h.store.ListRunsByPipelinePaged(pipelineID, pp.Limit(), pp.Offset())
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		runs = populateRunErrors(runs)
		writeJSON(w, http.StatusOK, PaginateSlice(runs, total, pp))
		return
	}

	if HasCursorPagination(r) {
		runs, hasNext, err := h.store.ListRunsByPipelineCursor(pipelineID, CursorParam(r), limit)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		runs = populateRunErrors(runs)
		cursor := ""
		if hasNext && len(runs) > 0 {
			cursor = runs[len(runs)-1].ID
		}
		writeJSON(w, http.StatusOK, store.CursorResult{
			Items:   runs,
			HasNext: hasNext,
			Cursor:  cursor,
			Limit:   limit,
		})
		return
	}

	runs, err := h.store.ListRunsByPipeline(pipelineID, limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, populateRunErrors(runs))
}

// populateRunErrors normalises a run listing for the wire: never nil, so
// clients get [] rather than null, each run's structured error filled
// in, and connection strings/filesystem paths scrubbed from the message
// the same way Get already does for a single run -- a listing is not a
// narrower audience than the single-run endpoint, so it shouldn't be a
// wider disclosure surface.
func populateRunErrors(runs []models.Run) []models.Run {
	if runs == nil {
		return []models.Run{}
	}
	for i := range runs {
		runs[i].PopulateError()
		runs[i].Error = sanitizeRunError(runs[i].Error)
	}
	return runs
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

// GetPlan returns the physical execution plan that was persisted for a
// run (ADR-015, #90 M2) — the plan as-decided at run time, for audit and
// recovery. Distinct from the pipeline-level GET /pipelines/{id}/plan,
// which recomputes the plan for the current definition.
func (h *RunHandler) GetPlan(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	run, err := h.store.GetRun(id)
	if err != nil {
		writeError(w, http.StatusNotFound, "run not found")
		return
	}
	if p, err := h.store.GetPipeline(run.PipelineID); err == nil {
		if !ValidateOrgAccess(r, p.OrgID) {
			DenyOrgAccess(w)
			return
		}
	}
	pp, ok := h.store.(store.PhysicalPlanStore)
	if !ok {
		writeError(w, http.StatusNotImplemented, "this server does not persist physical plans")
		return
	}
	planJSON, err := pp.GetRunPlan(id)
	if err != nil {
		writeError(w, http.StatusNotFound, "no physical plan recorded for this run")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	// #nosec G705 -- planJSON is the planner's own JSON snapshot read from
	// the store, not request-controlled input.
	_, _ = w.Write([]byte(planJSON))
}

// GetInstances returns the physical instances that executed in a run —
// the runtime counterpart to the pipeline's planned work units (ADR-015
// §8, #90). Projected from the run's existing records: one instance per
// plain node, one per resolved expansion item.
func (h *RunHandler) GetInstances(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	run, err := h.store.GetRun(id)
	if err != nil {
		writeError(w, http.StatusNotFound, "run not found")
		return
	}
	if p, err := h.store.GetPipeline(run.PipelineID); err == nil {
		if !ValidateOrgAccess(r, p.OrgID) {
			DenyOrgAccess(w)
			return
		}
	}
	// Prefer the durable per-instance record (#90 M3); fall back to
	// projecting from node_runs for runs that predate it or stores
	// without the capability.
	if pi, ok := h.store.(store.PhysicalInstanceStore); ok {
		if durable, err := pi.ListPhysicalInstances(id); err == nil && len(durable) > 0 {
			writeJSON(w, http.StatusOK, durable)
			return
		}
	}
	instances, err := engine.ProjectRunInstances(h.store, id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, instances)
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
	writeJSON(w, http.StatusOK, filterLogs(logs, r))
}

// GetEvents returns a run's durable lifecycle event log in chronological
// order. The run_events table has been written since the crash-recovery
// work but was never readable from outside the process — this is the
// difference between "the run failed" and "it was retried twice with
// backoff, reclaimed after a crash, then failed terminally", which is what
// someone debugging actually needs (Tnsor-Labs/brokoli#74).
func (h *RunHandler) GetEvents(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if !h.validateRunAccess(r, id) {
		DenyOrgAccess(w)
		return
	}
	events, err := h.store.ListEventsByRun(id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if events == nil {
		events = []models.RunEvent{}
	}
	writeJSON(w, http.StatusOK, events)
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

	filtered := filterLogs(logs, r)
	w.Header().Set("Content-Type", "text/plain")
	w.Header().Set("Content-Disposition", "attachment; filename=run-"+id[:8]+"-logs.txt")
	for _, l := range filtered {
		line := l.Timestamp.Format("2006-01-02T15:04:05Z") + " [" + string(l.Level) + "]"
		if l.NodeID != "" {
			line += " [" + l.NodeID + "]"
		}
		line += " " + l.Message + "\n"
		w.Write([]byte(line))
	}
}

// filterLogs applies ?node_id= and ?level= query filters to a log slice.
// Input validation: level is checked against the known enum; node_id is an
// opaque string filter (the store already scoped logs to the run, so the
// node_id cannot leak data from another run/pipeline).
func filterLogs(logs []models.LogEntry, r *http.Request) []models.LogEntry {
	nodeID := r.URL.Query().Get("node_id")
	level := models.LogLevel(r.URL.Query().Get("level"))
	if nodeID == "" && level == "" {
		return logs
	}
	if level != "" && !isValidLogLevel(level) {
		return logs
	}
	out := make([]models.LogEntry, 0, len(logs))
	for _, l := range logs {
		if nodeID != "" && l.NodeID != nodeID {
			continue
		}
		if level != "" && l.Level != level {
			continue
		}
		out = append(out, l)
	}
	return out
}

func isValidLogLevel(l models.LogLevel) bool {
	switch l {
	case models.LogLevelDebug, models.LogLevelInfo, models.LogLevelWarning, models.LogLevelError:
		return true
	}
	return false
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

	// Verify pipeline belongs to user's org -- Backfill actually executes
	// runs (h.engine.Backfill below), so this is not just a read-scoping
	// check: without it, a caller in a different org could trigger real
	// execution against this pipeline's live connections.
	if p, err := h.store.GetPipeline(pipelineID); err == nil {
		if !ValidateOrgAccess(r, p.OrgID) {
			DenyOrgAccess(w)
			return
		}
	}

	// Interval-native since ADR-028: start/end as RFC3339, enumerated
	// over the pipeline's own schedule. The pre-ADR date-only fields are
	// still accepted -- a bare date means midnight UTC, and end_date is
	// the inclusive day it always was.
	var req struct {
		Start     string `json:"start"`
		End       string `json:"end"`
		StartDate string `json:"start_date"`
		EndDate   string `json:"end_date"`
		Force     bool   `json:"force"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	parse := func(rfc, date string, endOfDay bool) (time.Time, error) {
		if rfc != "" {
			return time.Parse(time.RFC3339, rfc)
		}
		if date == "" {
			return time.Time{}, fmt.Errorf("start/end (RFC3339) or start_date/end_date (YYYY-MM-DD) required")
		}
		t, err := time.Parse("2006-01-02", date)
		if err == nil && endOfDay {
			t = t.AddDate(0, 0, 1)
		}
		return t, err
	}
	start, err := parse(req.Start, req.StartDate, false)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	end, err := parse(req.End, req.EndDate, true)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	plan, err := h.engine.Backfill(pipelineID, engine.BackfillRequest{Start: start, End: end, Force: req.Force})
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	AuditLog(r, "backfill", "pipeline", pipelineID, nil, map[string]interface{}{
		"intervals": plan.Intervals, "start": plan.First, "end": plan.Last,
	})
	writeJSON(w, http.StatusAccepted, plan)
}

func (h *RunHandler) DryRun(w http.ResponseWriter, r *http.Request) {
	pipelineID := chi.URLParam(r, "id")
	pipe, err := h.store.GetPipeline(pipelineID)
	if err != nil {
		writeError(w, http.StatusNotFound, "pipeline not found")
		return
	}
	// DryRun actually executes the pipeline for real (up to 10 rows,
	// unpersisted) -- same reasoning as Backfill above, not just a read
	// check.
	if !ValidateOrgAccess(r, pipe.OrgID) {
		DenyOrgAccess(w)
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

	// A real query, not only a ping (ADR-027's catalog rule). Ping proves
	// a handshake; SELECT 1 proves the server executes SQL for this user
	// against this database, which is what a green test result claims.
	var one int
	if err := db.QueryRow("SELECT 1").Scan(&one); err != nil {
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
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"profile": json.RawMessage(profile),
		"schema":  json.RawMessage(schema),
		"drift":   json.RawMessage(drift),
	})
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

	// Verify pipeline belongs to user's org
	if p, err := h.store.GetPipeline(pipelineID); err == nil {
		if !ValidateOrgAccess(r, p.OrgID) {
			DenyOrgAccess(w)
			return
		}
	}

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
