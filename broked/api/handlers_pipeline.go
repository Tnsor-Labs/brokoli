package api

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/hc12r/broked/engine"
	"github.com/hc12r/broked/models"
	"github.com/hc12r/broked/store"
)

type PipelineHandler struct {
	store store.Store
	sched *engine.Scheduler
}

// PipelineSummary is a lean DTO for the pipeline list — no nodes/edges/hooks.
type PipelineSummary struct {
	ID           string   `json:"id"`
	Name         string   `json:"name"`
	Description  string   `json:"description"`
	Schedule     string   `json:"schedule"`
	Enabled      bool     `json:"enabled"`
	Tags         []string `json:"tags"`
	NodeCount    int      `json:"node_count"`
	EdgeCount    int      `json:"edge_count"`
	SLADeadline  string   `json:"sla_deadline,omitempty"`
	SLATimezone  string   `json:"sla_timezone,omitempty"`
	DependsOn    []string `json:"depends_on,omitempty"`
	WebhookToken string   `json:"webhook_token,omitempty"`
	PipelineID   string   `json:"pipeline_id,omitempty"`
	Source       string   `json:"source,omitempty"`
	CreatedAt    string   `json:"created_at"`
	UpdatedAt    string   `json:"updated_at"`
}

func toPipelineSummary(p models.Pipeline) PipelineSummary {
	tags := p.Tags
	if tags == nil {
		tags = []string{}
	}
	deps := p.DependsOn
	if deps == nil {
		deps = []string{}
	}
	return PipelineSummary{
		ID:           p.ID,
		Name:         p.Name,
		Description:  p.Description,
		Schedule:     p.Schedule,
		Enabled:      p.Enabled,
		Tags:         tags,
		NodeCount:    len(p.Nodes),
		EdgeCount:    len(p.Edges),
		SLADeadline:  p.SLADeadline,
		SLATimezone:  p.SLATimezone,
		DependsOn:    deps,
		WebhookToken: p.WebhookToken,
		PipelineID:   p.PipelineID,
		Source:       p.Source,
		CreatedAt:    p.CreatedAt.Format("2006-01-02T15:04:05.999999999Z07:00"),
		UpdatedAt:    p.UpdatedAt.Format("2006-01-02T15:04:05.999999999Z07:00"),
	}
}

func NewPipelineHandler(s store.Store, sched *engine.Scheduler) *PipelineHandler {
	return &PipelineHandler{store: s, sched: sched}
}

func (h *PipelineHandler) List(w http.ResponseWriter, r *http.Request) {
	wsID := GetWorkspaceID(r)
	pipelines, err := h.store.ListPipelinesByWorkspace(wsID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	summaries := make([]PipelineSummary, 0, len(pipelines))
	for _, p := range pipelines {
		summaries = append(summaries, toPipelineSummary(p))
	}
	writeJSON(w, http.StatusOK, summaries)
}

func (h *PipelineHandler) Get(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	pipeline, err := h.store.GetPipeline(id)
	if err != nil {
		writeError(w, http.StatusNotFound, "pipeline not found")
		return
	}
	writeJSON(w, http.StatusOK, pipeline)
}

func (h *PipelineHandler) Create(w http.ResponseWriter, r *http.Request) {
	var p models.Pipeline
	if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}

	if err := p.Validate(); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	p.ID = uuid.New().String()
	now := time.Now()
	p.CreatedAt = now
	p.UpdatedAt = now
	if p.Nodes == nil {
		p.Nodes = []models.Node{}
	}
	if p.Edges == nil {
		p.Edges = []models.Edge{}
	}

	// Auto-generate pipeline_id from name if not provided
	if p.PipelineID == "" && p.Name != "" {
		pid := strings.ToLower(p.Name)
		pid = strings.ReplaceAll(pid, " ", "-")
		pid = regexp.MustCompile(`[^a-z0-9-]`).ReplaceAllString(pid, "")
		pid = regexp.MustCompile(`-+`).ReplaceAllString(pid, "-")
		pid = strings.Trim(pid, "-")
		p.PipelineID = pid
	}
	// UI-created pipelines are always source "ui"
	if p.Source == "" {
		p.Source = models.PipelineSourceUI
	}

	if err := h.store.CreatePipeline(&p); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	AuditLog(r, "create", "pipeline", p.ID, nil, map[string]interface{}{"name": p.Name, "nodes": len(p.Nodes)})

	// Sync scheduler if created with a schedule
	if h.sched != nil && p.Schedule != "" && p.Enabled {
		h.sched.SyncPipeline(p.ID, p.Name, p.Schedule, p.Enabled)
	}

	writeJSON(w, http.StatusCreated, p)
}

func (h *PipelineHandler) Update(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")

	existing, err := h.store.GetPipeline(id)
	if err != nil {
		writeError(w, http.StatusNotFound, "pipeline not found")
		return
	}

	// Reject UI updates for git-managed pipelines
	if existing.Source == models.PipelineSourceGit {
		writeError(w, http.StatusForbidden, "This pipeline is managed by Git. Push changes to your repository.")
		return
	}

	var p models.Pipeline
	if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}

	p.ID = existing.ID
	p.PipelineID = existing.PipelineID
	p.Source = existing.Source
	p.CreatedAt = existing.CreatedAt
	p.UpdatedAt = time.Now()
	if p.Nodes == nil {
		p.Nodes = []models.Node{}
	}
	if p.Edges == nil {
		p.Edges = []models.Edge{}
	}

	if err := p.Validate(); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	if err := h.store.UpdatePipeline(&p); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	// Auto-save version snapshot
	snapshot, _ := json.Marshal(p)
	h.store.SavePipelineVersion(p.ID, string(snapshot), "")

	// Sync scheduler
	if h.sched != nil {
		h.sched.SyncPipeline(p.ID, p.Name, p.Schedule, p.Enabled)
	}

	AuditLog(r, "update", "pipeline", p.ID, map[string]interface{}{"name": existing.Name}, map[string]interface{}{"name": p.Name, "nodes": len(p.Nodes)})
	writeJSON(w, http.StatusOK, p)
}

