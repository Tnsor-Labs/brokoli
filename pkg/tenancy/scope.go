// Package tenancy is the one place the runtime decides which organization
// and workspace data a request may see (ADR-049).
//
// A Scope is computed once per request from facts the middleware chain has
// already checked: the caller's organization, the current workspace, and
// every workspace the caller belongs to. Data access goes through the
// scoped store, which applies two rules and nothing else:
//
//   - a list shows the current workspace only (Lists);
//   - an item fetched by id is visible when it is in any workspace the
//     caller belongs to (Sees), so links between a person's own workspaces
//     keep working. Anyone else is answered "not found".
//
// Each resource declares once, in the registry, how its workspace is found
// (registry.go), and the SQL filters are built from that declaration, so no
// query carries a hand-written tenant filter to forget.
package tenancy

import (
	"context"
	"slices"
)

// DefaultWorkspace is the workspace of a single-tenant install and of rows
// written before workspaces existed. It mirrors models.DefaultWorkspaceID,
// which this package cannot import.
const DefaultWorkspace = "default"

// Actor is who a request acts as.
type Actor string

const (
	ActorPerson Actor = "person"
	ActorAgent  Actor = "agent"
	// ActorOperator is reserved for support operators reading across
	// organizations (ADR-049 section 6); nothing builds it yet.
	ActorOperator Actor = "operator"
)

// Scope is what one request may see.
//
// The zero value is not a permissive scope: it is invalid and sees
// nothing. Only New, OrgOnly and Unrestricted make a usable one.
type Scope struct {
	OrgID     string
	Workspace string
	Member    []string
	Actor     Actor

	valid    bool
	tenanted bool // an organization to keep to
	byWS     bool // and workspaces within it
}

// New is the scope of a request in a multi-tenant deployment: an
// organization, the current workspace (already checked to be one of
// member), and every workspace the caller belongs to.
func New(orgID, workspace string, member []string, actor Actor) Scope {
	return Scope{
		OrgID:     orgID,
		Workspace: workspace,
		Member:    slices.Clone(member),
		Actor:     actor,
		valid:     true,
		tenanted:  orgID != "",
		byWS:      orgID != "",
	}
}

// OrgOnly is the scope of a deployment that separates organizations but
// keeps no workspace registry: the organization rule applies, and every
// workspace in it is the caller's.
func OrgOnly(orgID, workspace string, actor Actor) Scope {
	if workspace == "" {
		workspace = DefaultWorkspace
	}
	return Scope{OrgID: orgID, Workspace: workspace, Actor: actor, valid: true, tenanted: orgID != ""}
}

// Unrestricted is the scope of a deployment with no tenancy to enforce:
// the community edition, or a single tenant with one workspace. Every rule
// passes, exactly as the handlers behaved there before this package.
func Unrestricted(workspace string) Scope {
	if workspace == "" {
		workspace = DefaultWorkspace
	}
	return Scope{Workspace: workspace, valid: true}
}

// Valid reports whether the scope was built by New or Unrestricted.
func (s Scope) Valid() bool { return s.valid }

// Tenanted reports whether rules are enforced at all.
func (s Scope) Tenanted() bool { return s.valid && s.tenanted }

// Lists reports whether a row in workspace ws belongs in a list made for
// this request: the current workspace only.
func (s Scope) Lists(orgID, ws string) bool {
	if !s.valid {
		return false
	}
	if !s.tenanted {
		return true
	}
	if !s.byWS {
		return orgID == "" || orgID == s.OrgID
	}
	return orgID == s.OrgID && ws == s.Workspace
}

// Sees reports whether an item in organization orgID and workspace ws may
// be fetched by id: in the caller's organization and in a workspace they
// belong to. A row with no organization, or in the default workspace, is
// governed by the organization alone, as it was before workspaces: such
// rows predate them and have no other owner to check.
func (s Scope) Sees(orgID, ws string) bool {
	if !s.valid {
		return false
	}
	if !s.tenanted {
		return true
	}
	if orgID != "" && orgID != s.OrgID {
		return false
	}
	if !s.byWS || ws == "" || ws == DefaultWorkspace {
		return true
	}
	return slices.Contains(s.Member, ws)
}

type scopeKey struct{}

// WithScope returns ctx carrying s.
func WithScope(ctx context.Context, s Scope) context.Context {
	return context.WithValue(ctx, scopeKey{}, s)
}

// From returns the scope ctx carries.
func From(ctx context.Context) (Scope, bool) {
	s, ok := ctx.Value(scopeKey{}).(Scope)
	return s, ok && s.valid
}
