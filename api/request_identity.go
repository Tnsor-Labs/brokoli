package api

import (
	"net/http"

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
// It returns the claims the rest of the request sees, which may be the
// same claims or new ones; every permission check and handler reads those.
// A non-zero status refuses the request with that status and message
// instead, before any handler runs.
//
// Requests without an identity never reach it: anonymous and open-mode
// requests are unaffected.
var RequestIdentityFunc func(r *http.Request, claims *jwt.MapClaims) (refined *jwt.MapClaims, status int, message string)

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
		refined, status, message := hook(r, claims)
		if status != 0 {
			writeJSON(w, status, map[string]string{"error": message})
			return
		}
		if refined != nil && refined != claims {
			r = r.WithContext(contextWithClaims(r.Context(), refined))
		}
		next.ServeHTTP(w, r)
	})
}
