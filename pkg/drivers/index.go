package drivers

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/Tnsor-Labs/brokoli/pkg/netguard"
)

const (
	// IndexEnvVar names the curated catalog this server installs from. There
	// is deliberately no default: the index decides which native code a
	// one-click install loads, and until it is signed, trusting one is a
	// decision the operator makes, not one the binary makes for them.
	IndexEnvVar                  = "BROKOLI_DRIVER_INDEX"
	indexCacheTTL                = 5 * time.Minute
	maxIndexBytes          int64 = 8 << 20
	indexTimeout                 = 15 * time.Second
	downloadTimeout              = 60 * time.Second
	maxCatalogArchiveBytes       = 512 << 20
	maxDocumentationBytes  int64 = 1 << 20
)

var (
	ErrDigestMismatch = errors.New("driver archive SHA-256 does not match expected digest")
	// ErrCatalogNotConfigured means no index is configured (IndexEnvVar).
	ErrCatalogNotConfigured = errors.New("native driver catalog is not configured; set " + IndexEnvVar)
	// ErrCatalogUnavailable wraps a failure to reach or read the catalog or
	// an artifact it lists: an upstream problem, not a bad request.
	ErrCatalogUnavailable = errors.New("native driver catalog is unavailable")
	// ErrNotInCatalog means the catalog has no installable release matching
	// the request on this platform.
	ErrNotInCatalog = errors.New("driver release is not in the catalog for this platform")
)

// Index is the curated static native-driver catalog.
type Index struct {
	Version int          `json:"version"`
	Drivers []IndexEntry `json:"drivers"`
}

// Documentation returns catalog-approved Markdown for one platform release.
func Documentation(ctx context.Context, name, version string) (string, error) {
	index, err := CachedIndex(ctx)
	if err != nil {
		return "", err
	}
	for _, entry := range index.Drivers {
		if entry.Name != name || entry.Version != version || entry.OS != runtime.GOOS || entry.Arch != runtime.GOARCH {
			continue
		}
		if entry.DocsURL == "" {
			return "", fmt.Errorf("%w: no documentation is published for %s %s", ErrNotInCatalog, name, version)
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, entry.DocsURL, nil)
		if err != nil {
			return "", err
		}
		response, err := netguard.Outbound().Client(indexTimeout).Do(req)
		if err != nil {
			return "", fmt.Errorf("%w: fetch documentation: %v", ErrCatalogUnavailable, err)
		}
		defer response.Body.Close()
		if response.StatusCode != http.StatusOK {
			return "", fmt.Errorf("%w: fetch documentation: HTTP %d", ErrCatalogUnavailable, response.StatusCode)
		}
		body, err := io.ReadAll(io.LimitReader(response.Body, maxDocumentationBytes+1))
		if err != nil {
			return "", err
		}
		if int64(len(body)) > maxDocumentationBytes {
			return "", fmt.Errorf("documentation exceeds size limit")
		}
		return string(body), nil
	}
	return "", fmt.Errorf("%w: %s %s", ErrNotInCatalog, name, version)
}

// InstallFromCatalog installs name's current-platform artifact from the
// operator-selected curated index. Archive URLs and digests are never supplied
// by the API caller.
func (m *Manager) InstallFromCatalog(ctx context.Context, name string) (*Manifest, error) {
	return m.InstallFromCatalogVersion(ctx, name, "")
}

