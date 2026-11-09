//go:build linux

package runas

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Root only: handing files to another user needs it.
func TestClaimRefusesWhatTheChildDidNotWrite(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("handing a folder to another user needs root")
	}
	t.Setenv(EnvUIDs, "1000000000-1999999999")
	id, err := For("org/ws")
	if err != nil {
		t.Fatal(err)
	}
	dir, err := id.MkdirTemp("claim-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	secret := filepath.Join(t.TempDir(), "secret")
	if err := os.WriteFile(secret, []byte("root's"), 0o600); err != nil {
		t.Fatal(err)
	}
	mine := filepath.Join(dir, "mine")
	_ = os.WriteFile(mine, []byte("child's"), 0o600)
	_ = os.Symlink(secret, filepath.Join(dir, "link"))
	_ = os.WriteFile(filepath.Join(dir, "roots"), []byte("root's"), 0o600)
	if err := id.Own(dir); err != nil {
		t.Fatal(err)
	}
	// Written by the server after the hand-over: not the child's.
	_ = os.Lchown(filepath.Join(dir, "roots"), 0, 0)

	got, err := id.Claim(dir, "mine")
	if err != nil {
		t.Fatalf("the child's own file was refused: %v", err)
	}
	defer os.Remove(got)
	if raw, _ := os.ReadFile(got); string(raw) != "child's" {
		t.Fatalf("claimed %q", raw)
	}
	if strings.HasPrefix(got, dir) {
		t.Fatalf("claimed file %s is still in the child's folder", got)
	}
	for _, name := range []string{"link", "roots", "../escape", ""} {
		if p, err := id.Claim(dir, name); err == nil {
			_ = os.Remove(p)
			t.Errorf("Claim(%q) accepted", name)
		}
	}
	if _, err := os.Stat(secret); err != nil {
		t.Fatalf("the link's target was touched: %v", err)
	}
}
