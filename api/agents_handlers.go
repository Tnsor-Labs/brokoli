package api

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/Tnsor-Labs/brokoli/models"
	"github.com/go-chi/chi/v5"
	"github.com/golang-jwt/jwt/v5"
)

// The agents API (ADR-046 phase 2).
//
//	GET    /api/agents                          agents you own; every agent for settings.manage_users
//	POST   /api/agents                          create one (settings.manage_users)
//	GET    /api/agents/{id}                     one (its owner, or settings.manage_users)
//	PATCH  /api/agents/{id}                     its role and description (settings.manage_users)
//	DELETE /api/agents/{id}                     disable it and revoke its tokens (its owner, or settings.manage_users)
//	GET    /api/agents/{id}/tokens              its tokens, never the secrets
//	POST   /api/agents/{id}/tokens              issue one; the token is in this answer only
//	DELETE /api/agents/{id}/tokens/{tokenId}    revoke one
//
// Only people manage agents: an agent's own token is refused on every one
// of these routes.

// AgentHooks let a distribution take part in agents' lives: a
// multi-tenant one makes an agent a member of its owner's organization when
// it is created, keeps that membership's role in step, ends it when the
// agent is disabled, and shows each organization only its own agents. Any
// hook left nil does nothing.
var AgentHooks struct {
	// Created runs after an agent is recorded, as the person who created
	// it; an error disables the agent again and is the answer (a refused
	// limit, say: return an *AgentHookError to choose the status).
	Created func(r *http.Request, a *Agent) error
	// Updated runs after an agent's role or description changes.
	Updated func(r *http.Request, a *Agent) error
	// Disabled runs after an agent is disabled and its tokens revoked.
	Disabled func(r *http.Request, a *Agent)
	// Visible reports whether the caller may see a (beyond being its
	// owner or managing users): an agent of another organization is not
	// there at all.
	Visible func(r *http.Request, a *Agent) bool
	// Scope is where a new agent's name must be unique, for the request
	// creating it (an organization, say); unset, the whole server.
	Scope func(r *http.Request) string
}

// AgentHookError is a hook's refusal, with the status to answer.
type AgentHookError struct {
	Status  int
	Message string
}

func (e *AgentHookError) Error() string { return e.Message }

func hookStatus(err error) (int, string) {
	var he *AgentHookError
	if errors.As(err, &he) {
		return he.Status, he.Message
	}
	return http.StatusInternalServerError, err.Error()
}

func visible(r *http.Request, a *Agent) bool {
	return AgentHooks.Visible == nil || AgentHooks.Visible(r, a)
}

type agentsAPI struct {
	users *UserStore
	pc    *PermissionChecker
}

func registerAgentRoutes(r chi.Router, users *UserStore, pc *PermissionChecker) {
	h := &agentsAPI{users: users, pc: pc}
	r.Get("/api/agents", h.list)
	r.Post("/api/agents", h.create)
	r.Get("/api/agents/{id}", h.get)
	r.Patch("/api/agents/{id}", h.update)
	r.Delete("/api/agents/{id}", h.disable)
	r.Get("/api/agents/{id}/tokens", h.listTokens)
	r.Post("/api/agents/{id}/tokens", h.createToken)
	r.Delete("/api/agents/{id}/tokens/{tokenId}", h.revokeToken)
}

// caller is the person making the request, or an answer already written.
func (h *agentsAPI) caller(w http.ResponseWriter, r *http.Request) (id string, admin, ok bool) {
	if !AgentIdentitiesEnabled {
		writeError(w, http.StatusNotFound, "agent identities are not available on this server")
		return "", false, false
	}
	claims, _ := r.Context().Value("claims").(*jwt.MapClaims)
	if claims == nil {
		writeError(w, http.StatusUnauthorized, "authentication required")
		return "", false, false
	}
	if isAgentClaims(claims) {
		writeError(w, http.StatusForbidden, "an agent cannot manage agents; a person does")
		return "", false, false
	}
	id, _ = (*claims)["sub"].(string)
	return id, h.pc.HasPermission(r, models.PermSettingsManageUsers), true
}

// owned is the agent in the path when the caller owns it or may manage
// every agent; otherwise an answer is written. Someone else's agent is
// "not found", not "forbidden": its existence is not theirs to learn.
func (h *agentsAPI) owned(w http.ResponseWriter, r *http.Request) (*Agent, string, bool, bool) {
	callerID, admin, ok := h.caller(w, r)
	if !ok {
		return nil, "", false, false
	}
	a, err := h.users.GetAgent(chi.URLParam(r, "id"))
	if err != nil || (!admin && a.OwnerID != callerID) || !visible(r, a) {
		writeError(w, http.StatusNotFound, ErrAgentNotFound.Error())
		return nil, "", false, false
	}
	return a, callerID, admin, true
}

