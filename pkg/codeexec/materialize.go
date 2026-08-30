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
	materializeOnce   sync.Once
	materializedDir   string
	materializeErr    error
	jsMaterializeOnce sync.Once
	jsMaterializedDir string
	jsMaterializeErr  error
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

// JSWrapperPath returns the on-disk path of worker_main.mjs,
// materializing all sibling imports on first use.
func JSWrapperPath() (string, error) {
	jsMaterializeOnce.Do(func() {
		jsMaterializedDir, jsMaterializeErr = materializeJS()
	})
	if jsMaterializeErr != nil {
		return "", jsMaterializeErr
	}
	return filepath.Join(jsMaterializedDir, "worker_main.mjs"), nil
}

func materialize() (string, error) {
	files := []string{"wrapper.py", "version.py", "protocol.py", "worker_main.py"}
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

func materializeJS() (string, error) {
	files := []string{"worker_main.mjs", "protocol.mjs", "contract.mjs", "version.mjs"}
	sum := sha256.New()
	contents := make(map[string][]byte, len(files))
	for _, name := range files {
		raw, err := jswrapperFS.ReadFile("jswrapper/" + name)
		if err != nil {
			return "", fmt.Errorf("embedded %s unreadable: %w", name, err)
		}
		contents[name] = raw
		sum.Write([]byte(name))
		sum.Write(raw)
	}
	prefix := fmt.Sprintf("brokoli-jswrapper-v%d-%s-", jsWrapperVersion, hex.EncodeToString(sum.Sum(nil))[:16])
	dir, err := os.MkdirTemp(os.TempDir(), prefix)
	if err != nil {
		return "", err
	}
	remove := true
	defer func() {
		if remove {
			_ = os.RemoveAll(dir)
		}
	}()
	for _, name := range files {
		if err := os.WriteFile(filepath.Join(dir, name), contents[name], 0o600); err != nil {
			return "", err
		}
	}
	if err := os.Chmod(dir, 0o700); err != nil { // #nosec G302 -- directory execute permission is required; files remain 0600.
		return "", err
	}
	remove = false
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