// InstallFromCatalogVersion installs an exact catalog version. An empty
// version selects the newest installable release for this platform.
//
// The archive is checked against the entry it was selected by before it is
// published: its manifest must name the same driver and version. The digest
// proves the bytes are the ones the catalog listed; this proves the catalog
// listed them under the right name, so an entry for one driver cannot
// install another.
func (m *Manager) InstallFromCatalogVersion(ctx context.Context, name, version string) (*Manifest, error) {
	indexURL := IndexURL()
	if indexURL == "" {
		return nil, ErrCatalogNotConfigured
	}
	// Installs read the index fresh, so a release revoked minutes ago is
	// not installed from a cached copy.
	index, err := FetchIndex(ctx, indexURL)
	if err != nil {
		return nil, err
	}
	entry := index.Select(name, version)
	if entry == nil {
		if version == "" {
			return nil, fmt.Errorf("%w: %s on %s/%s", ErrNotInCatalog, name, runtime.GOOS, runtime.GOARCH)
		}
		return nil, fmt.Errorf("%w: %s %s on %s/%s", ErrNotInCatalog, name, version, runtime.GOOS, runtime.GOARCH)
	}
	archive, err := os.CreateTemp("", "brokoli-driver-*.tar.gz")
	if err != nil {
		return nil, fmt.Errorf("create driver archive: %w", err)
	}
	archivePath := archive.Name()
	if err := archive.Close(); err != nil {
		_ = os.Remove(archivePath) // Best-effort cleanup after a failed temporary-file close.
		return nil, err
	}
	defer func() { _ = os.Remove(archivePath) }()
	if err := DownloadArchive(ctx, entry.ArchiveURL, entry.SHA256, archivePath, maxCatalogArchiveBytes); err != nil {
		return nil, err
	}
	return m.installArchive(archivePath, entry.SHA256, func(manifest *Manifest) error {
		if manifest.Name != entry.Name || manifest.Version != entry.Version {
			return fmt.Errorf("driver archive contains %s %s, but the catalog entry is %s %s", manifest.Name, manifest.Version, entry.Name, entry.Version)
		}
		return nil
	})
}

// Select returns the installable release of name for this platform: exactly
// version, or the newest by semantic version when version is empty.
func (idx *Index) Select(name, version string) *IndexEntry {
	var entry *IndexEntry
	for i := range idx.Drivers {
		candidate := &idx.Drivers[i]
		if candidate.Name != name || candidate.OS != runtime.GOOS || candidate.Arch != runtime.GOARCH || !candidate.Installable() {
			continue
		}
		if version != "" && candidate.Version != version {
			continue
		}
		if entry == nil || CompareVersions(candidate.Version, entry.Version) > 0 {
			entry = candidate
		}
	}
	return entry
}

type IndexEntry struct {
	Name        string     `json:"name"`
	DisplayName string     `json:"display_name,omitempty"`
	Description string     `json:"description,omitempty"`
	Icon        string     `json:"icon,omitempty"`
	IconURL     string     `json:"icon_url,omitempty"`
	License     string     `json:"license,omitempty"`
	Homepage    string     `json:"homepage,omitempty"`
	DocsURL     string     `json:"docs_url,omitempty"`
	Lifecycle   string     `json:"lifecycle,omitempty"`
	ADBCVersion string     `json:"adbc_version,omitempty"`
	MinBrokoli  string     `json:"min_brokoli,omitempty"`
	Advisories  []Advisory `json:"advisories,omitempty"`
	Version     string     `json:"version"`
	OS          string     `json:"os"`
	Arch        string     `json:"arch"`
	ArchiveURL  string     `json:"archive_url"`
	SHA256      string     `json:"sha256"`
}

// Advisory is a reviewed security or support notice associated with a release.
type Advisory struct {
	ID          string `json:"id"`
	Severity    string `json:"severity"`
	Summary     string `json:"summary"`
	FixedIn     string `json:"fixed_in,omitempty"`
	PublishedAt string `json:"published_at,omitempty"`
}

// Installable reports whether a release can be newly installed. Revoked
// releases remain visible so operators can remediate pinned connections.
func (e IndexEntry) Installable() bool { return e.Lifecycle != "revoked" }

// IndexURL is the operator-configured catalog, or "" when there is none.
func IndexURL() string { return strings.TrimSpace(os.Getenv(IndexEnvVar)) }

var indexCache struct {
	sync.Mutex
	url     string
	fetched time.Time
	index   *Index
}

// CachedIndex returns the configured catalog, fetching it at most once per
// indexCacheTTL. Browsing the catalog and its documentation use it; an
// install does not.
func CachedIndex(ctx context.Context) (*Index, error) {
	url := IndexURL()
	if url == "" {
		return nil, ErrCatalogNotConfigured
	}
	indexCache.Lock()
	defer indexCache.Unlock()
	if indexCache.index != nil && indexCache.url == url && time.Since(indexCache.fetched) < indexCacheTTL {
		return indexCache.index, nil
	}
	index, err := FetchIndex(ctx, url)
	if err != nil {
		return nil, err
	}
	indexCache.url, indexCache.fetched, indexCache.index = url, time.Now(), index
	return index, nil
}

