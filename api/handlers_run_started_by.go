package api

import (
	"net/http"
	"strconv"

	"github.com/Tnsor-Labs/brokoli/models"
)

// GET /api/runs?started_by=me (#241).

const startedByDefaultLimit = 20
const startedByMaxLimit = 200

// ListStartedBy returns the caller's own recent runs across pipelines.
//
// started_by is required and accepts only "me" today. A user id would be
// a different feature -- reading whose runs somebody else started is an
// audit question with its own access rule -- and accepting one here
// without that rule would let any member enumerate another's activity.
// An unsupported value is refused rather than quietly treated as "me",
// which would answer a question that was not asked.
func (h *RunHandler) ListStartedBy(w http.ResponseWriter, r *http.Request) {
	startedBy := r.URL.Query().Get("started_by")
	if startedBy == "" {
		writeError(w, http.StatusBadRequest, "started_by is required; the only supported value is \"me\"")
		return
	}
	if startedBy != "me" {
		writeError(w, http.StatusBadRequest, "started_by only supports \"me\"")
		return
	}

	attribution := runAttributionFromRequest(r)
	if attribution == nil || attribution.UserID == "" {
		writeError(w, http.StatusUnauthorized, "authentication required")
		return
	}

	limit := startedByDefaultLimit
	if v := r.URL.Query().Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > startedByMaxLimit {
			writeError(w, http.StatusBadRequest,
				"limit must be a whole number between 1 and 200")
			return
		}
		limit = n
	}

	// Read more ids than asked for, because the org filter below removes
	// some. Without the headroom a caller whose recent runs are mostly in
	// another organization gets a short page and no way to tell it from
	// having no more runs.
	ids, err := h.store.ListRunIDsStartedBy(attribution.UserID, limit*2)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list runs")
		return
	}

	runs := make([]models.Run, 0, limit)
	for _, id := range ids {
		if len(runs) == limit {
			break
		}
		run, err := h.store.GetRun(id)
		if err != nil {
			// A run that has been purged since its attribution was written.
			// Skipping is right: the row is a record of something that no
			// longer exists.
			continue
		}
		// The same tenant check every other run route makes. Attribution
		// is keyed by user, and a user can belong to more than one
		// organization, so their own runs elsewhere must not appear here.
		p, err := h.store.GetPipeline(run.PipelineID)
		if err != nil || !ValidateOrgAccess(r, p.OrgID) {
			continue
		}
		runs = append(runs, *run)
	}
	writeJSON(w, http.StatusOK, h.decorateRuns(runs))
}
