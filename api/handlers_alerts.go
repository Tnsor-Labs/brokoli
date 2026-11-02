package api

import (
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/Tnsor-Labs/brokoli/models"
	"github.com/Tnsor-Labs/brokoli/store"
)

// AlertHandler serves the persisted alert inbox — the readable counterpart
// to the product's outbound-only Slack/webhook notifications, which are lost
// if nobody happens to be watching when they fire.
//
// Every route goes through the request's scope (ADR-049): lists show the
// current workspace's alerts, and an alert is acted on by id only in a
// workspace the caller belongs to. The scope comes from the request
// context, never from the body or a query parameter.
type AlertHandler struct {
	store store.Store
}

func NewAlertHandler(s store.Store) *AlertHandler {
	return &AlertHandler{store: s}
}

func (h *AlertHandler) List(w http.ResponseWriter, r *http.Request) {
	orgID := GetOrgIDFromRequest(r)
	q := r.URL.Query()
	limit := boundedListLimit(q.Get("limit"))
	userID := alertCallerID(r)

	query := store.AlertQuery{
		OrgID:      orgID,
		UserID:     userID,
		UnreadOnly: q.Get("unread") == "true",
		Limit:      limit,
	}

	// An unrecognised state is refused rather than ignored. A filter that
	// silently does nothing returns every alert, and the caller renders
	// that as "these are the open ones" (brokoli-ee#242).
	if v := q.Get("state"); v != "" {
		state := store.AlertState(v)
		if !store.ValidAlertState(state) {
			writeError(w, http.StatusBadRequest, "state must be one of open, acknowledged, resolved")
			return
		}
		query.State = state
	}
	// assignee accepts only "me", for the same reason started_by does:
	// reading whose incidents somebody else holds is a different question
	// with its own access rule.
	if v := q.Get("assignee"); v != "" {
		if v != "me" {
			writeError(w, http.StatusBadRequest, `assignee only supports "me"`)
			return
		}
		if userID == "" {
			writeError(w, http.StatusUnauthorized, "authentication required")
			return
		}
		query.AssigneeIsCaller = true
	}

	// The current workspace's alerts only (ADR-049): the list filtered by
	// organization alone showed every workspace's failures in each.
	scoped := scopedStore(h.store, r)
	alerts, err := scoped.Alerts(query)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if alerts == nil {
		alerts = []models.Alert{}
	}

	// Unread is per person once there is a person. Without a caller
	// identity it falls back to the org-wide count, which is what an
	// unauthenticated deployment has always had.
	unread, err := scoped.CountUnreadAlerts(userID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"alerts":       alerts,
		"unread_count": unread,
	})
}

func (h *AlertHandler) MarkRead(w http.ResponseWriter, r *http.Request) {
	// Per person when there is one. The shared flag stays for a
	// deployment with no identity at all, which is the only case that
	// still needs it.
	if err := scopedStore(h.store, r).MarkAlertRead(chi.URLParam(r, "id"), alertCallerID(r)); err != nil {
		writeError(w, http.StatusNotFound, "alert not found")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *AlertHandler) MarkAllRead(w http.ResponseWriter, r *http.Request) {
	if err := scopedStore(h.store, r).MarkAllAlertsRead(alertCallerID(r)); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *AlertHandler) Dismiss(w http.ResponseWriter, r *http.Request) {
	if err := scopedStore(h.store, r).DismissAlert(chi.URLParam(r, "id")); err != nil {
		writeError(w, http.StatusNotFound, "alert not found")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ListDLQ returns dead-letter entries across every pipeline in the caller's
// org. The per-pipeline endpoint answers "what failed in this pipeline";
// this answers "what is broken anywhere", which is the question you have
// while triaging.
func (h *AlertHandler) ListDLQ(w http.ResponseWriter, r *http.Request) {
	includeResolved := r.URL.Query().Get("include_resolved") == "true"
	limit := boundedListLimit(r.URL.Query().Get("limit"))

	// The current workspace's entries (ADR-049); by organization alone
	// every workspace listed the others' failed records.
	entries, err := scopedStore(h.store, r).DLQ(includeResolved, limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if entries == nil {
		entries = []store.DLQEntry{}
	}
	writeJSON(w, http.StatusOK, entries)
}
