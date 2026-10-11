package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/Tnsor-Labs/brokoli/pkg/drivers"
	"github.com/Tnsor-Labs/brokoli/store"
)

const managedTestSet = `[{"name":"flightsql","version":"1.12.0","library_sha256":"2c9ead4e46ce85223d152bd1b9aae33823c1de5bd20a5b2a888b4b75024d2d4e"},` +
	`{"name":"mysql","version":"0.6.1","library_sha256":"b1f5e410a66e5e93a17bc60e2ec909cd826c121070951583f2a04d4e1eb5792e"}]`

// managedDriverHandler is a handler on a managed deployment with nothing
// installed on this process, and a catalog that counts every request.
func managedDriverHandler(t *testing.T) (*DriverHandler, *int32) {
	t.Helper()
	t.Setenv("BROKOLI_DRIVER_DIR", t.TempDir())
	t.Setenv(drivers.ManagedEnvVar, managedTestSet)
	var hits int32
	catalog := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&hits, 1)
		_, _ = w.Write([]byte(`{"version":1,"drivers":[]}`))
	}))
	t.Cleanup(catalog.Close)
	t.Setenv(drivers.IndexEnvVar, catalog.URL+"/index.json")
	s, err := store.NewSQLiteStore(t.TempDir() + "/m.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return NewDriverHandler(s), &hits
}

// On a managed deployment the drivers a connection can pin are the
// platform's declared set, although this process has none installed.
func TestManagedDriversAreListedFromTheDeclaredSet(t *testing.T) {
	h, _ := managedDriverHandler(t)
	rec := httptest.NewRecorder()
	h.Installed(rec, httptest.NewRequest(http.MethodGet, "/api/drivers", nil))
	var body struct {
		Managed bool              `json:"managed"`
		Drivers []installedDriver `json:"drivers"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode %s: %v", rec.Body.String(), err)
	}
	if !body.Managed || len(body.Drivers) != 2 {
		t.Fatalf("managed=%v drivers=%+v, want the 2 declared", body.Managed, body.Drivers)
	}
	if d := body.Drivers[1]; d.Name != "mysql" || d.LibrarySHA256 != "b1f5e410a66e5e93a17bc60e2ec909cd826c121070951583f2a04d4e1eb5792e" || len(d.UsableBy) != 1 || d.UsableBy[0] != "mysql" {
		t.Fatalf("mysql entry = %+v, want its declared identity and usable_by [mysql]", d)
	}
}

// The catalog view shows the same set and never fetches the catalog:
// there is nothing to browse or install on a managed deployment.
func TestManagedCatalogShowsTheSetWithoutFetchingTheCatalog(t *testing.T) {
	h, hits := managedDriverHandler(t)
	rec := httptest.NewRecorder()
	h.Catalog(rec, httptest.NewRequest(http.MethodGet, "/api/drivers/catalog", nil))
	var body struct {
		Managed bool            `json:"managed"`
		Drivers []catalogDriver `json:"drivers"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode %s: %v", rec.Body.String(), err)
	}
	if !body.Managed || len(body.Drivers) != 2 || !body.Drivers[0].Installed || body.Drivers[0].Available {
		t.Fatalf("catalog = %s, want the 2 declared builds, installed, none installable", rec.Body.String())
	}
	if n := atomic.LoadInt32(hits); n != 0 {
		t.Fatalf("the catalog was fetched %d time(s) on a managed deployment", n)
	}
}

// Installing and removing through the API are refused on a managed
// deployment, saying why, and the catalog is not touched.
func TestManagedDeploymentRefusesInstallAndRemove(t *testing.T) {
	h, hits := managedDriverHandler(t)
	install := httptest.NewRecorder()
	h.Install(install, withURLParam(httptest.NewRequest(http.MethodPost, "/api/drivers/catalog/sqlite/install", nil), "name", "sqlite"))
	remove := httptest.NewRecorder()
	h.Remove(remove, withURLParam(httptest.NewRequest(http.MethodDelete, "/api/drivers/flightsql", nil), "name", "flightsql"))
	for what, rec := range map[string]*httptest.ResponseRecorder{"install": install, "remove": remove} {
		if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "managed by the platform") {
			t.Errorf("%s: %d %s, want 403 naming the managed deployment", what, rec.Code, rec.Body.String())
		}
	}
	if n := atomic.LoadInt32(hits); n != 0 {
		t.Fatalf("the catalog was fetched %d time(s)", n)
	}
}

// Capabilities say whether drivers are managed, so the UI can offer
// exactly what the deployment allows.
func TestCapabilitiesReportManagedDrivers(t *testing.T) {
	t.Setenv(drivers.ManagedEnvVar, "")
	if nativeADBCDrivers().Managed {
		t.Fatal("managed reported with no declared set")
	}
	t.Setenv(drivers.ManagedEnvVar, managedTestSet)
	if !nativeADBCDrivers().Managed {
		t.Fatal("managed not reported with a declared set")
	}
}
