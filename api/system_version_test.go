package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/Tnsor-Labs/brokoli/engine"
	"github.com/Tnsor-Labs/brokoli/store"
)

// The version has to reach the API, because that is the only way the UI
// can ask what the server is running. Before this it did not: the
// sidebar printed a hardcoded literal and Settings read a field this
// endpoint never sent, so every build reported the same wrong string.
func TestSetBuildVersion(t *testing.T) {
	original := buildVersion
	t.Cleanup(func() { buildVersion = original })

	if buildVersion != "dev" {
		t.Errorf("default should be %q, got %q — a local build must not claim a release", "dev", buildVersion)
	}

	SetBuildVersion("v0.10.67")
	if buildVersion != "v0.10.67" {
		t.Errorf("version = %q, want v0.10.67", buildVersion)
	}

	// An empty value keeps whatever is there. A build without ldflags
	// should report "dev", not an empty string that renders as a bare
	// separator in the UI.
	SetBuildVersion("")
	if buildVersion != "v0.10.67" {
		t.Errorf("empty input overwrote the version: got %q", buildVersion)
	}
}

// The field has to be named what the UI already reads, and it has to
// come out of the real handler — an earlier version of this test built
// the payload by hand and asserted on its own map, which proved nothing.
func TestSystemInfoCarriesVersion(t *testing.T) {
	original := buildVersion
	t.Cleanup(func() { buildVersion = original })
	SetBuildVersion("v9.9.9")

	s, err := store.NewSQLiteStore(filepath.Join(t.TempDir(), "sysinfo.db"))
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}
	e := engine.NewEngine(s)
	t.Cleanup(func() { _ = e.Close(context.Background()) })

	rec := httptest.NewRecorder()
	systemInfo(s, e)(rec, httptest.NewRequest(http.MethodGet, "/api/system/info", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var got map[string]interface{}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v (body %s)", err, rec.Body.String())
	}
	if got["version"] != "v9.9.9" {
		t.Errorf(`"version" = %v, want v9.9.9 — this is the field Settings.svelte reads`, got["version"])
	}
	// The fields that were already there must survive.
	for _, k := range []string{"active_runs", "max_concurrent_runs"} {
		if _, ok := got[k]; !ok {
			t.Errorf("payload lost %q", k)
		}
	}
}
