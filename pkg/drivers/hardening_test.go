package drivers

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/Tnsor-Labs/brokoli/pkg/netguard"
)

// The manifest version becomes a directory under the driver root. A version
// that walks out of it must be refused before anything is written.
func TestInstallRefusesVersionThatLeavesTheDriverDirectory(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "drivers")
	archive, digest := testArchive(t, "escape", "driver.so", []byte("library"), func(m *Manifest) { m.Version = "../../../escaped" })
	if _, err := InstallArchive(archive, dir, digest); err == nil {
		t.Fatal("installed a driver whose version traverses out of the driver directory")
	}
	if _, err := os.Stat(filepath.Join(root, "escaped")); !os.IsNotExist(err) {
		t.Fatalf("install wrote outside the driver directory: %v", err)
	}
}

func TestCompareVersionsIsSemantic(t *testing.T) {
	for _, tc := range []struct {
		a, b string
		want int
	}{
		{"1.10.0", "1.9.0", 1},
		{"1.9.0", "1.10.0", -1},
		{"v2.0.0", "1.99.0", 1},
		{"1.0.0", "1.0.0-rc.1", 1},
		{"1.0.0", "1.0.0", 0},
	} {
		if got := CompareVersions(tc.a, tc.b); got != tc.want {
			t.Errorf("CompareVersions(%q, %q) = %d, want %d", tc.a, tc.b, got, tc.want)
		}
	}
}

func TestSelectPicksNewestSemverAndSkipsRevoked(t *testing.T) {
	entry := func(version, lifecycle string) IndexEntry {
		return IndexEntry{Name: "flightsql", Version: version, OS: runtime.GOOS, Arch: runtime.GOARCH, Lifecycle: lifecycle}
	}
	idx := &Index{Drivers: []IndexEntry{entry("1.9.0", ""), entry("1.10.0", ""), entry("1.11.0", "revoked")}}
	if got := idx.Select("flightsql", ""); got == nil || got.Version != "1.10.0" {
		t.Fatalf("Select() = %+v, want 1.10.0", got)
	}
	if got := idx.Select("flightsql", "1.11.0"); got != nil {
		t.Fatalf("Select() returned a revoked release: %+v", got)
	}
}

func serveIndex(t *testing.T, body string) {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(body)) }))
	t.Cleanup(server.Close)
	t.Setenv(IndexEnvVar, server.URL)
	t.Cleanup(netguard.SetOutboundForTesting(netguard.Policy{AllowLoopback: true}))
}

// A field added to the catalog later must not break servers already deployed.
func TestFetchIndexIgnoresUnknownFields(t *testing.T) {
	serveIndex(t, `{"version":1,"signature":"later","drivers":[{"name":"flightsql","version":"1.0.0","os":"linux","arch":"amd64","archive_url":"https://example.invalid/a.tgz","sha256":"`+strings.Repeat("a", 64)+`","new_field":true}]}`)
	idx, err := FetchIndex(context.Background(), IndexURL())
	if err != nil {
		t.Fatalf("FetchIndex refused an index with a field it does not know: %v", err)
	}
	if len(idx.Drivers) != 1 {
		t.Fatalf("drivers = %d, want 1", len(idx.Drivers))
	}
}

func TestFetchIndexRefusesUnverifiableDocumentationURL(t *testing.T) {
	serveIndex(t, `{"version":1,"drivers":[{"name":"flightsql","version":"1.0.0","os":"linux","arch":"amd64","archive_url":"https://example.invalid/a.tgz","docs_url":"http://example.invalid/README.md","sha256":"`+strings.Repeat("a", 64)+`"}]}`)
	if _, err := FetchIndex(context.Background(), IndexURL()); !errors.Is(err, ErrCatalogUnavailable) {
		t.Fatalf("FetchIndex error = %v, want an invalid-entry refusal", err)
	}
}

