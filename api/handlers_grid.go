package api

import (
	"net/http"
	"strconv"
	"time"

	"github.com/Tnsor-Labs/brokoli/engine"
	"github.com/Tnsor-Labs/brokoli/models"
	"github.com/go-chi/chi/v5"
)

// The grid view (#400): runs (columns, newest last) x nodes (rows, in the
// planner's own dependency order), each cell the LATEST attempt of that
// node in that run. The question it answers is the one an operator
// actually asks -- "how has this pipeline behaved lately" -- which neither
// the one-run-per-row list nor the per-day calendar can: a red row is a
// broken node, a red column is a bad day, a diagonal is a deploy.

type gridCell struct {
	Status     models.RunStatus `json:"status"`
	Attempt    int              `json:"attempt"`
	DurationMs int64            `json:"duration_ms"`
	RowCount   int              `json:"row_count"`
	Error      string           `json:"error,omitempty"`
}

type gridRun struct {
	ID                string           `json:"id"`
	Status            models.RunStatus `json:"status"`
	StartedAt         *time.Time       `json:"started_at"`
	Trigger           string           `json:"trigger,omitempty"`
	DataIntervalStart *time.Time       `json:"data_interval_start,omitempty"`
	DataIntervalEnd   *time.Time       `json:"data_interval_end,omitempty"`
	PipelineVersion   int              `json:"pipeline_version"`
}

type gridNode struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Type string `json:"type"`
}

type gridResponse struct {
	Nodes []gridNode                     `json:"nodes"`
	Runs  []gridRun                      `json:"runs"`
	Cells map[string]map[string]gridCell `json:"cells"` // run ID -> node ID -> cell
}

// Grid serves GET /api/pipelines/{id}/grid?runs=N.
func (h *RunHandler) Grid(w http.ResponseWriter, r *http.Request) {
	pipelineID := chi.URLParam(r, "id")
	pipe, err := h.store.GetPipeline(pipelineID)
	if err != nil {
		writeError(w, http.StatusNotFound, "pipeline not found")
		return
	}
	if !ValidateOrgAccess(r, pipe.OrgID) {
		DenyOrgAccess(w)
		return
	}

	limit := 30
	if v := r.URL.Query().Get("runs"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 100 {
			limit = n
		}
	}

	// Newest-first from the keyset walk (#79's index), reversed so the
	// grid reads left-to-right in time like every chart does.
	runs, _, err := h.store.ListRunsByPipelineCursor(pipelineID, "", limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "list runs: "+err.Error())
		return
	}
	for i, j := 0, len(runs)-1; i < j; i, j = i+1, j-1 {
		runs[i], runs[j] = runs[j], runs[i]
	}

	runIDs := make([]string, len(runs))
	gridRuns := make([]gridRun, len(runs))
	for i, run := range runs {
		runIDs[i] = run.ID
		gridRuns[i] = gridRun{
			ID: run.ID, Status: run.Status, StartedAt: run.StartedAt,
			Trigger:           run.Trigger,
			DataIntervalStart: run.DataIntervalStart,
			DataIntervalEnd:   run.DataIntervalEnd,
			PipelineVersion:   run.PipelineVersion,
		}
	}

	nodeRuns, err := h.store.GridNodeRuns(runIDs)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "grid node runs: "+err.Error())
		return
	}
	cells := make(map[string]map[string]gridCell, len(runIDs))
	for runID, nrs := range nodeRuns {
		row := make(map[string]gridCell, len(nrs))
		for _, nr := range nrs {
			row[nr.NodeID] = gridCell{
				Status: nr.Status, Attempt: nr.Attempt,
				DurationMs: nr.DurationMs, RowCount: nr.RowCount, Error: nr.Error,
			}
		}
		cells[runID] = row
	}

	// Rows are the CURRENT pipeline's nodes in dependency order. Cells
	// for nodes that have since been removed from the DAG are simply not
	// rendered -- run history pins its version, but the first cut of the
	// grid reads against the pipeline as it is now.
	ordered := engine.TopoOrderedNodes(pipe.Nodes, pipe.Edges)
	nodes := make([]gridNode, len(ordered))
	for i, n := range ordered {
		nodes[i] = gridNode{ID: n.ID, Name: n.Name, Type: string(n.Type)}
	}

	writeJSON(w, http.StatusOK, gridResponse{Nodes: nodes, Runs: gridRuns, Cells: cells})
}
