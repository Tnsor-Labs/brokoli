package api

import (
	"net/http"

	"github.com/Tnsor-Labs/brokoli/store"
)

// WriteJSONPublic is an exported wrapper for writeJSON, used by enterprise extensions.
func WriteJSONPublic(w http.ResponseWriter, status int, data interface{}) {
	writeJSON(w, status, data)
}

// WriteErrorPublic is an exported wrapper for writeError, used by enterprise extensions.
func WriteErrorPublic(w http.ResponseWriter, status int, message string) {
	writeError(w, status, message)
}

// ValidatePassword is an exported wrapper for validatePassword, used by enterprise signup.
func ValidatePassword(password string) error {
	return validatePassword(password)
}

// ParsePageParamsPublic is an exported wrapper for ParsePageParams, used by enterprise handlers.
func ParsePageParamsPublic(r *http.Request) store.PageParams {
	return ParsePageParams(r)
}

// PaginatePublic is an exported wrapper for PaginateSlice, used by enterprise handlers.
func PaginatePublic(items interface{}, total int, params store.PageParams) store.PageResult {
	return PaginateSlice(items, total, params)
}

// OrgIDContextKey is the context key used by enterprise OrgMiddleware to store the org ID.
// Enterprise sets this; open source reads it for multi-tenant scoping.
type OrgIDContextKey struct{}

// OrgResolverFunc resolves the org ID for a given user ID.
// Set by enterprise platform to include org_id in JWT claims.
var OrgResolverFunc func(userID string) string

// FeatureGateFunc checks if the requesting user's plan includes a feature.
// Set by enterprise. Returns true if allowed, false if blocked.
// When nil (community edition), all features are allowed.
var FeatureGateFunc func(r *http.Request, feature string) bool

// RequireFeature middleware blocks access if the user's plan doesn't include the feature.
// In community edition (FeatureGateFunc nil), all features are allowed.
func RequireFeature(feature string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if FeatureGateFunc != nil && !FeatureGateFunc(r, feature) {
				writeError(w, http.StatusForbidden, "this feature requires a plan upgrade")
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// ValidateOrgAccess checks if a resource belongs to the requesting user's org.
// Returns true if: no org context (community edition), or orgs match.
func ValidateOrgAccess(r *http.Request, resourceOrgID string) bool {
	reqOrg := GetOrgIDFromRequest(r)
	if reqOrg == "" {
		return true // community edition — no org isolation
	}
	if resourceOrgID == "" {
		return true // resource without org assignment (legacy)
	}
	return reqOrg == resourceOrgID
}

// DenyOrgAccess writes a 404 for resources that don't belong to the user's org.
// Uses 404 instead of 403 to avoid leaking resource existence.
func DenyOrgAccess(w http.ResponseWriter) {
	writeError(w, http.StatusNotFound, "not found")
}

// GetOrgIDFromRequest extracts the org ID set by enterprise OrgMiddleware.
// Returns "" if no org context is set (community edition).
func GetOrgIDFromRequest(r *http.Request) string {
	if v := r.Context().Value(OrgIDContextKey{}); v != nil {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
}
