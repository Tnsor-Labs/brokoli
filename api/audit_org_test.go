package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Tnsor-Labs/brokoli/extensions"
	"github.com/golang-jwt/jwt/v5"
)

type captureLogger struct{ entries []extensions.AuditEntry }

func (c *captureLogger) Log(e extensions.AuditEntry) error {
	c.entries = append(c.entries, e)
	return nil
}
func (c *captureLogger) Query(extensions.AuditFilter) ([]extensions.AuditEntry, error) {
	return c.entries, nil
}

func requestWithOrgAndUser(orgID string) *http.Request {
	r := httptest.NewRequest(http.MethodPost, "/api/pipelines", nil)
	claims := &jwt.MapClaims{"sub": "user-1", "username": "alice"}
	ctx := context.WithValue(r.Context(), "claims", claims)
	if orgID != "" {
		ctx = context.WithValue(ctx, OrgIDContextKey{}, orgID)
	}
	return r.WithContext(ctx)
}

// The enterprise audit query filters by org, so an entry recorded
// without one is stored and then unreachable. Core recorded everything
// that way.
func TestAuditEntryCarriesTheRequestOrg(t *testing.T) {
	cap := &captureLogger{}
	prev := auditLogger
	auditLogger = cap
	defer func() { auditLogger = prev }()

	AuditLog(requestWithOrgAndUser("org-42"), "create", "pipeline", "p1", nil, nil)

	if len(cap.entries) != 1 {
		t.Fatalf("expected one entry, got %d", len(cap.entries))
	}
	got := cap.entries[0].Metadata["org_id"]
	if got != "org-42" {
		t.Fatalf("entry org_id = %v, want org-42", got)
	}
}

// Single-tenant deployments have no org, and must not gain an empty one.
func TestAuditEntryOmitsOrgWhenAbsent(t *testing.T) {
	cap := &captureLogger{}
	prev := auditLogger
	auditLogger = cap
	defer func() { auditLogger = prev }()

	AuditLog(requestWithOrgAndUser(""), "create", "pipeline", "p1", nil, nil)

	if len(cap.entries) != 1 {
		t.Fatalf("expected one entry, got %d", len(cap.entries))
	}
	if _, ok := cap.entries[0].Metadata["org_id"]; ok {
		t.Fatal("an empty org was recorded")
	}
}

// The identity and payload still travel as before.
func TestAuditEntryKeepsItsOtherFields(t *testing.T) {
	cap := &captureLogger{}
	prev := auditLogger
	auditLogger = cap
	defer func() { auditLogger = prev }()

	AuditLog(requestWithOrgAndUser("org-1"), "update", "connection", "shop_pg",
		map[string]interface{}{"host": "old"}, map[string]interface{}{"host": "new"})

	e := cap.entries[0]
	if e.Action != "update" || e.Resource != "connection" || e.ResourceID != "shop_pg" {
		t.Fatalf("fields lost: %+v", e)
	}
	if e.Username != "alice" || e.UserID != "user-1" {
		t.Fatalf("identity lost: %+v", e)
	}
	if e.Before["host"] != "old" || e.After["host"] != "new" {
		t.Fatalf("before/after lost: %+v", e)
	}
}
