package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// connIconAllowlist pins the icon names the handler may reference: each
// must exist in ui/src/lib/icons.ts. Adding a connection type means adding
// its glyph there AND here — the test is the reminder.
var connIconAllowlist = map[string]bool{
	"connPostgres": true, "connMysql": true, "connSnowflake": true,
	"connRedshift": true, "connBigquery": true, "connDatabricks": true,
	"connOracle": true, "connMssql": true, "connSqlite": true,
	"connS3": true, "connGcs": true, "connAzureBlob": true,
	"connHttp": true, "connSftp": true, "connGeneric": true,
}

// TestConnectionTypes_CatalogMetadata locks the contract the connection
// catalog renders from: every type ships a label, a one-line description,
// a known category, at least one form field, and an icon name from the
// UI's set.
func TestConnectionTypes_CatalogMetadata(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/api/connection-types", nil)
	rec := httptest.NewRecorder()
	ConnectionTypes(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var types []struct {
		Type        string            `json:"type"`
		Label       string            `json:"label"`
		Category    string            `json:"category"`
		Description string            `json:"description"`
		Icon        string            `json:"icon"`
		Fields      []string          `json:"fields"`
		Hints       map[string]string `json:"hints"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &types); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(types) < 15 {
		t.Fatalf("got %d types, want at least 15", len(types))
	}

	validCategories := map[string]bool{"database": true, "storage": true, "api": true, "other": true}
	seen := map[string]bool{}
	for _, ct := range types {
		if ct.Type == "" || ct.Label == "" {
			t.Errorf("type %+v missing type/label", ct)
		}
		if seen[ct.Type] {
			t.Errorf("duplicate type %q", ct.Type)
		}
		seen[ct.Type] = true
		if ct.Description == "" {
			t.Errorf("%s: missing description — the catalog card renders it", ct.Type)
		}
		if !validCategories[ct.Category] {
			t.Errorf("%s: category %q not in the catalog's known set", ct.Type, ct.Category)
		}
		if len(ct.Fields) == 0 {
			t.Errorf("%s: no form fields", ct.Type)
		}
		if ct.Icon == "" {
			t.Errorf("%s: missing icon name", ct.Type)
		} else if !connIconAllowlist[ct.Icon] {
			t.Errorf("%s: icon %q not in the ui icon allowlist — add the glyph to ui/src/lib/icons.ts and this list", ct.Type, ct.Icon)
		}
	}
}
