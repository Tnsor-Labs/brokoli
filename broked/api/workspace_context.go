package api

import (
	"context"
	"net/http"

	"github.com/hc12r/broked/models"
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
