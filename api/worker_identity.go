package api

import (
	"context"
	"net/http"
	"strings"
)

// The identity a worker presents to the data-plane blob endpoints.
//
// Those endpoints have their own authorization model: the capability
// says which single object may be touched, and the caller's credential
// says which tenant it acts for. Verification compares two
// independently-derived descriptions, so both are required and neither
// substitutes for the other.
//
// A worker cannot produce a session. It holds an opaque, revocable token
// instead, and core deliberately does not know that token's shape: the
// enterprise build registers a resolver, exactly as it does for
// OrgResolverFunc and NodeTypeGateFunc.

// WorkerTokenOrgResolver resolves an opaque worker credential to the org
// it acts for, reporting whether it recognised the token at all.
//
// Consulted ONLY by the blob endpoints below. That confinement is the
// point and is not an implementation detail: this credential is handed
// to a machine, and in a hybrid deployment to a machine we do not
// operate, so it must not be able to authenticate anything else.
//
// Teaching the shared auth middleware to accept it instead would be far
// simpler and materially unsafe, because requirePerm's open-source
// fallback denies only "viewer" on writes and lets any unrecognised role
// through every other gate (#527). A worker identity stamped there would
// reach the whole API.
var WorkerTokenOrgResolver func(token string) (orgID string, ok bool)

// blobAuth authenticates a data-plane blob request as either an ordinary
// session or a worker presenting its opaque token.
//
// Ordering matters: a session is tried first, so a human request behaves
// exactly as it did before this existed and the worker path can never
// shadow it.
func blobAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Already authenticated upstream (a real session, or a static API
		// key that stamped claims). Nothing to add.
		if GetOrgIDFromRequest(r) != "" {
			next.ServeHTTP(w, r)
			return
		}

		token := bearerToken(r)
		if token == "" || WorkerTokenOrgResolver == nil {
			// No credential, or a deployment with no worker identity
			// configured. The endpoint's own check reports this as
			// missing tenant rather than guessing one.
			next.ServeHTTP(w, r)
			return
		}
		orgID, ok := WorkerTokenOrgResolver(token)
		if !ok || orgID == "" {
			next.ServeHTTP(w, r)
			return
		}
		// Only the tenant is stamped, deliberately. No role, no subject,
		// no claims: this identity exists to answer "which org" for a
		// capability check and must not read as a user anywhere.
		next.ServeHTTP(w, r.WithContext(
			context.WithValue(r.Context(), OrgIDContextKey{}, orgID)))
	})
}

func bearerToken(r *http.Request) string {
	h := r.Header.Get("Authorization")
	if !strings.HasPrefix(h, "Bearer ") {
		return ""
	}
	return strings.TrimPrefix(h, "Bearer ")
}