func decodeBody(w http.ResponseWriter, r *http.Request, into any) bool {
	if err := json.NewDecoder(io.LimitReader(r.Body, 64<<10)).Decode(into); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON")
		return false
	}
	return true
}

func (h *agentsAPI) list(w http.ResponseWriter, r *http.Request) {
	callerID, admin, ok := h.caller(w, r)
	if !ok {
		return
	}
	owner := callerID
	if admin {
		owner = ""
	}
	agents, err := h.users.ListAgents(owner)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not list agents")
		return
	}
	shown := make([]Agent, 0, len(agents))
	for i := range agents {
		if visible(r, &agents[i]) {
			shown = append(shown, agents[i])
		}
	}
	writeJSON(w, http.StatusOK, shown)
}

// checkRole is why role cannot be an agent's, or "".
func (h *agentsAPI) checkRole(role string) string {
	if !assignableAgentRole(role) {
		return "an agent cannot be an administrator; give it the agent role, or another"
	}
	if _, err := h.pc.store.GetRole(role); err != nil {
		return "no role " + role
	}
	return ""
}

func (h *agentsAPI) create(w http.ResponseWriter, r *http.Request) {
	callerID, admin, ok := h.caller(w, r)
	if !ok {
		return
	}
	if !admin {
		writeError(w, http.StatusForbidden, "insufficient permissions: "+string(models.PermSettingsManageUsers))
		return
	}
	var req struct {
		Name        string `json:"name"`
		Description string `json:"description"`
		Role        string `json:"role"`
		OwnerID     string `json:"owner_id"`
	}
	if !decodeBody(w, r, &req) {
		return
	}
	if req.Role == "" {
		req.Role = AgentRole
	}
	if why := h.checkRole(req.Role); why != "" {
		writeError(w, http.StatusBadRequest, why)
		return
	}
	if req.OwnerID == "" {
		req.OwnerID = callerID
	}
	if owner, err := h.users.GetUserByID(req.OwnerID); err != nil || owner == nil || h.users.kindOf(owner.ID) != UserKindPerson {
		writeError(w, http.StatusBadRequest, "the owner must be a person with an account")
		return
	}
	scope := ""
	if AgentHooks.Scope != nil {
		scope = AgentHooks.Scope(r)
	}
	a, err := h.users.CreateAgent(strings.TrimSpace(req.Name), strings.TrimSpace(req.Description), req.Role, req.OwnerID, scope)
	switch {
	case errors.Is(err, ErrUserExists):
		writeError(w, http.StatusConflict, "an agent named "+req.Name+" exists already")
		return
	case err != nil && strings.HasPrefix(err.Error(), "an agent's name"):
		writeError(w, http.StatusBadRequest, err.Error())
		return
	case err != nil:
		writeError(w, http.StatusInternalServerError, "could not create the agent")
		return
	}
	if AgentHooks.Created != nil {
		if err := AgentHooks.Created(r, a); err != nil {
			_ = h.users.discardAgent(a.ID)
			status, msg := hookStatus(err)
			writeError(w, status, msg)
			return
		}
	}
	AuditLog(r, "create", "agent", a.ID, nil, map[string]interface{}{"name": a.Name, "role": a.Role, "owner_id": a.OwnerID})
	writeJSON(w, http.StatusCreated, a)
}

func (h *agentsAPI) get(w http.ResponseWriter, r *http.Request) {
	if a, _, _, ok := h.owned(w, r); ok {
		writeJSON(w, http.StatusOK, a)
	}
}

