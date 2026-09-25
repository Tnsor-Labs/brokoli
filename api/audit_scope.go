package api

import (
	"net/http"

	"github.com/Tnsor-Labs/brokoli/extensions"
)

// Who may read one audit entry.

// auditEntryVisibleTo reports whether this request may see this entry.
//
// Every entry is stamped with the tenant it happened in (see AuditLog,
// which puts org_id in Metadata). A deployment with no tenants leaves
// that empty and every entry is visible, which is the single-tenant
// behaviour.
//
// Where there are tenants, an entry belongs to exactly one, and an entry
// carrying no tenant is visible to nobody rather than to everybody: the
// alternative makes an unstamped entry a hole in the boundary, and the
// entries most likely to be unstamped are the ones written before the
// stamping existed.
func auditEntryVisibleTo(r *http.Request, entry extensions.AuditEntry) bool {
	callerOrg := GetOrgIDFromRequest(r)
	if callerOrg == "" {
		return true
	}
	orgID, _ := entry.Metadata["org_id"].(string)
	return orgID == callerOrg
}
