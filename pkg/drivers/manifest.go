// Package drivers manages curated native ADBC driver files. It deliberately
// does not load or execute them; consumers can opt into that separately.
package drivers

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
)

const manifestFile = "manifest.json"

// Manifest describes one native ADBC library contained in an archive.
type Manifest struct {
	Name          string `json:"name"`
	Version       string `json:"version"`
	OS            string `json:"os"`
	Arch          string `json:"arch"`
	Library       string `json:"library"`
	Entrypoint    string `json:"entrypoint"`
	LibrarySHA256 string `json:"library_sha256"`
	ArchiveSHA256 string `json:"archive_sha256"`

	dir string
}

func (m *Manifest) Dir() string { return m.dir }

// LibraryPath resolves Library under the installed driver directory.
func (m *Manifest) LibraryPath() string { return filepath.Join(m.dir, filepath.FromSlash(m.Library)) }

func (m *Manifest) Validate() error {
	if !driverNameRE.MatchString(m.Name) {
		return fmt.Errorf("invalid driver name %q", m.Name)
	}
	if strings.TrimSpace(m.Version) == "" {
		return fmt.Errorf("version is required")
	}
	if m.OS == "" || m.Arch == "" {
		return fmt.Errorf("os and arch are required")
	}
	if m.OS != runtime.GOOS || m.Arch != runtime.GOARCH {
		return fmt.Errorf("driver is for %s/%s, host is %s/%s", m.OS, m.Arch, runtime.GOOS, runtime.GOARCH)
	}
	if !safeRelativePath(m.Library) {
		return fmt.Errorf("library %q must be a clean relative path", m.Library)
	}
	if strings.TrimSpace(m.Entrypoint) == "" {
		return fmt.Errorf("entrypoint is required")
	}
	if !sha256Hex(m.LibrarySHA256) || !sha256Hex(m.ArchiveSHA256) {
		return fmt.Errorf("library_sha256 and archive_sha256 must be SHA-256 hex digests")
	}
	return nil
}

// LoadManifest strictly parses an installed manifest and verifies the library
// remains inside its directory and still has the recorded digest.
func LoadManifest(dir string) (*Manifest, error) {
	raw, err := os.ReadFile(filepath.Join(dir, manifestFile))
	if err != nil {
		return nil, fmt.Errorf("read driver manifest: %w", err)
	}
	var m Manifest
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&m); err != nil {
		return nil, fmt.Errorf("parse driver manifest: %w", err)
	}
	var extra any
	if err := dec.Decode(&extra); err == nil {
		return nil, fmt.Errorf("parse driver manifest: trailing JSON values")
	} else if err != io.EOF {
		return nil, fmt.Errorf("parse driver manifest: %w", err)
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	m.dir = abs
	if err := m.Validate(); err != nil {
		return nil, fmt.Errorf("invalid driver manifest: %w", err)
	}
	got, err := fileSHA256(m.LibraryPath())
	if err != nil {
		return nil, fmt.Errorf("hash driver library: %w", err)
	}
	if !strings.EqualFold(got, m.LibrarySHA256) {
		return nil, fmt.Errorf("driver library digest mismatch")
	}
	return &m, nil
}

func safeRelativePath(path string) bool {
	if path == "" || filepath.IsAbs(path) || strings.Contains(path, "\\") {
		return false
	}
	return path == filepath.ToSlash(filepath.Clean(path)) && path != "." && !strings.HasPrefix(path, "../")
}

func sha256Hex(s string) bool {
	if len(s) != sha256.Size*2 {
		return false
	}
	_, err := hex.DecodeString(s)
	return err == nil
}

func fileSHA256(path string) (string, error) {
	f, err := os.Open(path) // #nosec G304 -- path is validated from a manager-owned manifest directory
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

var driverNameRE = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,63}$`)