func (h *agentsAPI) update(w http.ResponseWriter, r *http.Request) {
	a, _, admin, ok := h.owned(w, r)
	if !ok {
		return
	}
	var req struct {
		Role        *string `json:"role"`
		Description *string `json:"description"`
		Avatar      *string `json:"avatar"`
	}
	if !decodeBody(w, r, &req) {
		return
	}
	// The agent's owner picks its avatar; its role and description are for
	// whoever manages users.
	if (req.Role != nil || req.Description != nil) && !admin {
		writeError(w, http.StatusForbidden, "insufficient permissions: "+string(models.PermSettingsManageUsers))
		return
	}
	if req.Avatar != nil {
		if *req.Avatar != "" && !validAgentAvatar(*req.Avatar) {
			writeError(w, http.StatusBadRequest, "no avatar "+*req.Avatar+"; an agent's avatar is one of "+strings.Join(AgentAvatars, ", ")+", or empty for the default")
			return
		}
		if err := h.users.SetAgentAvatar(a.ID, *req.Avatar); err != nil {
			writeError(w, http.StatusInternalServerError, "could not update the agent")
			return
		}
		if req.Role == nil && req.Description == nil {
			updated, _ := h.users.GetAgent(a.ID)
			AuditLog(r, "update", "agent", a.ID, map[string]interface{}{"avatar": a.Avatar}, map[string]interface{}{"avatar": *req.Avatar})
			writeJSON(w, http.StatusOK, updated)
			return
		}
	}
	role, description := a.Role, a.Description
	if req.Role != nil {
		if why := h.checkRole(*req.Role); why != "" {
			writeError(w, http.StatusBadRequest, why)
			return
		}
		role = *req.Role
	}
	if req.Description != nil {
		description = strings.TrimSpace(*req.Description)
	}
	if err := h.users.UpdateAgent(a.ID, role, description); err != nil {
		writeError(w, http.StatusInternalServerError, "could not update the agent")
		return
	}
	updated, _ := h.users.GetAgent(a.ID)
	if AgentHooks.Updated != nil && updated != nil {
		if err := AgentHooks.Updated(r, updated); err != nil {
			status, msg := hookStatus(err)
			writeError(w, status, msg)
			return
		}
	}
	AuditLog(r, "update", "agent", a.ID, map[string]interface{}{"role": a.Role}, map[string]interface{}{"role": role})
	writeJSON(w, http.StatusOK, updated)
}

func (h *agentsAPI) disable(w http.ResponseWriter, r *http.Request) {
	a, _, _, ok := h.owned(w, r)
	if !ok {
		return
	}
	if err := h.users.DisableAgent(a.ID); err != nil {
		writeError(w, http.StatusInternalServerError, "could not disable the agent")
		return
	}
	if AgentHooks.Disabled != nil {
		AgentHooks.Disabled(r, a)
	}
	AuditLog(r, "disable", "agent", a.ID, nil, map[string]interface{}{"name": a.Name})
	w.WriteHeader(http.StatusNoContent)
}

func (h *agentsAPI) listTokens(w http.ResponseWriter, r *http.Request) {
	a, _, _, ok := h.owned(w, r)
	if !ok {
		return
	}
	tokens, err := h.users.ListAgentTokens(a.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not list the tokens")
		return
	}
	writeJSON(w, http.StatusOK, tokens)
}

func (h *agentsAPI) createToken(w http.ResponseWriter, r *http.Request) {
	a, callerID, _, ok := h.owned(w, r)
	if !ok {
		return
	}
	if a.Disabled {
		writeError(w, http.StatusConflict, "the agent is disabled")
		return
	}
	var req struct {
		Name          string `json:"name"`
		ExpiresInDays int    `json:"expires_in_days"`
		OnBehalfOf    bool   `json:"on_behalf_of_owner"`
	}
	if !decodeBody(w, r, &req) {
		return
	}
	// Only the owner can say the agent acts for them.
	if req.OnBehalfOf && callerID != a.OwnerID {
		writeError(w, http.StatusForbidden, "only the agent's owner can issue a token that acts on their behalf")
		return
	}
	token, rec, err := h.users.CreateAgentToken(a.ID, req.Name, callerID, req.ExpiresInDays, req.OnBehalfOf)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	AuditLog(r, "create_token", "agent", a.ID, nil, map[string]interface{}{"token_id": rec.ID, "name": rec.Name, "expires_at": rec.ExpiresAt})
	writeJSON(w, http.StatusCreated, map[string]interface{}{
		"token": token, "record": rec,
		"note": "This is the only time the token is shown. Store it where the agent reads its credentials.",
	})
}

func (h *agentsAPI) revokeToken(w http.ResponseWriter, r *http.Request) {
	a, _, _, ok := h.owned(w, r)
	if !ok {
		return
	}
	tokenID := chi.URLParam(r, "tokenId")
	if err := h.users.RevokeAgentToken(a.ID, tokenID); err != nil {
		writeError(w, http.StatusNotFound, ErrTokenNotFound.Error())
		return
	}
	AuditLog(r, "revoke_token", "agent", a.ID, nil, map[string]interface{}{"token_id": tokenID})
	w.WriteHeader(http.StatusNoContent)
}
