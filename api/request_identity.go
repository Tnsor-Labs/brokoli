package api

import (
	"context"
	"net/http"

	"github.com/Tnsor-Labs/brokoli/models"
	"github.com/golang-jwt/jwt/v5"
)

// RequestIdentityFunc, when set, sees the identity of every request that
// carries one, after authentication has resolved its organization and
// workspace and before any handler runs.
//
// A signed token is a statement made when it was issued. What it says about
// the caller's organization and role can stop being true long before it
// expires: a membership is removed, a role is changed. An embedding
// application that keeps memberships of its own uses this to answer from
// them on every request instead of from the token.
//
// Requests without an identity never reach it: anonymous and open-mode
// requests are unaffected.
var RequestIdentityFunc func(r *http.Request, claims *jwt.MapClaims) IdentityRefinement

// IdentityRefinement is what RequestIdentityFunc answers for one request.
type IdentityRefinement struct {
	// Claims the rest of the request sees, which every permission check
	// and handler reads. Nil keeps the request's own.
	Claims *jwt.MapClaims

	// Permissions, when not nil, are the request's permissions: every
	// permission check reads them instead of looking the role claim up in
	// the store. An application whose roles the store does not hold, such
	// as roles defined per organization, resolves them itself and answers
	// here. An empty, non-nil list grants nothing.
	Permissions []models.Permission

	// Status, when not zero, refuses the request with Status and Message
	// before any handler runs.
	Status  int
	Message string
}

type requestPermissionsKey struct{}

// RequestPermissions returns the permissions RequestIdentityFunc resolved
// for this request, and false when it resolved none, in which case the
// role claim decides.
func RequestPermissions(r *http.Request) ([]models.Permission, bool) {
	perms, ok := r.Context().Value(requestPermissionsKey{}).([]models.Permission)
	return perms, ok
}

// RequestIdentity applies RequestIdentityFunc. It is installed after
// WorkspaceMiddleware, because the identity it refines includes the
// workspace the request was resolved to.
func RequestIdentity(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hook := RequestIdentityFunc
		claims, _ := r.Context().Value("claims").(*jwt.MapClaims)
		if hook == nil || claims == nil {
			next.ServeHTTP(w, r)
			return
		}
		refined := hook(r, claims)
		if refined.Status != 0 {
			writeJSON(w, refined.Status, map[string]string{"error": refined.Message})
			return
		}
		ctx := r.Context()
		if refined.Claims != nil && refined.Claims != claims {
			ctx = contextWithClaims(ctx, refined.Claims)
		}
		if refined.Permissions != nil {
			ctx = context.WithValue(ctx, requestPermissionsKey{}, refined.Permissions)
		}
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}
