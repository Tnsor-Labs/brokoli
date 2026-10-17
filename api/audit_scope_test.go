package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/Tnsor-Labs/brokoli/engine"
	"github.com/Tnsor-Labs/brokoli/extensions"
	"github.com/Tnsor-Labs/brokoli/models"
	"github.com/Tnsor-Labs/brokoli/pkg/sodp"
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

type fixedAudit struct{ entries []extensions.AuditEntry }

func (fixedAudit) Log(extensions.AuditEntry) error { return nil }
func (f fixedAudit) Query(extensions.AuditFilter) ([]extensions.AuditEntry, error) {
	return f.entries, nil
}

// Listing answers the same question as fetching one entry: another
// organization's entries are not the caller's to see, and both ask for
// audit.view.
func TestAuditListShowsOnlyTheCallersOrganization(t *testing.T) {
	audit := fixedAudit{entries: []extensions.AuditEntry{
		{ID: "mine", Action: "delete", Metadata: map[string]interface{}{"org_id": "org-1"}},
		{ID: "theirs", Action: "delete", Metadata: map[string]interface{}{"org_id": "org-2"}},
	}}
	s := newOrgCheckStore(t)
	e := engine.NewEngine(s)
	t.Cleanup(func() { _ = e.Close(context.Background()) })

	r := chi.NewRouter()
	RegisterRoutes(r, s, e, sodp.NewServer(), nil, &extensions.Registry{Audit: audit}, nil)
	req := reqAsRole(http.MethodGet, "/api/audit", "admin")
	req = req.WithContext(context.WithValue(req.Context(), OrgIDContextKey{}, "org-1"))
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/audit: %d %s", rec.Code, rec.Body.String())
	}
	var got []struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].ID != "mine" {
		t.Errorf("listed %+v, want only the caller's organization's entry", got)
	}

	refusing := chi.NewRouter()
	RegisterRoutes(refusing, s, e, sodp.NewServer(), nil, &extensions.Registry{Audit: audit, Team: refuseOneTeam{refuse: string(models.PermAuditView)}}, nil)
	for _, path := range []string{"/api/audit", "/api/audit/mine"} {
		rec := httptest.NewRecorder()
		refusing.ServeHTTP(rec, reqAsRole(http.MethodGet, path, "admin"))
		if rec.Code != http.StatusForbidden {
			t.Errorf("GET %s without audit.view: %d, want 403", path, rec.Code)
		}
	}
}
