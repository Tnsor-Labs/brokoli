package api

import (
	"net/http"

	"github.com/hc12r/broked/store"
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
