package api

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/Tnsor-Labs/brokoli/models"
	"github.com/Tnsor-Labs/brokoli/pkg/common"
	"github.com/Tnsor-Labs/brokoli/store"
)

// TemplateHandler serves the built-in pipeline templates
// (store.Store's pipeline_templates table, seeded from
// pkg/templates.Builtin) offered when creating a new pipeline. Listing
// requires no permission (any authenticated user); create/update/delete
// require the admin role (see requireAdmin in routes.go) — templates are
// global and admin-curated, not per-org/per-workspace.
type TemplateHandler struct {
	store store.Store
}

func NewTemplateHandler(s store.Store) *TemplateHandler {
	return &TemplateHandler{store: s}
}

func (h *TemplateHandler) List(w http.ResponseWriter, r *http.Request) {
	list, err := h.store.ListPipelineTemplates()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if list == nil {
		list = []models.PipelineTemplate{}
	}
	creatable := make([]models.PipelineTemplate, 0, len(list))
	for _, template := range list {
		if len(template.Nodes) > 0 {
			creatable = append(creatable, template)
		}
	}
	writeJSON(w, http.StatusOK, creatable)
}

func (h *TemplateHandler) Create(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ID          string        `json:"id"`
		Name        string        `json:"name"`
		Description string        `json:"description"`
		Icon        string        `json:"icon"`
		Nodes       []models.Node `json:"nodes"`
		Edges       []models.Edge `json:"edges"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	if req.Name == "" {
		writeError(w, http.StatusBadRequest, "name is required")
		return
	}
	if req.ID == "" {
		req.ID = common.NewID()
	}
	now := time.Now().UTC()
	t := &models.PipelineTemplate{
		ID:          req.ID,
		Name:        req.Name,
		Description: req.Description,
		Icon:        req.Icon,
		Nodes:       req.Nodes,
		Edges:       req.Edges,
		CreatedAt:   now,
		UpdatedAt:   now,
	}
	if t.Nodes == nil {
		t.Nodes = []models.Node{}
	}
	if t.Edges == nil {
		t.Edges = []models.Edge{}
	}
	if err := h.store.CreatePipelineTemplate(t); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, t)
}

func (h *TemplateHandler) Update(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	existing, err := h.store.GetPipelineTemplate(id)
	if err != nil {
		writeError(w, http.StatusNotFound, "template not found")
		return
	}

	var req struct {
		Name        *string        `json:"name"`
		Description *string        `json:"description"`
		Icon        *string        `json:"icon"`
		Nodes       *[]models.Node `json:"nodes"`
		Edges       *[]models.Edge `json:"edges"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	if req.Name != nil {
		if *req.Name == "" {
			writeError(w, http.StatusBadRequest, "name cannot be empty")
			return
		}
		existing.Name = *req.Name
	}
	if req.Description != nil {
		existing.Description = *req.Description
	}
	if req.Icon != nil {
		existing.Icon = *req.Icon
	}
	if req.Nodes != nil {
		existing.Nodes = *req.Nodes
	}
	if req.Edges != nil {
		existing.Edges = *req.Edges
	}
	existing.UpdatedAt = time.Now().UTC()

	if err := h.store.UpdatePipelineTemplate(existing); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, existing)
}

func (h *TemplateHandler) Delete(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if err := h.store.DeletePipelineTemplate(id); err != nil {
		writeError(w, http.StatusNotFound, "template not found")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
