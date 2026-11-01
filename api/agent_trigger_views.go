package api

import (
	"net/http"
	"sort"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/golang-jwt/jwt/v5"

	"github.com/Tnsor-Labs/brokoli/models"
)

// Triggers as the rest of the product shows them (ADR-047 section 7):
//
//	GET /api/agents/trigger-events    the event types a trigger may subscribe to
//	GET /api/runs/{id}/agents         the agents told about a run, and what each then proposed
//	GET /api/pipelines/{id}/triggers  the triggers that watch a pipeline
//
// The last two answer anyone who may see the pipeline. They name the
// agent, as a change request it proposed already does; a webhook's URL is
// shown only to whoever may manage the agent.

// RunNotice is one agent told about a run.
type RunNotice struct {
	DeliveryID string     `json:"delivery_id"`
	TriggerID  string     `json:"trigger_id"`
	AgentID    string     `json:"agent_id"`
	AgentName  string     `json:"agent_name"`
	Event      string     `json:"event"`
	Status     string     `json:"status"`
	Reason     string     `json:"reason,omitempty"`
	CreatedAt  time.Time  `json:"created_at"`
	FinishedAt *time.Time `json:"finished_at,omitempty"`
	// Proposal is the first change request the agent made to the pipeline
	// after it was told, if it made one.
	Proposal *NoticeProposal `json:"proposal,omitempty"`
}

// NoticeProposal is the change request a notice led to.
type NoticeProposal struct {
	ID     string `json:"id"`
	Title  string `json:"title"`
	Status string `json:"status"`
}

// PipelineTrigger is a trigger seen from a pipeline it watches.
type PipelineTrigger struct {
	ID        string   `json:"id"`
	AgentID   string   `json:"agent_id"`
	AgentName string   `json:"agent_name"`
	Events    []string `json:"events"`
	Delivery  string   `json:"delivery"`
	// Scope is "pipeline" for a trigger on this pipeline alone, or
	// "workspace" for one on every pipeline in the workspace.
	Scope           string `json:"scope"`
	Enabled         bool   `json:"enabled"`
	CooldownSeconds int    `json:"cooldown_seconds"`
	// URL and MayManage are for whoever may manage the agent.
	URL       string `json:"url,omitempty"`
	MayManage bool   `json:"may_manage"`
}

func (h *triggersAPI) eventTypes(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, TriggerEventTypes())
}

// visiblePipeline is the pipeline when the caller may see it; otherwise an
// answer is written.
func (h *triggersAPI) visiblePipeline(w http.ResponseWriter, r *http.Request, id string) (*models.Pipeline, bool) {
	p, err := h.agents.pc.store.GetPipeline(id)
	if err != nil || p == nil {
		writeError(w, http.StatusNotFound, "pipeline not found")
		return nil, false
	}
	if !ValidateOrgAccess(r, p.OrgID) {
		DenyOrgAccess(w)
		return nil, false
	}
	if !userOwnsWorkspace(r, p.WorkspaceID) {
		denyWorkspaceAccess(w)
		return nil, false
	}
	return p, true
}

func (h *triggersAPI) runNotices(w http.ResponseWriter, r *http.Request) {
	run, err := h.agents.pc.store.GetRun(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, http.StatusNotFound, "run not found")
		return
	}
	p, ok := h.visiblePipeline(w, r, run.PipelineID)
	if !ok {
		return
	}
	deliveries, err := h.ts.RunDeliveries(run.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not list who was told about the run")
		return
	}
	var proposals []ChangeRequest
	if changeRequests != nil && len(deliveries) > 0 {
		if proposals, err = changeRequests.list(p.ID, "", 200); err != nil {
			writeError(w, http.StatusInternalServerError, "could not list the pipeline's change requests")
			return
		}
		// Oldest first, so the first match is the first proposal made.
		sort.SliceStable(proposals, func(i, j int) bool { return proposals[i].CreatedAt.Before(proposals[j].CreatedAt) })
	}
	names := map[string]string{}
	out := make([]RunNotice, 0, len(deliveries))
	for _, d := range deliveries {
		n := RunNotice{DeliveryID: d.ID, TriggerID: d.TriggerID, AgentID: d.AgentID, AgentName: h.agentName(names, d.AgentID),
			Event: d.Event, Status: d.Status, Reason: d.Reason, CreatedAt: d.CreatedAt, FinishedAt: d.FinishedAt}
		for _, cr := range proposals {
			if cr.CreatedBy == d.AgentID && !cr.CreatedAt.Before(d.CreatedAt) {
				n.Proposal = &NoticeProposal{ID: cr.ID, Title: cr.Title, Status: cr.Status}
				break
			}
		}
		out = append(out, n)
	}
	writeJSON(w, http.StatusOK, out)
}

func (h *triggersAPI) pipelineTriggers(w http.ResponseWriter, r *http.Request) {
	p, ok := h.visiblePipeline(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	triggers, err := h.ts.ListWorkspace(p.WorkspaceID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not list triggers")
		return
	}
	callerID, manager := h.callerManages(r)
	names := map[string]string{}
	out := []PipelineTrigger{}
	for _, t := range triggers {
		if t.PipelineID != "" && t.PipelineID != p.ID {
			continue
		}
		pt := PipelineTrigger{ID: t.ID, AgentID: t.AgentID, AgentName: h.agentName(names, t.AgentID), Events: t.Events,
			Delivery: t.Delivery, Scope: "workspace", Enabled: t.Enabled, CooldownSeconds: t.CooldownSeconds}
		if t.PipelineID != "" {
			pt.Scope = "pipeline"
		}
		if a, err := h.agents.users.GetAgent(t.AgentID); err == nil && callerID != "" && (manager || a.OwnerID == callerID) && visible(r, a) {
			pt.URL, pt.MayManage = t.URL, true
		}
		out = append(out, pt)
	}
	writeJSON(w, http.StatusOK, out)
}

// callerManages is the person calling and whether they may manage every
// agent; an agent calling manages none.
func (h *triggersAPI) callerManages(r *http.Request) (string, bool) {
	claims, _ := r.Context().Value("claims").(*jwt.MapClaims)
	if claims == nil || isAgentClaims(claims) || !AgentIdentitiesEnabled {
		return "", false
	}
	id, _ := (*claims)["sub"].(string)
	return id, h.agents.pc.HasPermission(r, models.PermSettingsManageUsers)
}

func (h *triggersAPI) agentName(cache map[string]string, id string) string {
	if n, ok := cache[id]; ok {
		return n
	}
	n := id
	if a, err := h.agents.users.GetAgent(id); err == nil {
		n = a.Name
	}
	cache[id] = n
	return n
}
