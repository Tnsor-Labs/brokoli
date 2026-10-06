package drivers

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"golang.org/x/mod/semver"

	"github.com/Tnsor-Labs/brokoli/pkg/archiveextract"
)

const (
	maxArchiveFileBytes  = 256 << 20
	maxArchiveTotalBytes = 512 << 20

	stagePrefix = ".stage-driver-"
	// staleStageAge is how old an abandoned staging directory must be
	// before a scan removes it. An install in flight is minutes old at
	// most; anything older was left by a crash.
	staleStageAge = time.Hour
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

// Manager scans and maintains installed native driver manifests. It is safe
// for concurrent use: the API, the worker loop and the connection resolver
// share one per directory (see Shared).
//
// Installed builds live at <dir>/<name>/<version>/<library sha256>/, so
// several builds of one driver coexist and a connection pinned to one is
// never silently moved to another.
type Manager struct {
	dir string

	mu        sync.RWMutex
	manifests map[string]*Manifest
	// digests caches library hashes by path, keyed on size and mtime, so a
	// rescan re-hashes only a library that changed. Hashing every installed
	// library (up to 256 MiB each) on every scan made each catalog view and
	// capabilities request read every driver from disk.
	digests map[string]cachedDigest
}

type cachedDigest struct {
	size    int64
	modTime time.Time
	sum     string
}

var (
	sharedMu       sync.Mutex
	sharedManagers = map[string]*Manager{}
)

// Shared returns the process-wide manager for dir, scanning it on first use.
// Every in-process consumer should use it, so an install through the API is
// visible to the worker loop and the resolver without a restart.
func Shared(dir string) (*Manager, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	sharedMu.Lock()
	defer sharedMu.Unlock()
	if m, ok := sharedManagers[abs]; ok {
		return m, nil
	}
	m, err := NewManager(abs)
	if err != nil {
		return nil, err
	}
	sharedManagers[abs] = m
	return m, nil
}

// NewManager returns a manager with dir scanned once. Most callers want
// Shared; this is for tools and tests that own a directory.
func NewManager(dir string) (*Manager, error) {
	m := &Manager{dir: dir, manifests: make(map[string]*Manifest), digests: make(map[string]cachedDigest)}
	return m, m.LoadAll()
}

func (m *Manager) Dir() string { return m.dir }

// LoadAll atomically replaces the in-memory scan. Invalid directories are
// skipped, so one corrupt driver cannot hide valid installed drivers.
func (m *Manager) LoadAll() error {
	entries, err := os.ReadDir(m.dir)
	if os.IsNotExist(err) {
		m.mu.Lock()
		m.manifests = make(map[string]*Manifest)
		m.mu.Unlock()
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
		if strings.HasPrefix(entry.Name(), stagePrefix) {
			m.removeStaleStage(entry)
			continue
		}
		root := filepath.Join(m.dir, entry.Name())
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
				dir := filepath.Join(root, version.Name(), digest.Name())
				manifest, err := m.loadCached(dir)
				if err != nil {
					continue
				}
				// A build is only addressable at the path its identity
				// names; anything else was copied or renamed by hand.
				if want, ok := identityPath(m.dir, manifest.Identity()); !ok || filepath.Clean(want) != filepath.Clean(dir) {
					continue
				}
				loaded[manifest.Identity().Key()] = manifest
			}
		}
	}
	m.mu.Lock()
	m.manifests = loaded
	m.mu.Unlock()
	return nil
}

// loadCached is LoadManifest, re-hashing the library only when its size or
// modification time changed since the last scan.
func (m *Manager) loadCached(dir string) (*Manifest, error) {
	manifest, err := parseManifest(dir)
	if err != nil {
		return nil, err
	}
	path := manifest.LibraryPath()
	info, err := os.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("stat driver library: %w", err)
	}
	m.mu.RLock()
	cached, ok := m.digests[path]
	m.mu.RUnlock()
	sum := cached.sum
	if !ok || cached.size != info.Size() || !cached.modTime.Equal(info.ModTime()) {
		sum, err = fileSHA256(path)
		if err != nil {
			return nil, fmt.Errorf("hash driver library: %w", err)
		}
		m.mu.Lock()
		m.digests[path] = cachedDigest{size: info.Size(), modTime: info.ModTime(), sum: sum}
		m.mu.Unlock()
	}
	if !strings.EqualFold(sum, manifest.LibrarySHA256) {
		return nil, fmt.Errorf("driver library digest mismatch")
	}
	return manifest, nil
}

