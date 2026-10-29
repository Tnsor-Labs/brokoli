package common

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The bug this file exists to prevent: the engine advertised /data as an
// allowed directory and the loaders refused it, so an operator who
// mounted shared storage there could configure everything correctly and
// still be told "not permitted".
func TestDataDirDefaultsIncludeTheAdvertisedMountPoint(t *testing.T) {
	t.Setenv("BROKOLI_DATA_DIRS", "")
	dirs := DataDirs()
	for _, want := range []string{"/data", "/tmp", "."} {
		found := false
		for _, d := range dirs {
			if d == want {
				found = true
			}
		}
		if !found {
			t.Errorf("%q should be allowed by default, got %v", want, dirs)
		}
	}
	if err := PathAllowed("/data/people.csv"); err != nil {
		t.Errorf("/data must be usable out of the box: %v", err)
	}
}

// Both the engine's check and the loaders' check now come through here,
// so configuring a directory has to actually make it usable.
func TestConfiguredDirectoryIsUsableByFileOperations(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("BROKOLI_DATA_DIRS", dir)

	path := filepath.Join(dir, "shared.csv")
	if err := SafeWriteFile(path, []byte("a,b\n1,2\n"), 0o644); err != nil {
		t.Fatalf("writing into a configured directory should work: %v", err)
	}
	got, err := SafeReadFile(path)
	if err != nil {
		t.Fatalf("reading it back should work: %v", err)
	}
	if string(got) != "a,b\n1,2\n" {
		t.Fatalf("content = %q", got)
	}
	f, err := SafeOpenFile(path)
	if err != nil {
		t.Fatalf("opening should work: %v", err)
	}
	f.Close()

	// And a directory that was not configured stays refused.
	other := filepath.Join(t.TempDir(), "elsewhere.csv")
	if err := SafeWriteFile(other, []byte("x"), 0o644); err == nil {
		t.Error("a path outside the configured directories must be refused")
	}
}

// "not permitted" on its own leaves an operator nothing to act on.
func TestRefusalNamesTheDirectoriesAndTheSetting(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("BROKOLI_DATA_DIRS", dir)
	err := PathAllowed("/etc/passwd")
	if err == nil {
		t.Fatal("/etc/passwd must be refused")
	}
	for _, want := range []string{"/etc/passwd", dir, "BROKOLI_DATA_DIRS"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal should mention %q: %v", want, err)
		}
	}
}

func TestPathTraversalStaysBlocked(t *testing.T) {
	t.Setenv("BROKOLI_DATA_DIRS", "/data")
	for _, p := range []string{"/data/../etc/passwd", "/data/x/../../etc/shadow", "../secrets"} {
		if err := PathAllowed(p); err == nil {
			t.Errorf("%q must be refused", p)
		}
	}
}

// A system whose temp directory is not /tmp must still work by default.
func TestNonStandardTempDirStillAllowed(t *testing.T) {
	t.Setenv("BROKOLI_DATA_DIRS", "")
	tmp := os.TempDir()
	if err := PathAllowed(filepath.Join(tmp, "scratch.csv")); err != nil {
		t.Errorf("the system temp directory should be allowed by default: %v", err)
	}
}

// An empty or whitespace-only setting falls back rather than allowing
// nothing (which would break every file node) or everything.
func TestEmptySettingFallsBackToDefaults(t *testing.T) {
	for _, v := range []string{"", "   ", ":::"} {
		t.Setenv("BROKOLI_DATA_DIRS", v)
		if err := PathAllowed("/data/x.csv"); err != nil {
			t.Errorf("BROKOLI_DATA_DIRS=%q should fall back to defaults: %v", v, err)
		}
		if err := PathAllowed("/etc/passwd"); err == nil {
			t.Errorf("BROKOLI_DATA_DIRS=%q must not allow everything", v)
		}
	}
}

// A symlink inside a data directory used to pass the check, which only
// read the path as written, and the open then followed it anywhere.
func TestASymlinkOutOfADataDirIsRefused(t *testing.T) {
	dir, outside := t.TempDir(), t.TempDir()
	t.Setenv("BROKOLI_DATA_DIRS", dir)
	secret := filepath.Join(outside, "secret.csv")
	if err := os.WriteFile(secret, []byte("token"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(dir, "mnt")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(secret, filepath.Join(dir, "people.csv")); err != nil {
		t.Fatal(err)
	}

	if _, err := SafeReadFile(filepath.Join(dir, "people.csv")); err == nil {
		t.Error("reading through a file symlink that leaves the directory must fail")
	}
	if _, err := SafeOpenFile(filepath.Join(dir, "mnt", "secret.csv")); err == nil {
		t.Error("opening through a directory symlink that leaves the directory must fail")
	}
	planted := filepath.Join(dir, "mnt", "new.csv")
	if err := SafeWriteFile(planted, []byte("x"), 0o644); err == nil {
		t.Error("writing through a symlink that leaves the directory must fail")
	}
	if err := MkdirAllInDataDir(filepath.Join(dir, "mnt", "sub"), 0o755); err == nil {
		t.Error("creating a directory through a symlink that leaves the directory must fail")
	}
	for _, p := range []string{"new.csv", "sub"} {
		if _, err := os.Stat(filepath.Join(outside, p)); err == nil {
			t.Errorf("%s was created outside the data directory", p)
		}
	}
	err := PathAllowed(planted)
	if err == nil {
		t.Fatal("PathAllowed must refuse a path that leaves the directory through a symlink")
	}
	if !strings.Contains(err.Error(), planted) || !strings.Contains(err.Error(), "symlinks") {
		t.Errorf("the refusal should name the path and say why: %v", err)
	}
}

// Links that stay inside the directory are ordinary layout and keep working.
func TestASymlinkInsideADataDirStillWorks(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("BROKOLI_DATA_DIRS", dir)
	if err := os.MkdirAll(filepath.Join(dir, "2026", "10"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "2026", "10", "runs.csv"), []byte("a\n1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(dir, "2026", "10"), filepath.Join(dir, "latest")); err != nil {
		t.Fatal(err)
	}
	got, err := SafeReadFile(filepath.Join(dir, "latest", "runs.csv"))
	if err != nil || string(got) != "a\n1\n" {
		t.Errorf("read through an inside link = %q, %v", got, err)
	}
	if err := PathAllowed(filepath.Join(dir, "latest", "next.csv")); err != nil {
		t.Errorf("an inside link must be allowed: %v", err)
	}
}

// A missing file is reported as missing, under the path the caller gave.
func TestAMissingFileNamesTheCallersPath(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("BROKOLI_DATA_DIRS", dir)
	path := filepath.Join(dir, "absent.csv")
	_, err := SafeOpenFile(path)
	if !errors.Is(err, fs.ErrNotExist) || !strings.Contains(err.Error(), path) {
		t.Errorf("err = %v, want a not-exist error naming %s", err, path)
	}
}

// A sink writing into a configured directory that does not exist yet
// creates it, as before.
func TestMkdirAllCreatesAMissingDataDir(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	t.Setenv("BROKOLI_DATA_DIRS", dir)
	if err := PathAllowed(filepath.Join(dir, "out.csv")); err != nil {
		t.Fatalf("a missing data directory has nothing to escape through: %v", err)
	}
	if err := MkdirAllInDataDir(filepath.Join(dir, "out"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := SafeWriteFile(filepath.Join(dir, "out", "x.csv"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
}