func TestCatalogIsOptIn(t *testing.T) {
	t.Setenv(IndexEnvVar, "")
	mgr, err := NewManager(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := mgr.InstallFromCatalog(context.Background(), "flightsql"); !errors.Is(err, ErrCatalogNotConfigured) {
		t.Fatalf("InstallFromCatalog error = %v, want ErrCatalogNotConfigured", err)
	}
}

// A catalog entry for one driver must not be able to install another: the
// digest proves the bytes are the listed ones, not that they are the driver
// the entry names.
func TestInstallFromCatalogRefusesArchiveForAnotherDriver(t *testing.T) {
	archive, digest := testArchive(t, "postgresql", "driver.so", []byte("library"), nil)
	data, err := os.ReadFile(archive)
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	mux.HandleFunc("/driver.tgz", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(data) })
	mux.HandleFunc("/index.json", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprintf(w, `{"version":1,"drivers":[{"name":"flightsql","version":"1.0.0","os":%q,"arch":%q,"archive_url":%q,"sha256":%q}]}`,
			runtime.GOOS, runtime.GOARCH, server.URL+"/driver.tgz", digest)
	})
	t.Setenv(IndexEnvVar, server.URL+"/index.json")
	t.Cleanup(netguard.SetOutboundForTesting(netguard.Policy{AllowLoopback: true}))

	dir := t.TempDir()
	mgr, err := NewManager(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := mgr.InstallFromCatalog(context.Background(), "flightsql"); err == nil || !strings.Contains(err.Error(), "catalog entry is flightsql") {
		t.Fatalf("InstallFromCatalog error = %v, want a name mismatch", err)
	}
	if len(mgr.List()) != 0 {
		t.Fatalf("a mismatched archive was installed: %+v", mgr.List())
	}
	if _, err := os.Stat(filepath.Join(dir, "postgresql")); !os.IsNotExist(err) {
		t.Fatalf("mismatched driver left on disk: %v", err)
	}
}

