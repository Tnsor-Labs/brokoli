package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Tnsor-Labs/brokoli/extensions"
)

// Who may read one audit entry.
//
// Fetching by id used to answer for any entry among the newest 500,
// whoever asked: no feature gate, no permission check, no tenant filter.
// Audit entries carry the acting user and the before/after values of a
// change, so that is a readable trail of another tenant's activity to
// anybody who can guess an id.

func auditRequest(orgID string) *http.Request {
	r := httptest.NewRequest(http.MethodGet, "/api/audit/x", nil)
	if orgID != "" {
		r = r.WithContext(context.WithValue(r.Context(), OrgIDContextKey{}, orgID))
	}
	return r
}

func entryFor(orgID string) extensions.AuditEntry {
	e := extensions.AuditEntry{ID: "e1", Action: "delete", Resource: "pipeline"}
	if orgID != "" {
		e.Metadata = map[string]interface{}{"org_id": orgID}
	}
	return e
}

func TestAuditEntryVisibleOnlyWithinItsTenant(t *testing.T) {
	if !auditEntryVisibleTo(auditRequest("org-1"), entryFor("org-1")) {
		t.Error("an entry from the caller's own tenant was hidden")
	}
	if auditEntryVisibleTo(auditRequest("org-1"), entryFor("org-2")) {
		t.Error("an entry from another tenant was visible")
	}
}

// An entry with no tenant is visible to nobody once tenants exist. The
// alternative makes an unstamped entry a hole in the boundary, and the
// entries most likely to be unstamped are the oldest ones.
func TestAnUnstampedEntryIsNotVisibleToATenant(t *testing.T) {
	if auditEntryVisibleTo(auditRequest("org-1"), entryFor("")) {
		t.Error("an entry with no tenant was visible to a tenant")
	}
	// A non-string in the metadata is not a match either.
	e := extensions.AuditEntry{ID: "e1", Metadata: map[string]interface{}{"org_id": 42}}
	if auditEntryVisibleTo(auditRequest("org-1"), e) {
		t.Error("an entry whose org_id is not a string was visible")
	}
}

// A deployment with no tenants sees everything, which is the
// single-tenant behaviour and must not change.
func TestWithoutTenantsEveryEntryIsVisible(t *testing.T) {
	for _, e := range []extensions.AuditEntry{entryFor(""), entryFor("org-1")} {
		if !auditEntryVisibleTo(auditRequest(""), e) {
			t.Errorf("entry %+v hidden from a deployment with no tenants", e)
		}
	}
}