func (m *Manager) removeStaleStage(entry os.DirEntry) {
	info, err := entry.Info()
	if err != nil || time.Since(info.ModTime()) < staleStageAge {
		return
	}
	_ = os.RemoveAll(filepath.Join(m.dir, entry.Name())) // Best effort: an abandoned install from a crashed process.
}

// List returns every installed build, by name and then newest version first.
func (m *Manager) List() []*Manifest {
	m.mu.RLock()
	out := make([]*Manifest, 0, len(m.manifests))
	for _, manifest := range m.manifests {
		out = append(out, manifest)
	}
	m.mu.RUnlock()
	sort.Slice(out, func(i, j int) bool {
		if out[i].Name != out[j].Name {
			return out[i].Name < out[j].Name
		}
		if c := CompareVersions(out[i].Version, out[j].Version); c != 0 {
			return c > 0
		}
		return out[i].LibrarySHA256 < out[j].LibrarySHA256
	})
	return out
}

// Get returns the newest installed build of name.
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
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.manifests[identity.Key()]
}

// Remove deletes every installed build of name.
func (m *Manager) Remove(name string) error {
	if !driverNameRE.MatchString(name) || m.Get(name) == nil {
		return fmt.Errorf("driver %q is not installed", name)
	}
	if err := os.RemoveAll(filepath.Join(m.dir, name)); err != nil {
		return fmt.Errorf("remove driver: %w", err)
	}
	return m.LoadAll()
}

// RemoveIdentity removes one exact installed build without affecting other
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
	return m.RemoveIdentity(matches[0].Identity())
}

// InstallArchive verifies caller-provided archive integrity, extracts into a
// same-filesystem staging directory, verifies its strict manifest and library,
// then atomically publishes it. Existing installations are never replaced.
func (m *Manager) InstallArchive(archivePath, expectedArchiveSHA256 string) (*Manifest, error) {
	return m.installArchive(archivePath, expectedArchiveSHA256, nil)
}

// installArchive is InstallArchive with an extra check of the staged
// manifest, run before anything is published.
func (m *Manager) installArchive(archivePath, expectedArchiveSHA256 string, accept func(*Manifest) error) (*Manifest, error) {
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
	stage, err := os.MkdirTemp(m.dir, stagePrefix)
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
	if accept != nil {
		if err := accept(manifest); err != nil {
			return nil, err
		}
	}
	dest, ok := identityPath(m.dir, manifest.Identity())
	if !ok {
		return nil, fmt.Errorf("driver manifest does not form a valid identity")
	}
	if _, err := os.Lstat(dest); err == nil {
		return nil, fmt.Errorf("driver %q version %q is already installed", manifest.Name, manifest.Version)
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0o750); err != nil {
		return nil, err
	}
	if err := os.Rename(stage, dest); err != nil {
		return nil, fmt.Errorf("publish driver install: %w", err)
	}
	installed, err := m.loadCached(dest)
	if err != nil {
		return nil, err
	}
	m.mu.Lock()
	m.manifests[installed.Identity().Key()] = installed
	m.mu.Unlock()
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

// CompareVersions orders two driver versions: by semantic version when both
// are one (with or without a leading "v"), otherwise lexically. A plain
// string comparison put 1.10.0 below 1.9.0.
func CompareVersions(a, b string) int {
	va, vb := canonicalSemver(a), canonicalSemver(b)
	if semver.IsValid(va) && semver.IsValid(vb) {
		if c := semver.Compare(va, vb); c != 0 {
			return c
		}
	}
	return strings.Compare(a, b)
}

func canonicalSemver(v string) string {
	if strings.HasPrefix(v, "v") {
		return v
	}
	return "v" + v
}
