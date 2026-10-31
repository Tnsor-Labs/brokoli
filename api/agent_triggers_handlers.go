package api

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"time"

	"github.com/Tnsor-Labs/brokoli/models"
	"github.com/Tnsor-Labs/brokoli/pkg/netguard"
	"github.com/go-chi/chi/v5"
)

// The agent triggers API (ADR-047). Whoever may manage the agent (its
// owner, or settings.manage_users) manages its triggers, in the workspace
// the request acts in.
//
//	GET    /api/agents/{id}/triggers                       its triggers in this workspace
//	POST   /api/agents/{id}/triggers                       make one; the signing secret is in this answer only
//	GET    /api/agents/{id}/triggers/{triggerId}           one
//	PATCH  /api/agents/{id}/triggers/{triggerId}           its events, URL, pipeline, cooldown, enabled
//	DELETE /api/agents/{id}/triggers/{triggerId}           remove it
//	POST   /api/agents/{id}/triggers/{triggerId}/secret    a new signing secret; the old one signs too for 24 hours

type triggersAPI struct {
	agents *agentsAPI
	ts     *TriggerStore
}

func registerAgentTriggerRoutes(r chi.Router, users *UserStore, pc *PermissionChecker, ts *TriggerStore) {
	h := &triggersAPI{agents: &agentsAPI{users: users, pc: pc}, ts: ts}
	r.Get("/api/agents/{id}/triggers", h.list)
	r.Post("/api/agents/{id}/triggers", h.create)
	r.Get("/api/agents/{id}/triggers/{triggerId}", h.get)
	r.Patch("/api/agents/{id}/triggers/{triggerId}", h.update)
	r.Delete("/api/agents/{id}/triggers/{triggerId}", h.remove)
	r.Post("/api/agents/{id}/triggers/{triggerId}/secret", h.rotate)
}

// triggerInput is a trigger as a request writes it; nil leaves a field
// as it is on PATCH.
type triggerInput struct {
	PipelineID      *string  `json:"pipeline_id"`
	Events          []string `json:"events"`
	URL             *string  `json:"url"`
	CooldownSeconds *int     `json:"cooldown_seconds"`
	Enabled         *bool    `json:"enabled"`
}

// trigger is the trigger in the path, of the agent in the path, in the
// request's workspace; otherwise an answer is written.
func (h *triggersAPI) trigger(w http.ResponseWriter, r *http.Request) (*Agent, *AgentTrigger, bool) {
	a, _, _, ok := h.agents.owned(w, r)
	if !ok {
		return nil, nil, false
	}
	t, err := h.ts.Get(a.ID, chi.URLParam(r, "triggerId"))
	if err != nil || !sameWorkspace(t.WorkspaceID, GetWorkspaceID(r)) {
		writeError(w, http.StatusNotFound, ErrTriggerNotFound.Error())
		return nil, nil, false
	}
	return a, t, true
}

func (h *triggersAPI) list(w http.ResponseWriter, r *http.Request) {
	a, _, _, ok := h.agents.owned(w, r)
	if !ok {
		return
	}
	triggers, err := h.ts.List(a.ID, workspaceOf(r))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not list triggers")
		return
	}
	writeJSON(w, http.StatusOK, triggers)
}

func (h *triggersAPI) get(w http.ResponseWriter, r *http.Request) {
	if _, t, ok := h.trigger(w, r); ok {
		writeJSON(w, http.StatusOK, t)
	}
}

func (h *triggersAPI) create(w http.ResponseWriter, r *http.Request) {
	a, callerID, _, ok := h.agents.owned(w, r)
	if !ok {
		return
	}
	if a.Disabled {
		writeError(w, http.StatusConflict, "the agent is disabled")
		return
	}
	var in triggerInput
	if !decodeBody(w, r, &in) {
		return
	}
	t := &AgentTrigger{
		AgentID: a.ID, WorkspaceID: workspaceOf(r), Delivery: "webhook", Events: in.Events,
		CooldownSeconds: int(defaultTriggerCooldown / time.Second), Enabled: true, CreatedBy: callerID,
	}
	if in.URL != nil {
		t.URL = *in.URL
	}
	if in.PipelineID != nil {
		t.PipelineID = *in.PipelineID
	}
	if in.CooldownSeconds != nil {
		t.CooldownSeconds = *in.CooldownSeconds
	}
	if in.Enabled != nil {
		t.Enabled = *in.Enabled
	}
	if msg := h.check(r, a, t); msg != "" {
		writeError(w, http.StatusBadRequest, msg)
		return
	}
	secret, err := h.ts.Create(t)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, err.Error())
		return
	}
	AuditLog(r, "create_trigger", "agent", a.ID, nil, map[string]interface{}{
		"trigger_id": t.ID, "events": t.Events, "pipeline_id": t.PipelineID, "url": t.URL,
	})
	writeJSON(w, http.StatusCreated, map[string]interface{}{
		"trigger": t, "secret": secret,
		"note": "This is the only time the signing secret is shown. The receiver verifies deliveries with it (Standard Webhooks).",
	})
}

