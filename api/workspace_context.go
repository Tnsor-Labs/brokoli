package api

import (
	"context"
	"net/http"

	"github.com/golang-jwt/jwt/v5"
	"github.com/Tnsor-Labs/brokoli/models"
)

const workspaceKey = "workspace_id"

// WorkspaceMiddleware extracts workspace ID from X-Workspace-ID header,
// sanitizes it, validates ownership, and adds it to the request context.
func WorkspaceMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		wsID := r.Header.Get("X-Workspace-ID")
		if wsID == "" {
			wsID = models.DefaultWorkspaceID
		}
		wsID = sanitizeWorkspaceID(wsID)

		// In multi-tenant mode, validate that the user owns this workspace.
		// Skip validation for default workspace (community edition).
		if wsID != models.DefaultWorkspaceID && UserWorkspaceResolverFunc != nil {
			userID := getUserIDFromRequest(r)
			if userID != "" {
				userWorkspaces := UserWorkspaceResolverFunc(userID)
				owned := false
				for _, uw := range userWorkspaces {
					if uw == wsID {
						owned = true
						break
					}
				}
				if !owned {
					// User doesn't own this workspace — fall back to their first workspace
					// or default if they have none. Never allow cross-tenant access.
					if len(userWorkspaces) > 0 {
						wsID = userWorkspaces[0]
					} else {
						wsID = models.DefaultWorkspaceID
					}
				}
			}
		}

		ctx := context.WithValue(r.Context(), workspaceKey, wsID)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// getUserIDFromRequest extracts the user ID from JWT claims in the request context.
func getUserIDFromRequest(r *http.Request) string {
	claims, ok := r.Context().Value("claims").(*jwt.MapClaims)
	if !ok || claims == nil {
		return ""
	}
	sub, _ := (*claims)["sub"].(string)
	return sub
}

// sanitizeWorkspaceID only allows alphanumeric characters, hyphens, and underscores.
func sanitizeWorkspaceID(id string) string {
	clean := make([]byte, 0, len(id))
	for i := 0; i < len(id); i++ {
		c := id[i]
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '-' || c == '_' {
			clean = append(clean, c)
		}
	}
	if len(clean) == 0 {
		return models.DefaultWorkspaceID
	}
	return string(clean)
}

// GetWorkspaceID returns the workspace ID from the request context.
func GetWorkspaceID(r *http.Request) string {
	if ws, ok := r.Context().Value(workspaceKey).(string); ok && ws != "" {
		return ws
	}
	return models.DefaultWorkspaceID
}

// UserWorkspaceResolverFunc resolves workspace IDs for a user (set by enterprise).
var UserWorkspaceResolverFunc func(userID string) []string

// ValidateWorkspaceAccess checks if a resource belongs to the user's current workspace.
func ValidateWorkspaceAccess(r *http.Request, resourceWorkspaceID string) bool {
	requestedWS := GetWorkspaceID(r)
	if requestedWS == models.DefaultWorkspaceID {
		return true
	}
	return requestedWS == resourceWorkspaceID
}
