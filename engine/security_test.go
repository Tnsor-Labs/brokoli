package engine

import (
	"os"
	"path/filepath"
	"testing"
)

// ─── validateFilePath ───────────────────────────────────────────────────────

func TestValidateFilePath_AllowedPaths(t *testing.T) {
	// Create temp directories that match the allowed dirs
	tmpDir := t.TempDir()
	dataDir := filepath.Join(tmpDir, "data")
	os.MkdirAll(dataDir, 0o755)

	// Save and restore allowedDataDirs
	origDirs := allowedDataDirs
	allowedDataDirs = []string{dataDir, os.TempDir(), "."}
	defer func() { allowedDataDirs = origDirs }()

	tests := []struct {
		name string
		path string
	}{
		{"data dir file", filepath.Join(dataDir, "input.csv")},
		{"tmp dir file", filepath.Join(os.TempDir(), "output.json")},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := validateFilePath(tc.path)
			if err != nil {
				t.Errorf("path %q should be allowed but got: %v", tc.path, err)
			}
		})
	}
}

func TestValidateFilePath_RelativePaths(t *testing.T) {
	// With "." in allowed dirs, relative paths under cwd should be allowed
	origDirs := allowedDataDirs
	allowedDataDirs = []string{"."}
	defer func() { allowedDataDirs = origDirs }()

	tests := []struct {
		name string
		path string
	}{
		{"simple relative", "local/file.csv"},
		{"dot-slash relative", "./local/file.csv"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := validateFilePath(tc.path)
			if err != nil {
				t.Errorf("path %q should be allowed but got: %v", tc.path, err)
			}
		})
	}
}

func TestValidateFilePath_BlockedPaths(t *testing.T) {
	// Set allowed dirs to only /data and /tmp
	origDirs := allowedDataDirs
	allowedDataDirs = []string{"/data", "/tmp"}
	defer func() { allowedDataDirs = origDirs }()

	blockedPaths := []struct {
		name string
		path string
	}{
		{"etc passwd", "/etc/passwd"},
		{"etc shadow", "/etc/shadow"},
		{"var log", "/var/log/syslog"},
		{"usr bin", "/usr/bin/python3"},
		{"root ssh", "/root/.ssh/id_rsa"},
		{"home directory", "/home/user/secrets.txt"},
	}
	for _, tc := range blockedPaths {
		t.Run(tc.name, func(t *testing.T) {
			err := validateFilePath(tc.path)
			if err == nil {
				t.Errorf("path %q should be blocked", tc.path)
			}
		})
	}
}

func TestValidateFilePath_PathTraversal(t *testing.T) {
	paths := []struct {
		name string
		path string
	}{
		{"simple traversal", "../file.csv"},
		{"deep traversal", "dir/../../../etc/passwd"},
		{"data breakout", "/data/subdir/../../root/secret"},
		{"double dot hidden", "/data/../etc/passwd"},
		{"dot dot in middle", "../../secret.txt"},
	}
	for _, tc := range paths {
		t.Run(tc.name, func(t *testing.T) {
			err := validateFilePath(tc.path)
			if err == nil {
				t.Errorf("path traversal %q should be blocked", tc.path)
			}
		})
	}
}

func TestValidateFilePath_TraversalErrorMessage(t *testing.T) {
	err := validateFilePath("../etc/passwd")
	if err == nil {
		t.Fatal("expected error for path traversal")
	}
	if err.Error() != "path traversal not allowed" {
		t.Errorf("unexpected error message: %q", err.Error())
	}
}

func TestValidateFilePath_OutsideAllowedDirsErrorMessage(t *testing.T) {
	origDirs := allowedDataDirs
	allowedDataDirs = []string{"/data", "/tmp"}
	defer func() { allowedDataDirs = origDirs }()

	err := validateFilePath("/etc/passwd")
	if err == nil {
		t.Fatal("expected error for path outside allowed dirs")
	}
	expected := "outside allowed directories"
	if !containsSubstring(err.Error(), expected) {
		t.Errorf("error should mention %q, got: %q", expected, err.Error())
	}
}

func TestValidateFilePath_EmptyAllowedDirs(t *testing.T) {
	origDirs := allowedDataDirs
	allowedDataDirs = []string{}
	defer func() { allowedDataDirs = origDirs }()

	// With no allowed dirs, everything should be blocked
	err := validateFilePath("/data/file.csv")
	if err == nil {
		t.Error("with no allowed dirs, all paths should be blocked")
	}
}

func TestValidateFilePath_CustomBROKOLI_DATA_DIRS(t *testing.T) {
	// Test that the allowed dirs list can be expanded
	customDir := t.TempDir()
	origDirs := allowedDataDirs
	allowedDataDirs = []string{customDir}
	defer func() { allowedDataDirs = origDirs }()

	err := validateFilePath(filepath.Join(customDir, "file.csv"))
	if err != nil {
		t.Errorf("path in custom allowed dir should be accepted: %v", err)
	}

	err = validateFilePath("/some/other/path.csv")
	if err == nil {
		t.Error("path outside custom dir should be blocked")
	}
}

func containsSubstring(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || len(s) > 0 && findSubstring(s, sub))
}

func findSubstring(s, sub string) bool {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
