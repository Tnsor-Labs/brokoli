package api

import (
	"encoding/json"
	"net/http"

	"github.com/Tnsor-Labs/brokoli/models"
	"github.com/go-chi/chi/v5"
)

// What a run actually did (ADR-039 part 4).
//
// The lineage graph describes the pipeline as it is defined now; this
// describes one execution. The two differ whenever a pipeline has been
// edited since, which is exactly the situation somebody is in when they
// ask why last Tuesday's number is wrong.

// RunProvenanceResponse is the wire shape.
type RunProvenanceResponse struct {
	RunID string                  `json:"run_id"`
	Nodes []models.NodeProvenance `json:"nodes"`
	// Recorded says whether this run has any provenance at all.
	//
	// Explicit, because an empty list has two meanings a client must not
	// conflate: a run from before this existed, and a run that recorded
	// nothing. The first is the common case for a while yet.
	Recorded bool `json:"recorded"`
}

// GetProvenance returns each node's execution record for one run.
func (h *RunHandler) GetProvenance(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")

	// validateRunAccess rather than the looser inline check some
	// neighbours use: it fails closed when the run or its pipeline
	// cannot be resolved, and provenance names the data a run touched.
	if !h.validateRunAccess(r, id) {
		// Not found rather than forbidden. Distinguishing them tells a
		// caller whether a run id exists in another organization, which
		// is itself information they should not have.
		writeError(w, http.StatusNotFound, "run not found")
		return
	}

	provStore, ok := h.store.(interface {
		GetRunProvenance(runID string) ([]models.NodeProvenance, error)
	})
	if !ok {
		writeError(w, http.StatusNotImplemented, "this server does not record provenance")
		return
	}

	nodes, err := provStore.GetRunProvenance(id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not read provenance")
		return
	}
	if nodes == nil {
		nodes = []models.NodeProvenance{}
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(RunProvenanceResponse{
		RunID: id, Nodes: nodes, Recorded: len(nodes) > 0,
	})
}
