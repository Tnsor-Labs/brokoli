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
		root := filepath.Join(m.dir, entry.Name())
		if manifest, err := LoadManifest(root); err == nil && manifest.Name == entry.Name() {
			loaded[manifestKey(manifest)] = manifest // Legacy name-only installation.
			continue
		}
		versions, _ := os.ReadDir(root)
		for _, version := range versions {
			if !version.IsDir() {
				continue
			}
			digests, _ := os.ReadDir(filepath.Join(root, version.Name()))
			for _, digest := range digests {
				if !digest.IsDir() {
					continue
				}
				manifest, err := LoadManifest(filepath.Join(root, version.Name(), digest.Name()))
				if err == nil {
					loaded[manifestKey(manifest)] = manifest
				}
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
	sort.Slice(out, func(i, j int) bool {
		if out[i].Name != out[j].Name {
			return out[i].Name < out[j].Name
		}
		return out[i].Version > out[j].Version
	})
	return out
}

func (m *Manager) Get(name string) *Manifest {
	for _, manifest := range m.List() {
		if manifest.Name == name {
			return manifest
		}
	}
	return nil
}

// GetIdentity returns exactly the artifact selected by a saved connection.
func (m *Manager) GetIdentity(identity DriverIdentity) *Manifest {
	return m.manifests[manifestKeyIdentity(identity)]
}

func (m *Manager) Remove(name string) error {
	if m.Get(name) == nil {
		return fmt.Errorf("driver %q is not installed", name)
	}
	if err := os.RemoveAll(filepath.Join(m.dir, name)); err != nil {
		return fmt.Errorf("remove driver: %w", err)
	}
	return m.LoadAll()
}

// RemoveIdentity removes one exact installed version without affecting other
// releases of the same driver.
func (m *Manager) RemoveIdentity(identity DriverIdentity) error {
	manifest := m.GetIdentity(identity)
	if manifest == nil {
		return fmt.Errorf("driver %q version %q is not installed", identity.Name, identity.Version)
	}
	if err := os.RemoveAll(manifest.Dir()); err != nil {
		return fmt.Errorf("remove driver: %w", err)
	}
	return m.LoadAll()
}

// RemoveVersion removes one installed version. It refuses an ambiguous request
// when distinct library builds share a version string; callers must then use
// RemoveIdentity with the persisted connection identity.
func (m *Manager) RemoveVersion(name, version string) error {
	var matches []*Manifest
	for _, manifest := range m.List() {
		if manifest.Name == name && manifest.Version == version {
			matches = append(matches, manifest)
		}
	}
	if len(matches) == 0 {
		return fmt.Errorf("driver %q version %q is not installed", name, version)
	}
	if len(matches) != 1 {
		return fmt.Errorf("driver %q version %q is ambiguous; remove by exact identity", name, version)
	}
	return m.RemoveIdentity(DriverIdentity{Name: matches[0].Name, Version: matches[0].Version, LibrarySHA256: matches[0].LibrarySHA256})
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
	dest := filepath.Join(m.dir, manifest.Name, manifest.Version, strings.ToLower(manifest.LibrarySHA256))
	if _, err := os.Lstat(dest); err == nil {
		return nil, fmt.Errorf("driver %q is already installed", manifest.Name)
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0o750); err != nil {
		return nil, err
	}
	if err := os.Rename(stage, dest); err != nil {
		return nil, fmt.Errorf("publish driver install: %w", err)
	}
	installed, err := LoadManifest(dest)
	if err != nil {
		return nil, err
	}
	m.manifests[manifestKey(installed)] = installed
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

func manifestKey(manifest *Manifest) string {
	return manifestKeyIdentity(DriverIdentity{Name: manifest.Name, Version: manifest.Version, LibrarySHA256: manifest.LibrarySHA256})
}
func manifestKeyIdentity(identity DriverIdentity) string {
	return identity.Name + "\x00" + identity.Version + "\x00" + strings.ToLower(identity.LibrarySHA256)
}