func (h *PipelineHandler) Delete(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if err := h.store.DeletePipeline(id); err != nil {
		writeError(w, http.StatusNotFound, "pipeline not found")
		return
	}
	if h.sched != nil {
		h.sched.Unregister(id)
	}
	AuditLog(r, "delete", "pipeline", id, nil, nil)
	w.WriteHeader(http.StatusNoContent)
}

func (h *PipelineHandler) ListVersions(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	versions, err := h.store.ListPipelineVersions(id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if versions == nil {
		versions = []store.PipelineVersion{}
	}
	writeJSON(w, http.StatusOK, versions)
}

func (h *PipelineHandler) Rollback(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")

	var req struct {
		Version int `json:"version"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Version <= 0 {
		writeError(w, http.StatusBadRequest, "version is required")
		return
	}

	snapshot, err := h.store.GetPipelineVersion(id, req.Version)
	if err != nil {
		writeError(w, http.StatusNotFound, "version not found")
		return
	}

	var p models.Pipeline
	if err := json.Unmarshal([]byte(snapshot), &p); err != nil {
		writeError(w, http.StatusInternalServerError, "corrupt snapshot")
		return
	}
	p.UpdatedAt = time.Now()

	if err := h.store.UpdatePipeline(&p); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	// Save rollback as a new version
	h.store.SavePipelineVersion(p.ID, snapshot, fmt.Sprintf("rollback to v%d", req.Version))

	writeJSON(w, http.StatusOK, p)
}

func (h *PipelineHandler) Validate(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	p, err := h.store.GetPipeline(id)
	if err != nil {
		writeError(w, http.StatusNotFound, "pipeline not found")
		return
	}

	ve := engine.ValidatePipeline(p)
	if ve.HasErrors() {
		writeJSON(w, http.StatusOK, map[string]interface{}{"valid": false, "errors": ve.Errors})
	} else {
		writeJSON(w, http.StatusOK, map[string]interface{}{"valid": true, "errors": []string{}})
	}
}

func (h *PipelineHandler) Import(w http.ResponseWriter, r *http.Request) {
	data, err := io.ReadAll(io.LimitReader(r.Body, 1<<20)) // 1MB max
	if err != nil {
		writeError(w, http.StatusBadRequest, "failed to read body")
		return
	}

	// Try JSON first, then YAML
	var p *models.Pipeline
	contentType := r.Header.Get("Content-Type")
	if contentType == "application/json" || data[0] == '{' || data[0] == '[' {
		var pipeline models.Pipeline
		if jsonErr := json.Unmarshal(data, &pipeline); jsonErr == nil {
			p = &pipeline
		}
	}
	if p == nil {
		var err error
		p, err = engine.ImportPipelineYAML(data)
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
	}
	if p.ID == "" {
		p.ID = uuid.New().String()
	}
	now := time.Now()
	if p.CreatedAt.IsZero() {
		p.CreatedAt = now
	}
	p.UpdatedAt = now

	if err := h.store.CreatePipeline(p); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, p)
}

func (h *PipelineHandler) Clone(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	orig, err := h.store.GetPipeline(id)
	if err != nil {
		writeError(w, http.StatusNotFound, "pipeline not found")
		return
	}

	// Create a deep copy with new IDs
	clone := *orig
	clone.ID = uuid.New().String()
	clone.Name = orig.Name + " (copy)"
	now := time.Now()
	clone.CreatedAt = now
	clone.UpdatedAt = now

	// Map old node IDs to new ones
	idMap := make(map[string]string, len(orig.Nodes))
	newNodes := make([]models.Node, len(orig.Nodes))
	for i, n := range orig.Nodes {
		newID := uuid.New().String()[:8]
		idMap[n.ID] = newID
		newNodes[i] = n
		newNodes[i].ID = newID
		// Deep copy config
		configJSON, _ := json.Marshal(n.Config)
		var newConfig map[string]interface{}
		json.Unmarshal(configJSON, &newConfig)
		newNodes[i].Config = newConfig
	}
	clone.Nodes = newNodes

	// Remap edge IDs
	newEdges := make([]models.Edge, 0, len(orig.Edges))
	for _, e := range orig.Edges {
		newFrom, ok1 := idMap[e.From]
		newTo, ok2 := idMap[e.To]
		if ok1 && ok2 {
			newEdges = append(newEdges, models.Edge{From: newFrom, To: newTo})
		}
	}
	clone.Edges = newEdges

	if err := h.store.CreatePipeline(&clone); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, clone)
}

func (h *PipelineHandler) ValidateNodes(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	p, err := h.store.GetPipeline(id)
	if err != nil {
		writeError(w, http.StatusNotFound, "pipeline not found")
		return
	}

	results := engine.ValidateNodes(p.Nodes)
	if results == nil {
		results = []engine.NodeValidationResult{}
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"issues": results,
	})
}

func (h *PipelineHandler) Export(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	p, err := h.store.GetPipeline(id)
	if err != nil {
		writeError(w, http.StatusNotFound, "pipeline not found")
		return
	}

	yamlData, err := engine.ExportPipelineYAML(p)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	w.Header().Set("Content-Type", "application/x-yaml")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%s.yaml", p.Name))
	w.Write(yamlData)
}
