//go:build !linux

package runas

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

// syscallNonblock opens without waiting for a FIFO's writer.
const syscallNonblock = 0

// supported reports whether this platform can start a child as another user.
const supported = false

// Apply does nothing here: For refuses an enabled identity off Linux.
func (id Identity) Apply(*exec.Cmd) {}

// MkdirTemp is os.MkdirTemp("", pattern).
func (id Identity) MkdirTemp(pattern string) (string, error) { return os.MkdirTemp("", pattern) }

// Own does nothing here.
func (id Identity) Own(string) error { return nil }

// Claim moves dir/name to the system temp directory, so it outlives dir,
// and returns its new path for the caller to read and remove.
func (id Identity) Claim(dir, name string) (string, error) {
	if name == "" || filepath.Base(name) != name {
		return "", fmt.Errorf("claim %q: not a name directly inside %s", name, dir)
	}
	var suffix [8]byte
	_, _ = rand.Read(suffix[:])
	dst := filepath.Join(os.TempDir(), "brokoli-"+hex.EncodeToString(suffix[:])+"-"+name)
	if err := os.Rename(filepath.Join(dir, name), dst); err != nil {
		return "", fmt.Errorf("claim %s: %w", name, err)
	}
	return dst, nil
}

// Env is env unchanged.
func (id Identity) Env(env []string, _ string) []string { return env }

// Check refuses the setting off Linux.
func Check() error {
	if _, _, ok, err := uidRange(); err != nil {
		return err
	} else if ok {
		return ErrNotSupported
	}
	return nil
}
