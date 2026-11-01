package api

import (
	"context"
	"net/http"

	"github.com/golang-jwt/jwt/v5"

	"github.com/Tnsor-Labs/brokoli/models"
)

// agentMaySeePipeline answers, with no request in hand, whether agent
// agentID may see pipeline p in workspace workspaceID right now, and why
// not when it may not. A trigger asks it when it is made and again before
// every delivery (ADR-047 section 1), so demoting the owner or disabling
// the agent stops its triggers without anyone editing them.
//
// It does not restate the rules. It builds the request the agent's own
// token would make for the pipeline, with the identity token resolution
// gives it (ResolveAgentToken, agentClaims), its organization and the
// workspace, applies RequestIdentityFunc as the middleware does, and asks
// what every handler asks: the permissions, the organization, the
// workspace.
func (us *UserStore) agentMaySeePipeline(pc *PermissionChecker, agentID, workspaceID string, p *models.Pipeline) (bool, string) {
	if !AgentIdentitiesEnabled {
		return false, "agent identities are turned off on this server"
	}
	a, err := us.GetAgent(agentID)
	if err != nil {
		return false, "the agent no longer exists"
	}
	if a.Disabled {
		return false, "the agent is disabled"
	}
	owner, err := us.GetUserByID(a.OwnerID)
	if err != nil || us.kindOf(a.OwnerID) != UserKindPerson {
		return false, "the agent's owner is no longer an active person"
	}
	if !sameWorkspace(p.WorkspaceID, workspaceID) {
		return false, "the pipeline is in another workspace"
	}

	claims := agentClaims(a.ID, a.Name, a.Role, owner.ID, string(owner.Role))
	return claimsMaySeePipeline(pc, claims, workspaceID, p, "the agent's role, within its owner's,", "the agent's")
}

// personMaySeePipeline is agentMaySeePipeline for a person: whether the
// person with userID may see pipeline p in workspace workspaceID, from the
// identity their own sign-in would carry. An agent asks it before linking
// a pipeline in a message to that person (ADR-048).
func (us *UserStore) personMaySeePipeline(pc *PermissionChecker, userID, workspaceID string, p *models.Pipeline) (bool, string) {
	u, err := us.GetUserByID(userID)
	if err != nil || us.kindOf(userID) != UserKindPerson {
		return false, "not an active person"
	}
	if !sameWorkspace(p.WorkspaceID, workspaceID) {
		return false, "the pipeline is in another workspace"
	}
	claims := jwt.MapClaims{"sub": u.ID, "username": u.Username, "role": string(u.Role)}
	if OrgResolverFunc != nil {
		if orgID := OrgResolverFunc(u.ID); orgID != "" {
			claims["org_id"] = orgID
		}
	}
	return claimsMaySeePipeline(pc, claims, workspaceID, p, "their role", "their")
}

// claimsMaySeePipeline builds the request a token with claims would make
// for pipeline p, applies RequestIdentityFunc as the middleware does, and
// asks what every handler asks: the permissions, the organization, the
// workspace. who and whose word the refusal.
func claimsMaySeePipeline(pc *PermissionChecker, claims jwt.MapClaims, workspaceID string, p *models.Pipeline, who, whose string) (bool, string) {
	ctx := context.WithValue(context.Background(), workspaceKey, workspaceID)
	if orgID, _ := claims["org_id"].(string); orgID != "" {
		ctx = context.WithValue(ctx, OrgIDContextKey{}, orgID)
	}
	ctx = contextWithClaims(ctx, &claims).(context.Context)
	r, err := http.NewRequestWithContext(ctx, http.MethodGet, "/api/pipelines/"+p.ID, nil) // #nosec G704 -- never sent: it only carries the identity the permission checks read
	if err != nil {
		return false, err.Error()
	}
	effective := &claims
	if hook := RequestIdentityFunc; hook != nil {
		refined := hook(r, &claims)
		if refined.Status != 0 {
			return false, "refused: " + refined.Message
		}
		if refined.Claims != nil {
			effective = refined.Claims
			ctx = contextWithClaims(ctx, refined.Claims).(context.Context)
		}
		if refined.Permissions != nil {
			ctx = context.WithValue(ctx, requestPermissionsKey{}, refined.Permissions)
		}
		r = r.WithContext(ctx)
	}

	held := map[models.Permission]bool{}
	for _, perm := range pc.requestPermissions(r, effective) {
		held[perm] = true
	}
	for _, need := range []models.Permission{models.PermPipelinesView, models.PermRunsView} {
		if !held[need] {
			return false, who + " does not grant " + string(need)
		}
	}
	if !ValidateOrgAccess(r, p.OrgID) || !userOwnsWorkspace(r, p.WorkspaceID) {
		return false, "the pipeline is not in " + whose + " organization or workspaces"
	}
	return true, ""
}

// sameWorkspace treats an empty workspace as the default one, as the
// pipelines table does.
func sameWorkspace(a, b string) bool {
	norm := func(ws string) string {
		if ws == "" {
			return models.DefaultWorkspaceID
		}
		return ws
	}
	return norm(a) == norm(b)
}