func TestLoadIdentityVerifiesTheExactBuild(t *testing.T) {
	dir := t.TempDir()
	archive, digest := testArchive(t, "flightsql", "lib/driver.so", []byte("library"), nil)
	installed, err := InstallArchive(archive, dir, digest)
	if err != nil {
		t.Fatal(err)
	}
	identity := installed.Identity()
	if got, err := LoadIdentity(dir, identity); err != nil || got.LibraryPath() != installed.LibraryPath() {
		t.Fatalf("LoadIdentity = %v, %v", got, err)
	}
	upper := identity
	upper.LibrarySHA256 = strings.ToUpper(upper.LibrarySHA256)
	if _, err := LoadIdentity(dir, upper); err != nil {
		t.Fatalf("LoadIdentity refused an uppercase digest of the same build: %v", err)
	}
	if _, err := LoadIdentity(dir, DriverIdentity{Name: "flightsql", Version: "../x", LibrarySHA256: identity.LibrarySHA256}); !errors.Is(err, ErrInvalidDriverRequest) {
		t.Fatalf("traversing identity error = %v", err)
	}
	if err := os.WriteFile(installed.LibraryPath(), []byte("swapped"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadIdentity(dir, identity); err == nil {
		t.Fatal("LoadIdentity accepted a library changed after install")
	}
}

// A rescan must notice a library that changed, even though it no longer
// hashes libraries whose size and modification time are unchanged.
func TestRescanDropsATamperedLibrary(t *testing.T) {
	dir := t.TempDir()
	archive, digest := testArchive(t, "flightsql", "driver.so", []byte("library"), nil)
	mgr, err := NewManager(dir)
	if err != nil {
		t.Fatal(err)
	}
	installed, err := mgr.InstallArchive(archive, digest)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(installed.LibraryPath(), []byte("tampered library"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := mgr.LoadAll(); err != nil {
		t.Fatal(err)
	}
	if mgr.GetIdentity(installed.Identity()) != nil {
		t.Fatal("a library changed on disk is still listed as the installed build")
	}
}

func TestSharedManagerSeesAnInstallThroughAnyHandle(t *testing.T) {
	dir := t.TempDir()
	a, err := Shared(dir)
	if err != nil {
		t.Fatal(err)
	}
	b, err := Shared(dir + string(filepath.Separator))
	if err != nil {
		t.Fatal(err)
	}
	if a != b {
		t.Fatal("Shared returned two managers for one directory")
	}
	archive, digest := testArchive(t, "flightsql", "driver.so", []byte("library"), nil)
	installed, err := a.InstallArchive(archive, digest)
	if err != nil {
		t.Fatal(err)
	}
	if b.GetIdentity(installed.Identity()) == nil {
		t.Fatal("an install through one handle is invisible through the other")
	}
	tags := b.Advertised()
	want, _ := Capabilities(installed.Identity())
	if strings.Join(tags, ",") != strings.Join(want, ",") {
		t.Fatalf("Advertised() = %v, want %v", tags, want)
	}
}

func TestScanRemovesOnlyStaleStagingDirectories(t *testing.T) {
	dir := t.TempDir()
	stale := filepath.Join(dir, stagePrefix+"old")
	fresh := filepath.Join(dir, stagePrefix+"new")
	for _, p := range []string{stale, fresh} {
		if err := os.MkdirAll(p, 0o750); err != nil {
			t.Fatal(err)
		}
	}
	old := time.Now().Add(-2 * staleStageAge)
	if err := os.Chtimes(stale, old, old); err != nil {
		t.Fatal(err)
	}
	if _, err := NewManager(dir); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Fatalf("stale staging directory remains: %v", err)
	}
	if _, err := os.Stat(fresh); err != nil {
		t.Fatalf("an install in flight lost its staging directory: %v", err)
	}
}

func TestCapabilitiesNeedNoInstallation(t *testing.T) {
	identity := DriverIdentity{Name: "flightsql", Version: "1.0.0", LibrarySHA256: strings.Repeat("AB", 32)}
	tags, err := Capabilities(identity)
	if err != nil {
		t.Fatal(err)
	}
	if tags[2] != "native-adbc:flightsql:1.0.0:"+strings.Repeat("ab", 6) {
		t.Fatalf("exact tag = %q, want a lowercased digest prefix", tags[2])
	}
}

func TestManifestValidateRefusesPathLikeVersions(t *testing.T) {
	for _, version := range []string{"", "../x", "1.0/../../x", "..", ".hidden", "1 0"} {
		m := Manifest{Name: "flightsql", Version: version, OS: runtime.GOOS, Arch: runtime.GOARCH, Library: "driver.so", Entrypoint: "Init",
			LibrarySHA256: strings.Repeat("a", 64), ArchiveSHA256: strings.Repeat("a", 64)}
		if err := m.Validate(); err == nil {
			t.Errorf("Validate accepted version %q", version)
		}
	}
}

// A build copied by hand under another identity's directory says one thing
// and sits where another should be; it is neither of them.
func TestLoadIdentityRefusesABuildFiledUnderAnotherIdentity(t *testing.T) {
	dir := t.TempDir()
	archive, digest := testArchive(t, "flightsql", "driver.so", []byte("library"), nil)
	installed, err := InstallArchive(archive, dir, digest)
	if err != nil {
		t.Fatal(err)
	}
	other := installed.Identity()
	other.Version = "2.0.0"
	otherDir, _ := identityPath(dir, other)
	if err := os.MkdirAll(filepath.Dir(otherDir), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(installed.Dir(), otherDir); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadIdentity(dir, other); !errors.Is(err, ErrDriverIdentityMismatch) {
		t.Fatalf("LoadIdentity error = %v, want ErrDriverIdentityMismatch", err)
	}
	mgr, err := NewManager(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(mgr.List()) != 0 {
		t.Fatalf("a misfiled build is listed: %+v", mgr.List())
	}
}

func TestMissingCapabilityJudgesOnlyNativeDriverTags(t *testing.T) {
	have := []string{"native-adbc", "native-adbc:flightsql"}
	if got := MissingCapability([]string{"task-runtime-v1", "native-adbc", "native-adbc:flightsql"}, have); got != "" {
		t.Fatalf("MissingCapability = %q, want none (other namespaces are the queue's)", got)
	}
	if got := MissingCapability([]string{"native-adbc:postgresql"}, have); got != "native-adbc:postgresql" {
		t.Fatalf("MissingCapability = %q", got)
	}
	if got := MissingCapability([]string{"native-adbcx"}, nil); got != "" {
		t.Fatalf("a look-alike namespace was judged: %q", got)
	}
}
