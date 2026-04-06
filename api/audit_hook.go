package api

import (
	"net/http"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/Tnsor-Labs/brokoli/extensions"
)

// Global audit logger — set by RegisterRoutes from the extensions registry.
var auditLogger extensions.AuditLogger

// AuditLog records an action if audit logging is enabled.
func AuditLog(r *http.Request, action, resource, resourceID string, before, after map[string]interface{}) {
	if auditLogger == nil {
		return
	}

	userID := ""
	username := ""
	if claims, ok := r.Context().Value("claims").(*jwt.MapClaims); ok && claims != nil {
		if sub, err := claims.GetSubject(); err == nil {
			userID = sub
		}
		if u, ok := (*claims)["username"].(string); ok {
			username = u
		}
	}

	ip := r.RemoteAddr
	if fwd := r.Header.Get("X-Forwarded-For"); fwd != "" {
		ip = fwd
	}

	auditLogger.Log(extensions.AuditEntry{
		Timestamp:  time.Now(),
		UserID:     userID,
		Username:   username,
		Action:     action,
		Resource:   resource,
		ResourceID: resourceID,
		Before:     before,
		After:      after,
		IP:         ip,
	})
}
