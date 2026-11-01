package api

import (
	"net/http"

	"github.com/Tnsor-Labs/brokoli/models"
	"github.com/Tnsor-Labs/brokoli/pkg/tenancy"
	"github.com/Tnsor-Labs/brokoli/store"
	"github.com/golang-jwt/jwt/v5"
)

// RequestScope is what this request may see (ADR-049): the one place the
// tenant rules are derived, for every handler.
//
// WorkspaceMiddleware computes the workspace facts once and carries them
// in the context. The organization is read at call time, because an
// embedding application's own middleware may set it later in the chain.
// A request that never met the middleware (a handler mounted directly, as
// tests do) gets the same scope computed here, so the rules do not depend
// on how a handler was reached.
func RequestScope(r *http.Request) tenancy.Scope {
	orgID := GetOrgIDFromRequest(r)
	cached, ok := tenancy.From(r.Context())
	if ok && cached.OrgID == orgID {
		return cached
	}
	if ok {
		return scopeFor(orgID, cached.Workspace, cached.Member, cached.Actor)
	}
	return computeScope(r, orgID)
}

// scopedStore is s as this request may use it.
func scopedStore(s store.Store, r *http.Request) store.ScopedStore {
	return store.Scoped(s, RequestScope(r))
}

func scopeFor(orgID, workspace string, member []string, actor tenancy.Actor) tenancy.Scope {
	// No organization (community edition): nothing to keep apart.
	if orgID == "" {
		return tenancy.Unrestricted(workspace)
	}
	// Organizations but no workspace registry: the organization rule
	// alone, as ValidateOrgAccess applied it.
	if UserWorkspaceResolverFunc == nil {
		return tenancy.OrgOnly(orgID, workspace, actor)
	}
	return tenancy.New(orgID, workspace, member, actor)
}

func computeScope(r *http.Request, orgID string) tenancy.Scope {
	if orgID == "" {
		return tenancy.Unrestricted(GetWorkspaceID(r))
	}
	if UserWorkspaceResolverFunc == nil {
		return tenancy.OrgOnly(orgID, GetWorkspaceID(r), requestActor(r))
	}
	userID := getUserIDFromRequest(r)
	if userID == "" {
		return tenancy.Scope{} // sees nothing
	}
	member := UserWorkspaceResolverFunc(userID)
	ws := GetWorkspaceID(r)
	if ws == models.DefaultWorkspaceID && len(member) > 0 {
		ws = member[0]
	}
	return tenancy.New(orgID, ws, member, requestActor(r))
}

func requestActor(r *http.Request) tenancy.Actor {
	if claims, _ := r.Context().Value("claims").(*jwt.MapClaims); claims != nil {
		if kind, _ := (*claims)["kind"].(string); kind == UserKindAgent {
			return tenancy.ActorAgent
		}
	}
	return tenancy.ActorPerson
}
