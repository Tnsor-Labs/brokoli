package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/Tnsor-Labs/brokoli/models"
)

// bulkActionPermission is the permission each bulk action needs: what the
// single-pipeline endpoint for the same change asks for.
var bulkActionPermission = map[string]models.Permission{
	"delete":  models.PermPipelinesDelete,
	"enable":  models.PermPipelinesEdit,
	"disable": models.PermPipelinesEdit,
}

// bulkMaxIDs is how many pipelines one bulk request may name.
const bulkMaxIDs = 100

type bulkRequest struct {
	IDs    []string `json:"ids"`
	Action string   `json:"action"`
}

type bulkFailure struct {
	ID    string `json:"id"`
	Error string `json:"error"`
}

// Bulk handles POST /pipelines/bulk: delete, enable or disable up to 100
// pipelines. Each pipeline goes through what its single endpoint does, so
// a bulk change is never a way around a check (#593): a delete refuses a
// pipeline others depend on, a Git-managed pipeline cannot be enabled or
// disabled here, and every change syncs the scheduler, is versioned or
// unregistered, and is audited. A pipeline that cannot be changed is
// reported in errors with the reason; the others proceed.
//
// gate is the route's permission middleware. The permission depends on
// the action, which is in the body, so the gate is applied here once the
// body is read: enable and disable need pipelines.edit, delete needs
// pipelines.delete. An action that is none of these is refused with 400
// before anything changes.
func (h *PipelineHandler) Bulk(gate func(models.Permission) func(http.Handler) http.Handler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		limitBody(w, r)
		raw, err := io.ReadAll(r.Body)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid JSON")
			return
		}
		var req bulkRequest
		if err := json.Unmarshal(raw, &req); err != nil {
			writeError(w, http.StatusBadRequest, "invalid JSON")
			return
		}
		perm, ok := bulkActionPermission[req.Action]
		if !ok {
			writeError(w, http.StatusBadRequest, fmt.Sprintf("unknown action %q; use delete, enable or disable", req.Action))
			return
		}
		if len(req.IDs) > bulkMaxIDs {
			writeError(w, http.StatusBadRequest, fmt.Sprintf("max %d operations per request", bulkMaxIDs))
			return
		}
		r.Body = io.NopCloser(bytes.NewReader(raw))
		gate(perm)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			h.bulk(w, r, req)
		})).ServeHTTP(w, r)
	}
}

func (h *PipelineHandler) bulk(w http.ResponseWriter, r *http.Request, req bulkRequest) {
	var succeeded []string
	var failed []bulkFailure
	if req.Action == "delete" {
		succeeded, failed = h.bulkDelete(r, req.IDs)
	} else {
		for _, id := range req.IDs {
			if msg := h.bulkSetEnabled(r, id, req.Action == "enable"); msg != "" {
				failed = append(failed, bulkFailure{ID: id, Error: msg})
			} else {
				succeeded = append(succeeded, id)
			}
		}
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"action":    req.Action,
		"succeeded": len(succeeded),
		"failed":    len(failed),
		"errors":    failed,
	})
}

// actionable is the pipeline id names, when the caller may change it.
// One outside the current workspace is answered like a missing one, so a
// bulk request cannot tell which ids exist elsewhere.
func (h *PipelineHandler) actionable(r *http.Request, id string) (*models.Pipeline, bool) {
	p, err := scopedStore(h.store, r).Pipeline(id)
	if err != nil || !pipelineActionable(r, p) {
		return nil, false
	}
	return p, true
}

// bulkSetEnabled enables or disables one pipeline as an edit does, and
// answers why it could not, or "".
func (h *PipelineHandler) bulkSetEnabled(r *http.Request, id string, enabled bool) string {
	existing, ok := h.actionable(r, id)
	if !ok {
		return "not found"
	}
	if existing.Source == models.PipelineSourceGit {
		return "managed by Git; change it in the repository"
	}
	updated := *existing
	updated.Enabled = enabled
	if _, status, msg := h.saveUpdate(r, existing, &updated, ""); status != http.StatusOK {
		return msg
	}
	return ""
}

// bulkDelete deletes the pipelines ids names, each as the single delete
// does without a resolve: one that others depend on is refused. A
// dependent named in the same request does not block it, so ids may come
// in any order: a pipeline refused for its dependents is tried again once
// others have been deleted, until a pass deletes nothing more.
func (h *PipelineHandler) bulkDelete(r *http.Request, ids []string) ([]string, []bulkFailure) {
	var succeeded []string
	reasons := map[string]string{}
	pending := []string{}
	for _, id := range ids {
		if _, ok := h.actionable(r, id); !ok {
			reasons[id] = "not found"
			continue
		}
		pending = append(pending, id)
	}
	for len(pending) > 0 {
		var next []string
		for _, id := range pending {
			msg, retry := h.bulkDeleteOne(r, id)
			switch {
			case msg == "":
				succeeded = append(succeeded, id)
				delete(reasons, id)
			case retry:
				reasons[id] = msg
				next = append(next, id)
			default:
				reasons[id] = msg
			}
		}
		if len(next) == len(pending) {
			break
		}
		pending = next
	}
	var failed []bulkFailure
	for _, id := range ids {
		if msg, ok := reasons[id]; ok {
			failed = append(failed, bulkFailure{ID: id, Error: msg})
		}
	}
	return succeeded, failed
}

// bulkDeleteOne deletes one pipeline unless others depend on it. It
// answers why not, or "", and whether the refusal was for dependents,
// which a later pass may have removed.
func (h *PipelineHandler) bulkDeleteOne(r *http.Request, id string) (string, bool) {
	existing, ok := h.actionable(r, id)
	if !ok {
		return "not found", false
	}
	deps, err := h.dependentsOf(r, existing)
	if err != nil {
		return "load dependents: " + err.Error(), false
	}
	if len(deps.direct) > 0 {
		names := make([]string, 0, len(deps.visible))
		for _, d := range deps.visible {
			names = append(names, d.Name)
		}
		msg := "pipeline has dependents"
		if len(names) > 0 {
			msg += ": " + strings.Join(names, ", ")
		}
		if deps.hidden > 0 {
			msg += fmt.Sprintf(" (and %d in other workspaces)", deps.hidden)
		}
		return msg + "; delete it on its own with resolve=cascade or resolve=decouple", true
	}
	if err := h.commitDelete(r, id, "abort", nil, nil); err != nil {
		return err.Error(), false
	}
	return "", false
}
