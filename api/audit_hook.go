package api

import (
	"net/http"
	"time"

	"github.com/Tnsor-Labs/brokoli/extensions"
	"github.com/golang-jwt/jwt/v5"
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

	// Stamp the tenant onto every entry. The audit query filters by the
	// caller's org — that is the isolation boundary and it is applied
	// server-side — so an entry written without one can never be read
	// back through the API. Everything recorded from here (pipelines,
	// connections, variables, runs) was landing in the table with no
	// org and disappearing from the audit view: on a live instance, 66
	// of 67 stored entries were unreachable, leaving "who changed this
	// connection?" unanswerable by the product while the row sat in the
	// database. Enterprise handlers already set this; core did not.
	entry := extensions.AuditEntry{
		Timestamp:  time.Now(),
		UserID:     userID,
		Username:   username,
		Action:     action,
		Resource:   resource,
		ResourceID: resourceID,
		Before:     before,
		After:      after,
		IP:         ip,
	}
	if orgID := GetOrgIDFromRequest(r); orgID != "" {
		entry.Metadata = map[string]interface{}{"org_id": orgID}
	}
	auditLogger.Log(entry)
}
