package api

import (
	"archive/tar"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/Tnsor-Labs/brokoli/models"
	"github.com/Tnsor-Labs/brokoli/pkg/drivers"
	"github.com/Tnsor-Labs/brokoli/pkg/netguard"
	"github.com/Tnsor-Labs/brokoli/store"
)

// driverTestArchive builds a minimal installable driver archive in a
// temporary directory, so handler tests exercise the real installer.
func driverTestArchive(t *testing.T, dir, name string) (string, string) {
	t.Helper()
	library := []byte("native library")
	libraryDigest := sha256.Sum256(library)
	manifest := drivers.Manifest{
		Name: name, Version: "1.0.0", OS: runtime.GOOS, Arch: runtime.GOARCH,
		Library: "lib/driver.so", Entrypoint: "AdbcDriverFlightSQLInit",
		LibrarySHA256: hex.EncodeToString(libraryDigest[:]),
		ArchiveSHA256: strings.Repeat("0", 64),
	}
	encoded, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, name+"-archive.tar.gz")
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	gzipWriter := gzip.NewWriter(file)
	tarWriter := tar.NewWriter(gzipWriter)
	for _, entry := range []struct {
		name string
		data []byte
	}{{"manifest.json", encoded}, {"lib/driver.so", library}} {
		if err := tarWriter.WriteHeader(&tar.Header{Name: entry.name, Mode: 0o600, Size: int64(len(entry.data))}); err != nil {
			t.Fatal(err)
		}
		if _, err := tarWriter.Write(entry.data); err != nil {
			t.Fatal(err)
		}
	}
	if err := tarWriter.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gzipWriter.Close(); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	archiveDigest, err := drivers.ArchiveSHA256(path)
	if err != nil {
		t.Fatal(err)
	}
	return path, archiveDigest
}

func TestDriverCatalogHandlerFiltersToCurrentPlatform(t *testing.T) {
	index := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"version":1,"drivers":[{"name":"flightsql","version":"1.0.0","os":"` + runtime.GOOS + `","arch":"` + runtime.GOARCH + `","archive_url":"https://example.invalid/flight.tar.gz","sha256":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},{"name":"other","version":"1.0.0","os":"other","arch":"other","archive_url":"https://example.invalid/other.tar.gz","sha256":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"}]}`))
	}))
	defer index.Close()
	t.Setenv("BROKOLI_DRIVER_INDEX", index.URL)
	t.Cleanup(netguard.SetOutboundForTesting(netguard.Policy{AllowLoopback: true}))
	t.Setenv("BROKOLI_DRIVER_DIR", t.TempDir())

	recorder := httptest.NewRecorder()
	NewDriverHandler(nil).Catalog(recorder, httptest.NewRequest(http.MethodGet, "/api/drivers/catalog", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", recorder.Code, recorder.Body.String())
	}
	if body := recorder.Body.String(); !strings.Contains(body, `"name":"flightsql"`) || strings.Contains(body, `"name":"other"`) {
		t.Fatalf("unexpected catalog response: %s", body)
	}
}

func TestDriverCatalogReportsInstalledMatchingRelease(t *testing.T) {
	driverDir := t.TempDir()
	manager, err := drivers.NewManager(driverDir)
	if err != nil {
		t.Fatal(err)
	}
	archive, digest := driverTestArchive(t, driverDir, "flightsql")
	if _, err := manager.InstallArchive(archive, digest); err != nil {
		t.Fatal(err)
	}

	index := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"version":1,"drivers":[{"name":"flightsql","version":"1.0.0","os":"` + runtime.GOOS + `","arch":"` + runtime.GOARCH + `","archive_url":"https://example.invalid/flight.tar.gz","sha256":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}]}`))
	}))
	defer index.Close()
	t.Setenv("BROKOLI_DRIVER_INDEX", index.URL)
	t.Cleanup(netguard.SetOutboundForTesting(netguard.Policy{AllowLoopback: true}))
	t.Setenv("BROKOLI_DRIVER_DIR", driverDir)

	recorder := httptest.NewRecorder()
	NewDriverHandler(nil).Catalog(recorder, httptest.NewRequest(http.MethodGet, "/api/drivers/catalog", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", recorder.Code, recorder.Body.String())
	}
	if !strings.Contains(recorder.Body.String(), `"installed":true`) {
		t.Fatalf("catalog did not report installed release: %s", recorder.Body.String())
	}
}

// Removal must refuse while a saved connection is pinned to that exact driver
// identity: deleting it would turn every run using that connection into an
// opaque driver failure instead of a clear refusal at the destructive action.
func TestDriverRemoveRefusesWhileConnectionsArePinned(t *testing.T) {
	driverDir := t.TempDir()
	manager, err := drivers.NewManager(driverDir)
	if err != nil {
		t.Fatal(err)
	}
	archive, digest := driverTestArchive(t, driverDir, "flightsql")
	installed, err := manager.InstallArchive(archive, digest)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("BROKOLI_DRIVER_DIR", driverDir)

	pinned := installed.Identity()
	connections, err := store.NewSQLiteStore(t.TempDir() + "/connections.db")
	if err != nil {
		t.Fatal(err)
	}
	defer connections.Close()
	if err := connections.CreateConnection(&models.Connection{ID: "c1", ConnID: "flight", Type: models.ConnTypeFlightSQL, DriverIdentity: &pinned}); err != nil {
		t.Fatal(err)
	}

	recorder := httptest.NewRecorder()
	request := withURLParam(httptest.NewRequest(http.MethodDelete, "/api/drivers/flightsql?version="+installed.Version, nil), "name", "flightsql")
	NewDriverHandler(connections).Remove(recorder, request)

	if recorder.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409: %s", recorder.Code, recorder.Body.String())
	}
	if body := recorder.Body.String(); !strings.Contains(body, "flight") {
		t.Fatalf("conflict must name the pinned connection: %s", body)
	}
	if manager.GetIdentity(pinned) == nil {
		t.Fatal("a refused removal still deleted the driver")
	}
}
