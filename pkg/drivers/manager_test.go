package drivers

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestInstallListAndRemove(t *testing.T) {
	dir := t.TempDir()
	archive, digest := testArchive(t, "adbc-postgresql", "lib/driver.so", []byte("native library"), nil)
	mgr, err := NewManager(dir)
	if err != nil {
		t.Fatal(err)
	}
	installed, err := mgr.InstallArchive(archive, digest)
	if err != nil {
		t.Fatalf("InstallArchive: %v", err)
	}
	if installed.Name != "adbc-postgresql" {
		t.Fatalf("installed %q", installed.Name)
	}
	if got := mgr.Get("adbc-postgresql"); got == nil || got.LibraryPath() != filepath.Join(dir, "adbc-postgresql", "1.0.0", installed.LibrarySHA256, "lib", "driver.so") {
		t.Fatalf("Get() = %#v", got)
	}
	if got := mgr.List(); len(got) != 1 || got[0].Name != "adbc-postgresql" {
		t.Fatalf("List() = %#v", got)
	}
	if err := mgr.Remove("adbc-postgresql"); err != nil {
		t.Fatal(err)
	}
	if len(mgr.List()) != 0 {
		t.Fatal("driver remains after remove")
	}
	if _, err := os.Stat(filepath.Join(dir, "adbc-postgresql")); !os.IsNotExist(err) {
		t.Fatalf("installed directory remains: %v", err)
	}
}

func TestInstallRejectsBadArchiveDigestWithoutInstallation(t *testing.T) {
	archive, _ := testArchive(t, "digest-driver", "driver.so", []byte("library"), nil)
	assertNotInstalled(t, installFails(t, archive, "00"+stringsOf("0", 62)), "digest-driver")
}

func TestInstallRejectsBadLibraryDigestWithoutInstallation(t *testing.T) {
	archive, digest := testArchive(t, "library-driver", "driver.so", []byte("library"), func(m *Manifest) { m.LibrarySHA256 = stringsOf("0", 64) })
	assertNotInstalled(t, installFails(t, archive, digest), "library-driver")
}

func TestInstallRejectsPlatformAndTraversalWithoutInstallation(t *testing.T) {
	t.Run("platform", func(t *testing.T) {
		archive, digest := testArchive(t, "platform-driver", "driver.so", []byte("library"), func(m *Manifest) { m.OS = "not-" + runtime.GOOS })
		assertNotInstalled(t, installFails(t, archive, digest), "platform-driver")
	})
	t.Run("library traversal", func(t *testing.T) {
		archive, digest := testArchive(t, "traversal-driver", "../driver.so", []byte("library"), nil)
		assertNotInstalled(t, installFails(t, archive, digest), "traversal-driver")
	})
	t.Run("archive traversal", func(t *testing.T) {
		archive := filepath.Join(t.TempDir(), "traversal.tar.gz")
		f, err := os.Create(archive)
		if err != nil {
			t.Fatal(err)
		}
		gz := gzip.NewWriter(f)
		tw := tar.NewWriter(gz)
		writeTarFile(t, tw, "../outside", []byte("nope"))
		if err := tw.Close(); err != nil {
			t.Fatal(err)
		}
		if err := gz.Close(); err != nil {
			t.Fatal(err)
		}
		if err := f.Close(); err != nil {
			t.Fatal(err)
		}
		digest, err := ArchiveSHA256(archive)
		if err != nil {
			t.Fatal(err)
		}
		assertNotInstalled(t, installFails(t, archive, digest), "archive-traversal")
	})
}

func installFails(t *testing.T, archive, digest string) string {
	t.Helper()
	dir := t.TempDir()
	_, err := InstallArchive(archive, dir, digest)
	if err == nil {
		t.Fatal("InstallArchive succeeded")
	}
	return dir
}

func assertNotInstalled(t *testing.T, dir, name string) {
	t.Helper()
	if _, err := os.Stat(filepath.Join(dir, name)); !os.IsNotExist(err) {
		t.Fatalf("rejected install left driver behind: %v", err)
	}
}

func testArchive(t *testing.T, name, library string, contents []byte, change func(*Manifest)) (string, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "driver.tar.gz")
	makeArchive := func() {
		f, err := os.Create(path)
		if err != nil {
			t.Fatal(err)
		}
		gz := gzip.NewWriter(f)
		tw := tar.NewWriter(gz)
		libraryDigest := sha256.Sum256(contents)
		m := Manifest{Name: name, Version: "1.0.0", OS: runtime.GOOS, Arch: runtime.GOARCH, Library: library, Entrypoint: "AdbcDriverInit", LibrarySHA256: hex.EncodeToString(libraryDigest[:]), ArchiveSHA256: stringsOf("0", 64)}
		if change != nil {
			change(&m)
		}
		manifest, err := json.Marshal(m)
		if err != nil {
			t.Fatal(err)
		}
		writeTarFile(t, tw, manifestFile, manifest)
		if library != "../driver.so" {
			writeTarFile(t, tw, library, contents)
		} else {
			writeTarFile(t, tw, "driver.so", contents)
		}
		if err := tw.Close(); err != nil {
			t.Fatal(err)
		}
		if err := gz.Close(); err != nil {
			t.Fatal(err)
		}
		if err := f.Close(); err != nil {
			t.Fatal(err)
		}
	}
	makeArchive()
	digest, err := ArchiveSHA256(path)
	if err != nil {
		t.Fatal(err)
	}
	return path, digest
}

func writeTarFile(t *testing.T, tw *tar.Writer, name string, data []byte) {
	t.Helper()
	if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o600, Size: int64(len(data))}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write(data); err != nil {
		t.Fatal(err)
	}
}

func stringsOf(s string, n int) string {
	b := make([]byte, n)
	for i := range b {
		b[i] = s[0]
	}
	return string(b)
}

func TestInstallFromCatalogRejectsUnavailablePlatform(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"version":1,"drivers":[{"name":"flightsql","version":"1.0.0","os":"other","arch":"other","archive_url":"https://example.invalid/driver.tar.gz","sha256":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}]}`))
	}))
	defer server.Close()
	t.Setenv(IndexEnvVar, server.URL)

	manager, err := NewManager(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.InstallFromCatalog(context.Background(), "flightsql"); err == nil {
		t.Fatal("installed unavailable platform driver")
	}
}
