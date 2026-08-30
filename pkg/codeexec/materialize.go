package codeexec

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

// Materialization: the embedded wrapper written once per content to a
// directory any process on the host can share safely. The directory
// name carries the content hash, so a version bump (or any edit) lands
// in a fresh directory and concurrent processes running different
// binaries never fight over one path. Idempotent; verified by hash, so
// a torn earlier write is repaired rather than trusted.

var (
	materializeOnce sync.Once
	materializedDir string
	materializeErr  error
)

// WrapperPath returns the on-disk path of wrapper.py, materializing the
// embedded files on first use.
func WrapperPath() (string, error) {
	materializeOnce.Do(func() {
		materializedDir, materializeErr = materialize()
	})
	if materializeErr != nil {
		return "", materializeErr
	}
	return filepath.Join(materializedDir, "wrapper.py"), nil
}

func materialize() (string, error) {
	files := []string{"wrapper.py", "version.py"}
	sum := sha256.New()
	contents := make(map[string][]byte, len(files))
	for _, name := range files {
		raw, err := pywrapperFS.ReadFile("pywrapper/" + name)
		if err != nil {
			return "", fmt.Errorf("embedded %s unreadable: %w", name, err)
		}
		contents[name] = raw
		sum.Write([]byte(name))
		sum.Write(raw)
	}
	dir := filepath.Join(os.TempDir(),
		fmt.Sprintf("brokoli-pywrapper-v%d-%s", wrapperVersion, hex.EncodeToString(sum.Sum(nil))[:16]))

	if verifyMaterialized(dir, contents) {
		return dir, nil
	}
	// Write to a fresh staging dir and rename into place: concurrent
	// processes either see the complete directory or their own staging.
	staging, err := os.MkdirTemp(os.TempDir(), "brokoli-pywrapper-stage-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(staging)
	for _, name := range files {
		if err := os.WriteFile(filepath.Join(staging, name), contents[name], 0o600); err != nil {
			return "", err
		}
	}
	if err := os.Chmod(staging, 0o700); err != nil { // #nosec G302 -- a directory: the execute bit is required to enter it, and 0700 is the minimal mode that still works; files inside are 0600.
		return "", err
	}
	if err := os.Rename(staging, dir); err != nil {
		// A concurrent process won the rename; accept its copy if sound.
		if verifyMaterialized(dir, contents) {
			return dir, nil
		}
		return "", fmt.Errorf("materialize wrapper: %w", err)
	}
	return dir, nil
}

func verifyMaterialized(dir string, contents map[string][]byte) bool {
	for name, want := range contents {
		got, err := os.ReadFile(filepath.Join(dir, name)) // #nosec G304 -- dir is os.TempDir + our own content hash and name is a fixed embedded-file name; nothing here is user input, and the read exists precisely to verify the content matches the embed.
		if err != nil || string(got) != string(want) {
			return false
		}
	}
	return true
}
