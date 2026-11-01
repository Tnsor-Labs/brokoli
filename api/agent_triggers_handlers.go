package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/Tnsor-Labs/brokoli/models"
	"github.com/Tnsor-Labs/brokoli/pkg/netguard"
	"github.com/go-chi/chi/v5"
	"github.com/golang-jwt/jwt/v5"
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
//	GET    /api/agents/{id}/triggers/{triggerId}/deliveries                        its newest deliveries, every attempt
//	POST   /api/agents/{id}/triggers/{triggerId}/deliveries/{deliveryId}/redeliver  send one again, now
//	POST   /api/agents/{id}/triggers/{triggerId}/test                               deliver a test run.failed

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
	r.Get("/api/agents/{id}/triggers/{triggerId}/deliveries", h.deliveries)
	r.Post("/api/agents/{id}/triggers/{triggerId}/deliveries/{deliveryId}/redeliver", h.redeliver)
	r.Post("/api/agents/{id}/triggers/{triggerId}/test", h.test)
	r.Get("/api/agents/me/events", h.feed)
	r.Get("/api/agents/trigger-events", h.eventTypes)
	r.Get("/api/runs/{id}/agents", h.runNotices)
	r.Get("/api/pipelines/{id}/triggers", h.pipelineTriggers)
}

const (
	feedDefaultLimit = 50
	feedMaxLimit     = 200
)

// feed is the calling agent's waiting events, for triggers that deliver to
// a feed (ADR-047 section 6): GET /api/agents/me/events?after=<cursor>.
// Passing a cursor acknowledges every event up to it, so an event the
// agent did not get to comes back on the next read. Access is checked
// now, as a webhook delivery's is just before it is sent.
func (h *triggersAPI) feed(w http.ResponseWriter, r *http.Request) {
	if !AgentIdentitiesEnabled {
		writeError(w, http.StatusNotFound, "agent identities are not available on this server")
		return
	}
	claims, _ := r.Context().Value("claims").(*jwt.MapClaims)
	if claims == nil {
		writeError(w, http.StatusUnauthorized, "authentication required")
		return
	}
	if !isAgentClaims(claims) {
		writeError(w, http.StatusForbidden, "only an agent has an event feed; a person reads a trigger's deliveries")
		return
	}
	agentID, _ := (*claims)["sub"].(string)
	after, err := ParseFeedPosition(r.URL.Query().Get("after"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "after: "+err.Error())
		return
	}
	limit := feedDefaultLimit
	if v, err := strconv.Atoi(r.URL.Query().Get("limit")); err == nil && v > 0 {
		limit = min(v, feedMaxLimit)
	}
	now := time.Now().UTC()
	if err := h.ts.FeedAcknowledge(agentID, after, now); err != nil {
		writeError(w, http.StatusInternalServerError, "could not acknowledge the events read")
		return
	}
	pending, err := h.ts.FeedPending(agentID, after, limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not read the feed")
		return
	}
	events := []json.RawMessage{}
	cursor := after
	for i := range pending {
		d := &pending[i]
		cursor = d.Position()
		if why := h.feedRefusal(d); why != "" {
			_ = h.ts.Skip(d, why, now)
			continue
		}
		events = append(events, json.RawMessage(d.payload))
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"events": events, "cursor": cursor.String()})
}

// feedRefusal is why d may not be read by its agent now, or "".
func (h *triggersAPI) feedRefusal(d *TriggerDelivery) string {
	t, err := h.ts.GetByID(d.TriggerID)
	if err != nil {
		return "the trigger was removed"
	}
	if !t.Enabled {
		return "the trigger is disabled"
	}
	return deliveryRefusal(h.agents.pc.store, h.agents.users, h.agents.pc, d, t)
}

