package drivers

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/Tnsor-Labs/brokoli/pkg/archiveextract"
)

const (
	maxArchiveFileBytes  = 256 << 20
	maxArchiveTotalBytes = 512 << 20
)

// DefaultDir returns BROKOLI_DRIVER_DIR, then XDG's driver directory, then
// ~/.brokoli/drivers. The relative fallback keeps the function total.
func DefaultDir() string {
	if dir := os.Getenv("BROKOLI_DRIVER_DIR"); dir != "" {
		return dir
	}
	if xdg := os.Getenv("XDG_DATA_HOME"); xdg != "" {
		return filepath.Join(xdg, "brokoli", "drivers")
	}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		return filepath.Join(home, ".brokoli", "drivers")
	}
	return "drivers"
}

// Manager scans and maintains installed native driver manifests.
type Manager struct {
	dir       string
	manifests map[string]*Manifest
}

func NewManager(dir string) (*Manager, error) {
	m := &Manager{dir: dir, manifests: make(map[string]*Manifest)}
	return m, m.LoadAll()
}

func (m *Manager) Dir() string { return m.dir }

// LoadAll atomically replaces the in-memory scan. Invalid directories are
// skipped, so one corrupt driver cannot hide valid installed drivers.
func (m *Manager) LoadAll() error {
	entries, err := os.ReadDir(m.dir)
	if os.IsNotExist(err) {
		m.manifests = make(map[string]*Manifest)
		return nil
	}
	if err != nil {
		return fmt.Errorf("read driver directory: %w", err)
	}
	loaded := make(map[string]*Manifest)
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		manifest, err := LoadManifest(filepath.Join(m.dir, entry.Name()))
		if err == nil && manifest.Name == entry.Name() {
			if _, duplicate := loaded[manifest.Name]; !duplicate {
				loaded[manifest.Name] = manifest
			}
		}
	}
	m.manifests = loaded
	return nil
}

func (m *Manager) List() []*Manifest {
	out := make([]*Manifest, 0, len(m.manifests))
	for _, manifest := range m.manifests {
		out = append(out, manifest)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func (m *Manager) Get(name string) *Manifest { return m.manifests[name] }

func (m *Manager) Remove(name string) error {
	if m.Get(name) == nil {
		return fmt.Errorf("driver %q is not installed", name)
	}
	if err := os.RemoveAll(filepath.Join(m.dir, name)); err != nil {
		return fmt.Errorf("remove driver: %w", err)
	}
	return m.LoadAll()
}

// InstallArchive verifies caller-provided archive integrity, extracts into a
// same-filesystem staging directory, verifies its strict manifest and library,
// then atomically publishes it. Existing installations are never replaced.
func (m *Manager) InstallArchive(archivePath, expectedArchiveSHA256 string) (*Manifest, error) {
	if !sha256Hex(expectedArchiveSHA256) {
		return nil, fmt.Errorf("caller must supply an archive SHA-256 digest")
	}
	got, err := fileSHA256(archivePath)
	if err != nil {
		return nil, fmt.Errorf("hash driver archive: %w", err)
	}
	if !strings.EqualFold(got, expectedArchiveSHA256) {
		return nil, fmt.Errorf("driver archive digest mismatch")
	}
	if err := os.MkdirAll(m.dir, 0o750); err != nil {
		return nil, err
	}
	stage, err := os.MkdirTemp(m.dir, ".stage-driver-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(stage)
	f, err := os.Open(archivePath) // #nosec G304 -- archive is supplied to this explicit install operation
	if err != nil {
		return nil, err
	}
	extractErr := archiveextract.Extract(f, stage, archiveextract.Options{MaxFileBytes: maxArchiveFileBytes, MaxTotalBytes: maxArchiveTotalBytes})
	closeErr := f.Close()
	if extractErr != nil {
		return nil, fmt.Errorf("extract driver archive: %w", extractErr)
	}
	if closeErr != nil {
		return nil, closeErr
	}
	manifest, err := LoadManifest(stage)
	if err != nil {
		return nil, err
	}
	dest := filepath.Join(m.dir, manifest.Name)
	if _, err := os.Lstat(dest); err == nil {
		return nil, fmt.Errorf("driver %q is already installed", manifest.Name)
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	if err := os.Rename(stage, dest); err != nil {
		return nil, fmt.Errorf("publish driver install: %w", err)
	}
	installed, err := LoadManifest(dest)
	if err != nil {
		return nil, err
	}
	m.manifests[installed.Name] = installed
	return installed, nil
}

// InstallArchive installs an archive into destRoot without requiring callers
// to retain a Manager instance.
func InstallArchive(archivePath, destRoot, expectedArchiveSHA256 string) (*Manifest, error) {
	m, err := NewManager(destRoot)
	if err != nil {
		return nil, err
	}
	return m.InstallArchive(archivePath, expectedArchiveSHA256)
}

// ArchiveSHA256 returns the lowercase SHA-256 digest of an archive.
func ArchiveSHA256(path string) (string, error) { return fileSHA256(path) }
