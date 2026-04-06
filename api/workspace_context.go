package api

import (
	"context"
	"net/http"

	"github.com/Tnsor-Labs/brokoli/models"
)

const workspaceKey = "workspace_id"

// WorkspaceMiddleware extracts workspace ID from X-Workspace-ID header,
// sanitizes it to prevent injection, and adds it to the request context.
func WorkspaceMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		wsID := r.Header.Get("X-Workspace-ID")
		if wsID == "" {
			wsID = models.DefaultWorkspaceID
		}
		wsID = sanitizeWorkspaceID(wsID)
		ctx := context.WithValue(r.Context(), workspaceKey, wsID)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
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
// Returns the user's workspace IDs so we can validate workspace access.
var UserWorkspaceResolverFunc func(userID string) []string

// ValidateWorkspaceAccess checks if a resource belongs to the user's current workspace.
// In community edition (default workspace), always returns true.
// When team features assign non-default workspaces, the resource's workspace must match.
func ValidateWorkspaceAccess(r *http.Request, resourceWorkspaceID string) bool {
	requestedWS := GetWorkspaceID(r)
	if requestedWS == models.DefaultWorkspaceID {
		return true // community edition or default workspace
	}
	return requestedWS == resourceWorkspaceID
}
