package drivers

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Tnsor-Labs/brokoli/pkg/netguard"
)

func libraryDigest(contents []byte) string {
	sum := sha256.Sum256(contents)
	return hex.EncodeToString(sum[:])
}

func TestParseManagedSet(t *testing.T) {
	digest := libraryDigest([]byte("library"))
	good := fmt.Sprintf(`[{"name":"flightsql","version":"1.0.0","library_sha256":%q},{"name":"sqlite","version":"1.0.0","library_sha256":%q}]`,
		strings.ToUpper(digest), digest)
	set, err := ParseManagedSet(good)
	if err != nil || len(set) != 2 {
		t.Fatalf("ParseManagedSet = %v, %v", set, err)
	}
	if set[0].LibrarySHA256 != digest {
		t.Fatalf("digest not normalized: %s", set[0].LibrarySHA256)
	}
	for name, raw := range map[string]string{
		"not json":       `flightsql@1.0.0`,
		"no digest":      `[{"name":"flightsql","version":"1.0.0"}]`,
		"short digest":   `[{"name":"flightsql","version":"1.0.0","library_sha256":"abc"}]`,
		"unknown field":  fmt.Sprintf(`[{"name":"flightsql","version":"1.0.0","library_sha256":%q,"url":"x"}]`, digest),
		"declared twice": fmt.Sprintf(`[{"name":"flightsql","version":"1.0.0","library_sha256":%q},{"name":"flightsql","version":"1.0.0","library_sha256":%q}]`, digest, digest),
	} {
		if _, err := ParseManagedSet(raw); err == nil {
			t.Errorf("%s: accepted %s", name, raw)
		}
	}
}

// A value that does not parse still makes the deployment managed: a typo
// must not quietly reopen the API to installs.
func TestManagedIsOnForAnyValue(t *testing.T) {
	t.Setenv(ManagedEnvVar, "")
	if Managed() {
		t.Fatal("managed with no value")
	}
	t.Setenv(ManagedEnvVar, "not json")
	if !Managed() {
		t.Fatal("an unparseable value turned managed mode off")
	}
	if _, err := ManagedDrivers(); err == nil {
		t.Fatal("ManagedDrivers accepted an unparseable value")
	}
}

// managedCatalog serves one driver archive and counts the requests.
func managedCatalog(t *testing.T, contents []byte) (indexURL string, hits *int32) {
	t.Helper()
	archive, digest := testArchive(t, "flightsql", "lib/driver.so", contents, nil)
	data, err := os.ReadFile(archive)
	if err != nil {
		t.Fatal(err)
	}
	var n int32
	mux := http.NewServeMux()
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	mux.HandleFunc("/driver.tgz", func(w http.ResponseWriter, _ *http.Request) { atomic.AddInt32(&n, 1); _, _ = w.Write(data) })
	mux.HandleFunc("/index.json", func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&n, 1)
		fmt.Fprintf(w, `{"version":1,"drivers":[{"name":"flightsql","version":"1.0.0","os":%q,"arch":%q,"archive_url":%q,"sha256":%q}]}`,
			runtime.GOOS, runtime.GOARCH, server.URL+"/driver.tgz", digest)
	})
	t.Cleanup(netguard.SetOutboundForTesting(netguard.Policy{AllowLoopback: true}))
	return server.URL + "/index.json", &n
}

// EnsureManaged installs a missing build from the given catalog, and a
// second pass with the build installed touches nothing.
func TestEnsureManagedInstallsMissingBuildsOnce(t *testing.T) {
	contents := []byte("library")
	indexURL, hits := managedCatalog(t, contents)
	mgr, err := NewManager(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	set := ManagedSet{{Name: "flightsql", Version: "1.0.0", LibrarySHA256: libraryDigest(contents)}}
	if err := mgr.EnsureManaged(context.Background(), indexURL, set); err != nil {
		t.Fatalf("EnsureManaged: %v", err)
	}
	if mgr.GetIdentity(set[0]) == nil {
		t.Fatal("the declared build is not installed")
	}
	before := atomic.LoadInt32(hits)
	if err := mgr.EnsureManaged(context.Background(), indexURL, set); err != nil {
		t.Fatalf("second EnsureManaged: %v", err)
	}
	if after := atomic.LoadInt32(hits); after != before {
		t.Fatalf("a second pass fetched the catalog %d more time(s); the build was already installed", after-before)
	}
}

// The catalog's build must be the declared one: a catalog serving a
// different library under the same name and version is refused.
func TestEnsureManagedRefusesABuildThatIsNotTheDeclaredOne(t *testing.T) {
	indexURL, _ := managedCatalog(t, []byte("a different library"))
	mgr, err := NewManager(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	set := ManagedSet{{Name: "flightsql", Version: "1.0.0", LibrarySHA256: libraryDigest([]byte("library"))}}
	err = mgr.EnsureManaged(context.Background(), indexURL, set)
	if err == nil || !strings.Contains(err.Error(), "but the platform declares") {
		t.Fatalf("EnsureManaged error = %v, want a declared-digest mismatch", err)
	}
}

// An unreachable catalog is waited out: the install retries until the
// catalog answers, and gives up only when its deadline ends. A failure
// that is not about reaching the catalog is not retried.
func TestEnsureManagedWaitsOutAnUnreachableCatalog(t *testing.T) {
	prevFirst, prevMax := catalogRetryFirst, catalogRetryMax
	catalogRetryFirst, catalogRetryMax = time.Millisecond, 4*time.Millisecond
	t.Cleanup(func() { catalogRetryFirst, catalogRetryMax = prevFirst, prevMax })

	contents := []byte("library")
	indexURL, hits := managedCatalog(t, contents)
	set := ManagedSet{{Name: "flightsql", Version: "1.0.0", LibrarySHA256: libraryDigest(contents)}}

	// The index fails three times (as DNS or the host would), then answers.
	var failures int32 = 3
	flaky := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&failures, -1) >= 0 {
			http.Error(w, "unavailable", http.StatusBadGateway)
			return
		}
		http.Redirect(w, r, indexURL, http.StatusTemporaryRedirect)
	}))
	t.Cleanup(flaky.Close)
	mgr, err := NewManager(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := mgr.EnsureManaged(context.Background(), flaky.URL, set); err != nil {
		t.Fatalf("EnsureManaged after a brief outage: %v", err)
	}
	if mgr.GetIdentity(set[0]) == nil || atomic.LoadInt32(hits) == 0 {
		t.Fatal("not installed once the catalog answered")
	}

	// Down for good: gives up at the deadline, saying it kept trying.
	down := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "unavailable", http.StatusServiceUnavailable)
	}))
	t.Cleanup(down.Close)
	other, _ := NewManager(t.TempDir())
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	err = other.EnsureManaged(ctx, down.URL, set)
	if !errors.Is(err, ErrCatalogUnavailable) || !strings.Contains(err.Error(), "gave up after") {
		t.Fatalf("a catalog down for good: %v", err)
	}

	// Not in the catalog: refused at once, not retried.
	var asked int32
	empty := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&asked, 1)
		fmt.Fprint(w, `{"version":1,"drivers":[]}`)
	}))
	t.Cleanup(empty.Close)
	third, _ := NewManager(t.TempDir())
	if err := third.EnsureManaged(context.Background(), empty.URL, set); !errors.Is(err, ErrNotInCatalog) || atomic.LoadInt32(&asked) != 1 {
		t.Fatalf("not in the catalog: %v after %d request(s)", err, asked)
	}
}
