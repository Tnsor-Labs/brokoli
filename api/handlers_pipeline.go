package api

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/Tnsor-Labs/brokoli/engine"
	"github.com/Tnsor-Labs/brokoli/models"
	"github.com/Tnsor-Labs/brokoli/pkg/common"
	"github.com/Tnsor-Labs/brokoli/store"
	"github.com/go-chi/chi/v5"
)

type PipelineHandler struct {
	store store.Store
	sched *engine.Scheduler
}

// requirePipelineOrg returns the caller's org_id for a pipeline create/clone/
// import flow, or writes a 400 and returns ok=false if multi-tenant mode is
// active and the caller has none.
//
// Without this check, users without an org membership could write pipelines
// with org_id="" — which are then invisible to every list/dashboard handler
// (those reject empty-org rows to prevent cross-tenant leaks). The result is
// a pipeline that exists in the database but cannot be seen by anyone: a
// silent data-loss bug. This helper makes the failure loud.
//
// In non-multi-tenant mode (community / self-hosted OSS with no
// OrgResolverFunc set), an empty org_id is legitimate and we let it through
// so the pipeline ends up scoped by workspace instead.
func requirePipelineOrg(w http.ResponseWriter, r *http.Request) (string, bool) {
	orgID := GetOrgIDFromRequest(r)
	if orgID != "" {
		return orgID, true
	}
	if OrgResolverFunc != nil {
		writeError(w, http.StatusBadRequest,
			"cannot create pipeline: user has no organization membership")
		return "", false
	}
	return "", true
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
	orgID := GetOrgIDFromRequest(r)
	after := r.URL.Query().Get("after")
	limit := 25
	if l := r.URL.Query().Get("limit"); l != "" {
		if n, _ := strconv.Atoi(l); n > 0 && n <= 100 {
			limit = n
		}
	}

	// Cursor-based pagination — no COUNT, uses UUIDv7 ordering
	if orgID != "" {
		pipelines, hasNext, err := h.store.ListPipelinesByOrgCursor(orgID, after, limit)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		summaries := make([]PipelineSummary, 0, len(pipelines))
		for _, p := range pipelines {
			summaries = append(summaries, toPipelineSummary(p))
		}
		cursor := ""
		if len(pipelines) > 0 {
			cursor = pipelines[len(pipelines)-1].ID
		}
		writeJSON(w, http.StatusOK, store.CursorResult{
			Items: summaries, HasNext: hasNext, Cursor: cursor, Limit: limit,
		})
		return
	}

	// Community mode fallback
	var pipelines []models.Pipeline
	var err error
	if OrgResolverFunc != nil {
		pipelines = []models.Pipeline{}
	} else {
		pipelines, err = h.store.ListPipelinesByWorkspace(GetWorkspaceID(r))
	}
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
	if !ValidateOrgAccess(r, pipeline.OrgID) {
		DenyOrgAccess(w)
		return
	}
	writeJSON(w, http.StatusOK, pipeline)
}

func (h *PipelineHandler) Create(w http.ResponseWriter, r *http.Request) {
	var p models.Pipeline
	if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}

	if err := p.Validate(); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	// Set org/workspace before cycle detection so traversal is org-scoped.
	p.WorkspaceID = GetWorkspaceID(r)
	orgID, ok := requirePipelineOrg(w, r)
	if !ok {
		return
	}
	p.OrgID = orgID

	if err := validateDependencyOrgScope(h.store, &p); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	if err := engine.DetectDependencyCycle(h.store, &p); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	p.ID = common.NewID()
	now := time.Now().UTC()
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
		h.sched.SyncPipeline(p.ID, p.Name, p.Schedule, p.Enabled, p.ScheduleTimezone)
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
	if !ValidateOrgAccess(r, existing.OrgID) {
		DenyOrgAccess(w)
		return
	}

	// Reject UI updates for git-managed pipelines
	if existing.Source == models.PipelineSourceGit {
		writeError(w, http.StatusForbidden, "This pipeline is managed by Git. Push changes to your repository.")
		return
	}

	var p models.Pipeline
	if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}

	p.ID = existing.ID
	p.PipelineID = existing.PipelineID
	p.Source = existing.Source
	p.WorkspaceID = existing.WorkspaceID
	p.OrgID = existing.OrgID
	p.CreatedAt = existing.CreatedAt
	p.UpdatedAt = time.Now().UTC()
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

	if err := validateDependencyOrgScope(h.store, &p); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	if err := engine.DetectDependencyCycle(h.store, &p); err != nil {
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
		h.sched.SyncPipeline(p.ID, p.Name, p.Schedule, p.Enabled, p.ScheduleTimezone)
	}

	AuditLog(r, "update", "pipeline", p.ID, map[string]interface{}{"name": existing.Name}, map[string]interface{}{"name": p.Name, "nodes": len(p.Nodes)})
	writeJSON(w, http.StatusOK, p)
}