// test queues a synthetic run.failed, marked "test", about the trigger's
// pipeline or the first pipeline in its workspace the agent may see, so a
// receiver can be wired up without waiting for a real failure. It ignores
// the cooldown, and goes out like any other delivery.
func (h *triggersAPI) test(w http.ResponseWriter, r *http.Request) {
	a, t, ok := h.trigger(w, r)
	if !ok {
		return
	}
	p, why := h.testPipeline(t)
	if p == nil {
		writeError(w, http.StatusConflict, why)
		return
	}
	now := time.Now().UTC()
	run := &models.Run{ID: "test-" + generateID()[:16], PipelineID: p.ID, Status: models.RunStatusFailed,
		Error: "This is a test event from Brokoli: no run failed. A real run.failed carries the run's error here."}
	del := &TriggerDelivery{ID: newDeliveryID(), TriggerID: t.ID, AgentID: t.AgentID, WorkspaceID: t.WorkspaceID,
		PipelineID: p.ID, RunID: run.ID, Event: TriggerEventRunFailed}
	d := &triggerDispatcher{s: h.agents.pc.store, users: h.agents.users, publicURL: strings.TrimRight(oauthPublicURL, "/")}
	body, err := d.eventBody(del, t, p, run, models.RunEvent{CreatedAt: now}, true)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not build the test event")
		return
	}
	if _, err := h.ts.Enqueue(del, string(body), 0, now); err != nil {
		writeError(w, http.StatusInternalServerError, "could not queue the test event")
		return
	}
	AuditLog(r, "test_trigger", "agent", a.ID, nil, map[string]interface{}{"trigger_id": t.ID, "delivery_id": del.ID})
	writeJSON(w, http.StatusAccepted, map[string]string{"delivery_id": del.ID, "pipeline_id": p.ID,
		"note": "Queued; it is sent within a few seconds. The event carries \"test\": true."})
}

// testPipeline is the pipeline a test event is about, or why there is none.
func (h *triggersAPI) testPipeline(t *AgentTrigger) (*models.Pipeline, string) {
	candidates := []models.Pipeline{}
	if t.PipelineID != "" {
		if p, err := h.agents.pc.store.GetPipeline(t.PipelineID); err == nil {
			candidates = append(candidates, *p)
		}
	} else if all, err := h.agents.pc.store.ListPipelines(); err == nil {
		candidates = all
	}
	for i := range candidates {
		p := &candidates[i]
		if !sameWorkspace(p.WorkspaceID, t.WorkspaceID) {
			continue
		}
		if ok, _ := h.agents.users.agentMaySeePipeline(h.agents.pc, t.AgentID, t.WorkspaceID, p); ok {
			return p, ""
		}
	}
	return nil, "there is no pipeline in this workspace the agent may see, so there is nothing to make a test event about"
}

func (h *triggersAPI) deliveries(w http.ResponseWriter, r *http.Request) {
	_, t, ok := h.trigger(w, r)
	if !ok {
		return
	}
	out, err := h.ts.Deliveries(t.ID, 50)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not list deliveries")
		return
	}
	if out == nil {
		out = []TriggerDelivery{}
	}
	writeJSON(w, http.StatusOK, out)
}

func (h *triggersAPI) redeliver(w http.ResponseWriter, r *http.Request) {
	a, t, ok := h.trigger(w, r)
	if !ok {
		return
	}
	id := chi.URLParam(r, "deliveryId")
	if err := h.ts.Redeliver(t.ID, id, time.Now()); err != nil {
		writeError(w, http.StatusNotFound, ErrDeliveryNotFound.Error())
		return
	}
	AuditLog(r, "redeliver_trigger_event", "agent", a.ID, nil, map[string]interface{}{"trigger_id": t.ID, "delivery_id": id})
	writeJSON(w, http.StatusAccepted, map[string]string{"status": DeliveryPending, "note": "Queued; it is sent within a few seconds."})
}

// triggerInput is a trigger as a request writes it; nil leaves a field
// as it is on PATCH.
type triggerInput struct {
	Delivery        *string  `json:"delivery"`
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
		AgentID: a.ID, WorkspaceID: workspaceOf(r), Delivery: DeliveryWebhook, Events: in.Events,
		CooldownSeconds: int(defaultTriggerCooldown / time.Second), Enabled: true, CreatedBy: callerID,
	}
	if in.Delivery != nil {
		t.Delivery = *in.Delivery
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
	if t.Delivery == DeliveryFeed {
		writeJSON(w, http.StatusCreated, map[string]interface{}{
			"trigger": t,
			"note":    "The agent reads its events with its own token: GET /api/agents/me/events, or the next_events MCP tool.",
		})
		return
	}
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
	if in.Delivery != nil && *in.Delivery != t.Delivery {
		writeError(w, http.StatusBadRequest, "a trigger's delivery cannot change; make a new trigger and remove this one")
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
	if errors.Is(err, ErrNoSecret) {
		writeError(w, http.StatusConflict, err.Error())
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
	switch t.Delivery {
	case DeliveryWebhook:
		if msg := checkTriggerURL(r.Context(), t.URL); msg != "" {
			return msg
		}
	case DeliveryFeed:
		if t.URL != "" {
			return "a feed trigger has no url: its agent reads GET /api/agents/me/events"
		}
	default:
		return `delivery must be "webhook" (posted to url, signed) or "feed" (read by the agent)`
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
			!pipelineVisible(r, found) {
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
