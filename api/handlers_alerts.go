package api

import (
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	"github.com/Tnsor-Labs/brokoli/models"
	"github.com/Tnsor-Labs/brokoli/store"
)

// AlertHandler serves the persisted alert inbox — the readable counterpart
// to the product's outbound-only Slack/webhook notifications, which are lost
// if nobody happens to be watching when they fire.
//
// Every route is scoped to the caller's organization. The org is taken from
// the request context, never from the request body or a query parameter, so
// a caller cannot ask for another tenant's alerts.
type AlertHandler struct {
	store store.Store
}

func NewAlertHandler(s store.Store) *AlertHandler {
	return &AlertHandler{store: s}
}

func (h *AlertHandler) List(w http.ResponseWriter, r *http.Request) {
	orgID := GetOrgIDFromRequest(r)
	unreadOnly := r.URL.Query().Get("unread") == "true"
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))

	alerts, err := h.store.ListAlerts(orgID, unreadOnly, limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if alerts == nil {
		alerts = []models.Alert{}
	}
	unread, err := h.store.CountUnreadAlerts(orgID)
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
	if err := h.store.MarkAlertRead(GetOrgIDFromRequest(r), chi.URLParam(r, "id")); err != nil {
		writeError(w, http.StatusNotFound, "alert not found")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *AlertHandler) MarkAllRead(w http.ResponseWriter, r *http.Request) {
	if err := h.store.MarkAllAlertsRead(GetOrgIDFromRequest(r)); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *AlertHandler) Dismiss(w http.ResponseWriter, r *http.Request) {
	if err := h.store.DismissAlert(GetOrgIDFromRequest(r), chi.URLParam(r, "id")); err != nil {
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
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))

	entries, err := h.store.ListDLQByOrg(GetOrgIDFromRequest(r), includeResolved, limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if entries == nil {
		entries = []store.DLQEntry{}
	}
	writeJSON(w, http.StatusOK, entries)
}