func (h *triggersAPI) update(w http.ResponseWriter, r *http.Request) {
	a, t, ok := h.trigger(w, r)
	if !ok {
		return
	}
	var in triggerInput
	if !decodeBody(w, r, &in) {
		return
	}
	before := map[string]interface{}{"events": t.Events, "pipeline_id": t.PipelineID, "url": t.URL,
		"cooldown_seconds": t.CooldownSeconds, "enabled": t.Enabled}
	if in.Events != nil {
		t.Events = in.Events
	}
	if in.URL != nil {
		t.URL = *in.URL
	}
	if in.PipelineID != nil {
		t.PipelineID = *in.PipelineID
	}
	if in.CooldownSeconds != nil {
		t.CooldownSeconds = *in.CooldownSeconds
	}
	if in.Enabled != nil {
		t.Enabled = *in.Enabled
	}
	if msg := h.check(r, a, t); msg != "" {
		writeError(w, http.StatusBadRequest, msg)
		return
	}
	if err := h.ts.Update(t); err != nil {
		writeError(w, http.StatusInternalServerError, "could not update the trigger")
		return
	}
	AuditLog(r, "update_trigger", "agent", a.ID, before, map[string]interface{}{"trigger_id": t.ID, "events": t.Events,
		"pipeline_id": t.PipelineID, "url": t.URL, "cooldown_seconds": t.CooldownSeconds, "enabled": t.Enabled})
	writeJSON(w, http.StatusOK, t)
}

func (h *triggersAPI) remove(w http.ResponseWriter, r *http.Request) {
	a, t, ok := h.trigger(w, r)
	if !ok {
		return
	}
	if err := h.ts.Delete(a.ID, t.ID); err != nil {
		writeError(w, http.StatusInternalServerError, "could not remove the trigger")
		return
	}
	AuditLog(r, "delete_trigger", "agent", a.ID, map[string]interface{}{"trigger_id": t.ID}, nil)
	w.WriteHeader(http.StatusNoContent)
}

func (h *triggersAPI) rotate(w http.ResponseWriter, r *http.Request) {
	a, t, ok := h.trigger(w, r)
	if !ok {
		return
	}
	secret, err := h.ts.RotateSecret(a.ID, t.ID)
	if errors.Is(err, ErrTriggerNotFound) {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, err.Error())
		return
	}
	AuditLog(r, "rotate_trigger_secret", "agent", a.ID, nil, map[string]interface{}{"trigger_id": t.ID})
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"secret": secret,
		"note":   "This is the only time the new secret is shown. For 24 hours deliveries are signed with both the old and the new secret.",
	})
}

// check is why t cannot be saved, or "".
func (h *triggersAPI) check(r *http.Request, a *Agent, t *AgentTrigger) string {
	if msg := validTriggerEvents(t.Events); msg != "" {
		return msg
	}
	if msg := checkTriggerURL(r.Context(), t.URL); msg != "" {
		return msg
	}
	if t.CooldownSeconds < 0 || time.Duration(t.CooldownSeconds)*time.Second > maxTriggerCooldown {
		return "cooldown_seconds must be between 0 and 604800 (7 days)"
	}
	// A trigger on one pipeline needs a pipeline the caller can see, in
	// this workspace. Either way the agent must be able to see what it
	// would be told about: a workspace-wide trigger is checked against
	// the workspace itself, so an agent whose role cannot see pipelines
	// is refused now rather than skipped at every delivery.
	p := &models.Pipeline{WorkspaceID: t.WorkspaceID}
	if t.PipelineID != "" {
		found, err := h.agents.pc.store.GetPipeline(t.PipelineID)
		if err != nil || !sameWorkspace(found.WorkspaceID, t.WorkspaceID) ||
			!ValidateOrgAccess(r, found.OrgID) || !userOwnsWorkspace(r, found.WorkspaceID) {
			return "no pipeline " + t.PipelineID + " in this workspace"
		}
		p = found
	}
	if ok, why := h.agents.users.agentMaySeePipeline(h.agents.pc, a.ID, t.WorkspaceID, p); !ok {
		return "agent " + a.Name + " would not be allowed to act on it: " + why
	}
	return ""
}

// checkTriggerURL is why raw cannot receive deliveries, or "".
func checkTriggerURL(ctx context.Context, raw string) string {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
		return "url must be an http or https URL that receives the deliveries"
	}
	if u.User != nil {
		return "url must not carry credentials; deliveries are signed instead"
	}
	if err := netguard.Outbound().CheckHost(ctx, u.Hostname()); err != nil {
		return "url is not allowed by this server's outbound network policy: " + err.Error()
	}
	return ""
}

// workspaceOf is the request's workspace, the default one when none.
func workspaceOf(r *http.Request) string {
	if ws := GetWorkspaceID(r); ws != "" {
		return ws
	}
	return models.DefaultWorkspaceID
}
