package api

import (
	"net/http"

	"github.com/Tnsor-Labs/brokoli/models"
	"github.com/Tnsor-Labs/brokoli/store"
	"github.com/golang-jwt/jwt/v5"
)

// PermissionChecker validates user permissions against roles stored in the database.
type PermissionChecker struct {
	store store.Store
}

// NewPermissionChecker creates a new PermissionChecker.
func NewPermissionChecker(s store.Store) *PermissionChecker {
	return &PermissionChecker{store: s}
}

// GetUserPermissions returns the effective permissions for a user based on their role.
func (pc *PermissionChecker) GetUserPermissions(userRole string, workspaceID string) []models.Permission {
	role, err := pc.store.GetRole(userRole)
	if err != nil {
		// Unknown role -- fall back to viewer
		role, _ = pc.store.GetRole("viewer")
		if role == nil {
			return []models.Permission{models.PermPipelinesView, models.PermRunsView}
		}
	}
	return role.Permissions
}

// HasPermission checks if the current request has a specific permission.
func (pc *PermissionChecker) HasPermission(r *http.Request, perm models.Permission) bool {
	claims := r.Context().Value("claims")
	if claims == nil {
		// An unconfigured system awaiting its first user is allowed
		// through — but only when JWTAuth said so. Absent claims used to
		// be read as open mode on their own, and that is also exactly
		// what a request looks like when no authentication middleware ran
		// at all: NewServer mounts JWTAuth only `if userStore != nil`, so
		// a user store that failed to build served every permission-gated
		// route to anyone, behind nothing but a WARNING in the log.
		return IsOpenMode(r)
	}
	mapClaims, ok := claims.(*jwt.MapClaims)
	if !ok || mapClaims == nil {
		return false
	}
	role, _ := (*mapClaims)["role"].(string)
	wsID := GetWorkspaceID(r)
	perms := pc.GetUserPermissions(role, wsID)
	for _, p := range perms {
		if p == perm {
			return true
		}
	}
	return false
}

// RequirePermission returns middleware that checks for a specific permission.
func (pc *PermissionChecker) RequirePermission(perm models.Permission) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !pc.HasPermission(r, perm) {
				writeJSON(w, http.StatusForbidden, map[string]string{
					"error": "insufficient permissions: " + string(perm),
				})
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}
