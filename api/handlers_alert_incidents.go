package api

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/golang-jwt/jwt/v5"

	"github.com/Tnsor-Labs/brokoli/store"
)

// Incident ownership on alerts (brokoli-ee#242): assign it, say you are
// on it, say it is dealt with.

// alertCallerID is who is asking. Empty when the deployment has no
// authentication at all, which is the one case the org-wide read state
// still serves.
func alertCallerID(r *http.Request) string {
	claims, _ := r.Context().Value("claims").(*jwt.MapClaims)
	if claims == nil {
		return ""
	}
	sub, _ := (*claims)["sub"].(string)
	return sub
}

// Assign sets or clears an alert's owner.
func (h *AlertHandler) Assign(w http.ResponseWriter, r *http.Request) {
	orgID := GetOrgIDFromRequest(r)
	alertID := chi.URLParam(r, "id")

	// A pointer, so that clearing the assignee is expressible: absent
	// means the caller sent nothing, null means unassign.
	var req struct {
		UserID *string `json:"user_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	assignee := ""
	if req.UserID != nil {
		assignee = *req.UserID
	}

	if err := h.store.SetAlertAssignee(orgID, alertID, assignee); err != nil {
		if errors.Is(err, store.ErrAlertNotFound) {
			writeError(w, http.StatusNotFound, "alert not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to assign alert")
		return
	}
	AuditLog(r, "assign_alert", "alert", alertID, nil,
		map[string]interface{}{"assignee_user_id": assignee})
	w.WriteHeader(http.StatusNoContent)
}

// Acknowledge records that the caller is on it.
func (h *AlertHandler) Acknowledge(w http.ResponseWriter, r *http.Request) {
	orgID := GetOrgIDFromRequest(r)
	alertID := chi.URLParam(r, "id")
	userID := alertCallerID(r)
	if userID == "" {
		// "Somebody is on it" with no somebody is not a fact worth
		// storing.
		writeError(w, http.StatusUnauthorized, "authentication required")
		return
	}

	if err := h.store.AcknowledgeAlert(orgID, alertID, userID); err != nil {
		if errors.Is(err, store.ErrAlertNotFound) {
			writeError(w, http.StatusNotFound, "alert not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to acknowledge alert")
		return
	}
	AuditLog(r, "acknowledge_alert", "alert", alertID, nil, nil)
	w.WriteHeader(http.StatusNoContent)
}

// Resolve marks an incident dealt with.
func (h *AlertHandler) Resolve(w http.ResponseWriter, r *http.Request) {
	orgID := GetOrgIDFromRequest(r)
	alertID := chi.URLParam(r, "id")
	userID := alertCallerID(r)
	if userID == "" {
		writeError(w, http.StatusUnauthorized, "authentication required")
		return
	}

	if err := h.store.ResolveAlert(orgID, alertID, userID); err != nil {
		if errors.Is(err, store.ErrAlertNotFound) {
			writeError(w, http.StatusNotFound, "alert not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to resolve alert")
		return
	}
	AuditLog(r, "resolve_alert", "alert", alertID, nil, nil)
	w.WriteHeader(http.StatusNoContent)
}