func FetchIndex(ctx context.Context, url string) (*Index, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("build driver index request: %w", err)
	}
	client := netguard.Outbound().Client(indexTimeout)
	resp, err := client.Do(req) // #nosec G107 -- URL is explicitly operator-configured
	if err != nil {
		return nil, fmt.Errorf("%w: fetch driver index: %v", ErrCatalogUnavailable, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%w: fetch driver index: HTTP %d", ErrCatalogUnavailable, resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxIndexBytes+1))
	if err != nil {
		return nil, fmt.Errorf("%w: read driver index: %v", ErrCatalogUnavailable, err)
	}
	if int64(len(body)) > maxIndexBytes {
		return nil, fmt.Errorf("%w: driver index exceeds %d bytes", ErrCatalogUnavailable, maxIndexBytes)
	}
	// Unknown fields are ignored, not refused: the catalog is published
	// independently of every deployed binary, and refusing them would mean
	// no field could ever be added without breaking every older server.
	// A change older servers must not misread bumps Version instead.
	var idx Index
	if err := json.Unmarshal(body, &idx); err != nil {
		return nil, fmt.Errorf("%w: parse driver index: %v", ErrCatalogUnavailable, err)
	}
	if idx.Version != 1 {
		return nil, fmt.Errorf("%w: unsupported driver index version %d", ErrCatalogUnavailable, idx.Version)
	}
	for i, entry := range idx.Drivers {
		if err := entry.validate(); err != nil {
			return nil, fmt.Errorf("%w: invalid driver index entry %d: %v", ErrCatalogUnavailable, i, err)
		}
	}
	return &idx, nil
}

func (e IndexEntry) validate() error {
	switch {
	case !driverNameRE.MatchString(e.Name):
		return fmt.Errorf("invalid name %q", e.Name)
	case !versionRE.MatchString(e.Version):
		return fmt.Errorf("invalid version %q", e.Version)
	case e.OS == "" || e.Arch == "":
		return fmt.Errorf("os and arch are required")
	case !sha256Hex(e.SHA256):
		return fmt.Errorf("sha256 must be a SHA-256 hex digest")
	case !validLifecycle(e.Lifecycle):
		return fmt.Errorf("invalid lifecycle %q", e.Lifecycle)
	}
	// The archive is verified by digest, so a plain-HTTP mirror inside an
	// air-gapped network is acceptable. Documentation and icons are not
	// verified by anything and are shown to people, so they must be HTTPS.
	if !hasScheme(e.ArchiveURL, "https://", "http://") {
		return fmt.Errorf("archive_url must be an http(s) URL")
	}
	if e.DocsURL != "" && !hasScheme(e.DocsURL, "https://") {
		return fmt.Errorf("docs_url must be an https URL")
	}
	if e.IconURL != "" && !hasScheme(e.IconURL, "https://") {
		return fmt.Errorf("icon_url must be an https URL")
	}
	return nil
}

func hasScheme(url string, schemes ...string) bool {
	for _, scheme := range schemes {
		if strings.HasPrefix(strings.ToLower(url), scheme) && len(url) > len(scheme) {
			return true
		}
	}
	return false
}

func validLifecycle(value string) bool {
	return value == "" || value == "supported" || value == "deprecated" || value == "revoked"
}

// DownloadArchive writes at most maxBytes after verifying the expected digest.
func DownloadArchive(ctx context.Context, url, expectedSHA256, destPath string, maxBytes int64) error {
	if !sha256Hex(expectedSHA256) {
		return fmt.Errorf("expected archive SHA-256 is required")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return fmt.Errorf("build driver archive request: %w", err)
	}
	client := netguard.Outbound().Client(downloadTimeout)
	resp, err := client.Do(req) // #nosec G107 -- URL comes from operator-selected catalog
	if err != nil {
		return fmt.Errorf("%w: download driver archive: %v", ErrCatalogUnavailable, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%w: download driver archive: HTTP %d", ErrCatalogUnavailable, resp.StatusCode)
	}
	f, err := os.OpenFile(destPath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600) // #nosec G304 -- caller-controlled temporary destination
	if err != nil {
		return err
	}
	h := sha256.New()
	n, copyErr := io.Copy(io.MultiWriter(f, h), io.LimitReader(resp.Body, maxBytes+1))
	closeErr := f.Close()
	if copyErr != nil {
		return copyErr
	}
	if closeErr != nil {
		return closeErr
	}
	if n > maxBytes {
		return fmt.Errorf("driver archive exceeds the %d-byte limit", maxBytes)
	}
	if !strings.EqualFold(hex.EncodeToString(h.Sum(nil)), expectedSHA256) {
		return ErrDigestMismatch
	}
	return nil
}
