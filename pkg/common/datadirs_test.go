package common

import (
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