func (h *PipelineHandler) Delete(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	existing, err := h.store.GetPipeline(id)
	if err != nil {
		writeError(w, http.StatusNotFound, "pipeline not found")
		return
	}
	if !ValidateOrgAccess(r, existing.OrgID) {
		DenyOrgAccess(w)
		return
	}

	resolve := r.URL.Query().Get("resolve")
	if resolve == "" {
		resolve = "abort"
	}
	if resolve != "abort" && resolve != "cascade" && resolve != "decouple" {
		writeError(w, http.StatusBadRequest, "invalid resolve value, use abort|cascade|decouple")
		return
	}

	// Load the org adjacency once and compute direct + transitive dependents from it.
	summaries, err := h.store.ListPipelineDepsByOrg(existing.OrgID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "load dependents: "+err.Error())
		return
	}
	direct := directDependents(summaries, id)

	if len(direct) > 0 && resolve == "abort" {
		respDeps := make([]map[string]string, 0, len(direct))
		for _, d := range direct {
			respDeps = append(respDeps, map[string]string{"id": d.ID, "name": d.Name})
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusConflict)
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"error":      "pipeline has dependents",
			"dependents": respDeps,
			"hint":       "retry with ?resolve=cascade to delete dependents, or ?resolve=decouple to strip this dependency from them",
		})
		return
	}

	// Compute the transitive closure for cascade, so a->b->c cascade from a wipes all three.
	var toDelete []models.PipelineDepSummary
	if resolve == "cascade" {
		toDelete = transitiveDependents(summaries, id)
	}

	// All mutations happen inside a single transaction using the tx-scoped store methods.
	// If any step fails, the transaction rolls back and nothing is persisted.
	err = h.store.WithTx(func(tx *sql.Tx) error {
		switch resolve {
		case "cascade":
			for _, d := range toDelete {
				if err := h.store.DeletePipelineTx(tx, d.ID); err != nil {
					return fmt.Errorf("cascade delete %s: %w", d.ID, err)
				}
			}
		case "decouple":
			for _, d := range direct {
				stripped, err := h.store.GetPipeline(d.ID)
				if err != nil {
					return fmt.Errorf("decouple load %s: %w", d.ID, err)
				}
				stripDependencyInPlace(stripped, id)
				stripped.UpdatedAt = time.Now().UTC()
				if err := h.store.UpdatePipelineTx(tx, stripped); err != nil {
					return fmt.Errorf("decouple update %s: %w", d.ID, err)
				}
			}
		}
		return h.store.DeletePipelineTx(tx, id)
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	// Post-commit side effects: scheduler unregistration + audit logging.
	// These run after the transaction so a partial commit can't leave ghost schedule entries.
	if h.sched != nil {
		h.sched.Unregister(id)
		for _, d := range toDelete {
			h.sched.Unregister(d.ID)
		}
	}
	switch resolve {
	case "cascade":
		for _, d := range toDelete {
			AuditLog(r, "delete", "pipeline", d.ID,
				map[string]interface{}{"name": d.Name},
				map[string]interface{}{"cascade_from": id})
		}
	case "decouple":
		for _, d := range direct {
			AuditLog(r, "update", "pipeline", d.ID,
				map[string]interface{}{"name": d.Name},
				map[string]interface{}{"decoupled_from": id})
		}
	}
	AuditLog(r, "delete", "pipeline", id, nil, map[string]interface{}{
		"resolve":             resolve,
		"direct_dependents":   len(direct),
		"cascaded_dependents": len(toDelete),
	})
	w.WriteHeader(http.StatusNoContent)
}

// directDependents returns same-org summaries whose rules reference targetID.
func directDependents(summaries []models.PipelineDepSummary, targetID string) []models.PipelineDepSummary {
	out := make([]models.PipelineDepSummary, 0)
	for _, sum := range summaries {
		for _, rule := range sum.EffectiveDependencies() {
			if rule.PipelineID == targetID {
				out = append(out, sum)
				break
			}
		}
	}
	return out
}

// transitiveDependents returns the full set of pipelines reachable as dependents
// of targetID via the dep graph, in leaf-first order so the caller can delete
// them safely without violating any lingering foreign-key-ish expectations.
func transitiveDependents(summaries []models.PipelineDepSummary, targetID string) []models.PipelineDepSummary {
	// Build reverse adjacency: pipelineID -> summaries that list it as a dep.
	reverse := make(map[string][]models.PipelineDepSummary, len(summaries))
	for _, sum := range summaries {
		for _, rule := range sum.EffectiveDependencies() {
			reverse[rule.PipelineID] = append(reverse[rule.PipelineID], sum)
		}
	}

	visited := make(map[string]bool)
	order := make([]models.PipelineDepSummary, 0)

	var dfs func(id string)
	dfs = func(id string) {
		for _, child := range reverse[id] {
			if visited[child.ID] {
				continue
			}
			visited[child.ID] = true
			dfs(child.ID)
			order = append(order, child)
		}
	}
	dfs(targetID)
	return order
}

// validateDependencyOrgScope rejects any dependency rule that references a pipeline
// outside the caller's org. Prevents cross-tenant side channels via trigger-mode auto-firing
// or blocked-run reason leaks. Must be called after p.OrgID has been set.
//
// Uses a single ListPipelineDepsByOrg query for all rules (was N GetPipeline round-trips).
func validateDependencyOrgScope(s store.Store, p *models.Pipeline) error {
	rules := p.EffectiveDependencies()
	if len(rules) == 0 {
		return nil
	}
	summaries, err := s.ListPipelineDepsByOrg(p.OrgID)
	if err != nil {
		return fmt.Errorf("load org pipelines: %w", err)
	}
	sameOrg := make(map[string]bool, len(summaries))
	for _, sum := range summaries {
		sameOrg[sum.ID] = true
	}
	for _, rule := range rules {
		if rule.PipelineID == p.ID {
			// Self-dep is rejected by Pipeline.Validate(), but guard here too.
			continue
		}
		if !sameOrg[rule.PipelineID] {
			// Treat missing and cross-org identically — the error message never reveals
			// whether the pipeline ID exists in another org.
			return fmt.Errorf("dependency pipeline not found: %s", rule.PipelineID)
		}
	}
	return nil
}

// sanitizeFilename strips characters that could cause header injection or path traversal.
func sanitizeFilename(name string) string {
	clean := make([]byte, 0, len(name))
	for i := 0; i < len(name); i++ {
		c := name[i]
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '-' || c == '_' || c == '.' {
			clean = append(clean, c)
		}
	}
	if len(clean) == 0 {
		return "pipeline"
	}
	return string(clean)
}

// stripDependencyInPlace removes all references to depID from DependsOn and DependencyRules.
func stripDependencyInPlace(p *models.Pipeline, depID string) {
	newDeps := p.DependsOn[:0]
	for _, d := range p.DependsOn {
		if d != depID {
			newDeps = append(newDeps, d)
		}
	}
	p.DependsOn = newDeps
	newRules := p.DependencyRules[:0]
	for _, r := range p.DependencyRules {
		if r.PipelineID != depID {
			newRules = append(newRules, r)
		}
	}
	p.DependencyRules = newRules
}

func (h *PipelineHandler) ListVersions(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if p, err := h.store.GetPipeline(id); err == nil {
		if !ValidateOrgAccess(r, p.OrgID) {
			DenyOrgAccess(w)
			return
		}
	}
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
	if existing, err := h.store.GetPipeline(id); err == nil {
		if !ValidateOrgAccess(r, existing.OrgID) {
			DenyOrgAccess(w)
			return
		}
	} else {
		writeError(w, http.StatusNotFound, "pipeline not found")
		return
	}

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
	p.UpdatedAt = time.Now().UTC()

	if err := h.store.UpdatePipeline(&p); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

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
	if !ValidateOrgAccess(r, p.OrgID) {
		DenyOrgAccess(w)
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
		p.ID = common.NewID()
	}
	now := time.Now().UTC()
	if p.CreatedAt.IsZero() {
		p.CreatedAt = now
	}
	p.UpdatedAt = now

	// Set org/workspace ownership. Import is a create-flow, so it gets
	// the same empty-org gate as Create — an imported YAML with no org
	// context would produce an invisible pipeline.
	orgID, ok := requirePipelineOrg(w, r)
	if !ok {
		return
	}
	p.OrgID = orgID
	if p.WorkspaceID == "" {
		p.WorkspaceID = GetWorkspaceID(r)
	}

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

	// Validate org access
	if !ValidateOrgAccess(r, orig.OrgID) {
		DenyOrgAccess(w)
		return
	}

	// Create a deep copy with new IDs
	clone := *orig
	clone.ID = common.NewID()
	clone.Name = orig.Name + " (copy)"
	now := time.Now().UTC()
	clone.CreatedAt = now
	clone.UpdatedAt = now

	// Map old node IDs to new ones
	idMap := make(map[string]string, len(orig.Nodes))
	newNodes := make([]models.Node, len(orig.Nodes))
	for i, n := range orig.Nodes {
		newID := common.NewID()[:8]
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

	// Ensure clone belongs to current user's org/workspace. Clone is a
	// create-flow, so it gets the same empty-org gate as Create.
	orgID, ok := requirePipelineOrg(w, r)
	if !ok {
		return
	}
	clone.OrgID = orgID
	if wsID := GetWorkspaceID(r); wsID != "" {
		clone.WorkspaceID = wsID
	}

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
	if !ValidateOrgAccess(r, p.OrgID) {
		DenyOrgAccess(w)
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
	if !ValidateOrgAccess(r, p.OrgID) {
		DenyOrgAccess(w)
		return
	}

	yamlData, err := engine.ExportPipelineYAML(p)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	safeName := sanitizeFilename(p.Name)
	w.Header().Set("Content-Type", "application/x-yaml")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s.yaml"`, safeName))
	w.Write(yamlData) //nolint:errcheck
}
