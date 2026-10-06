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
	"time"

	"github.com/Tnsor-Labs/brokoli/pkg/netguard"
)

const (
	DefaultIndexURL              = "https://raw.githubusercontent.com/Tnsor-Labs/brokoli-adbc-drivers/main/index.json"
	IndexEnvVar                  = "BROKOLI_DRIVER_INDEX"
	maxIndexBytes          int64 = 8 << 20
	indexTimeout                 = 15 * time.Second
	downloadTimeout              = 60 * time.Second
	maxCatalogArchiveBytes       = 512 << 20
	maxDocumentationBytes  int64 = 1 << 20
)

var ErrDigestMismatch = errors.New("driver archive SHA-256 does not match expected digest")

// Index is the curated static native-driver catalog.
type Index struct {
	Version int          `json:"version"`
	Drivers []IndexEntry `json:"drivers"`
}

// Documentation returns catalog-approved Markdown for one platform release.
func Documentation(ctx context.Context, name, version string) (string, error) {
	index, err := FetchIndex(ctx, IndexURL())
	if err != nil {
		return "", err
	}
	for _, entry := range index.Drivers {
		if entry.Name != name || entry.Version != version || entry.OS != runtime.GOOS || entry.Arch != runtime.GOARCH {
			continue
		}
		if entry.DocsURL == "" {
			return "", fmt.Errorf("documentation is unavailable")
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, entry.DocsURL, nil)
		if err != nil {
			return "", err
		}
		response, err := netguard.Outbound().Client(indexTimeout).Do(req)
		if err != nil {
			return "", fmt.Errorf("fetch documentation: %w", err)
		}
		defer response.Body.Close()
		if response.StatusCode != http.StatusOK {
			return "", fmt.Errorf("fetch documentation: HTTP %d", response.StatusCode)
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
	return "", fmt.Errorf("driver release is not available")
}

// InstallFromCatalog installs name's current-platform artifact from the
// operator-selected curated index. Archive URLs and digests are never supplied
// by the API caller.
func (m *Manager) InstallFromCatalog(ctx context.Context, name string) (*Manifest, error) {
	return m.InstallFromCatalogVersion(ctx, name, "")
}

// InstallFromCatalogVersion installs an exact catalog version. An empty
// version selects the highest lexicographic supported version for callers that
// intentionally want the catalog default.
func (m *Manager) InstallFromCatalogVersion(ctx context.Context, name, version string) (*Manifest, error) {
	indexURL := IndexURL()
	if indexURL == "" {
		return nil, fmt.Errorf("native driver catalog is not configured; set %s", IndexEnvVar)
	}
	index, err := FetchIndex(ctx, indexURL)
	if err != nil {
		return nil, err
	}
	var entry *IndexEntry
	for i := range index.Drivers {
		candidate := &index.Drivers[i]
		if candidate.Name != name || candidate.OS != runtime.GOOS || candidate.Arch != runtime.GOARCH || !candidate.Installable() || version != "" && candidate.Version != version {
			continue
		}
		if entry == nil || candidate.Version > entry.Version {
			entry = candidate
		}
	}
	if entry == nil {
		return nil, fmt.Errorf("driver %q version %q is not available for %s/%s", name, version, runtime.GOOS, runtime.GOARCH)
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
	return m.InstallArchive(archivePath, entry.SHA256)
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

func (idx *Index) FindEntry(name string) *IndexEntry {
	for i := range idx.Drivers {
		if idx.Drivers[i].Name == name {
			return &idx.Drivers[i]
		}
	}
	return nil
}

func IndexURL() string {
	if url := os.Getenv(IndexEnvVar); url != "" {
		return url
	}
	return DefaultIndexURL
}

func FetchIndex(ctx context.Context, url string) (*Index, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("build driver index request: %w", err)
	}
	client := netguard.Outbound().Client(indexTimeout)
	resp, err := client.Do(req) // #nosec G107 -- URL is explicitly operator-configured
	if err != nil {
		return nil, fmt.Errorf("fetch driver index: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("fetch driver index: HTTP %d", resp.StatusCode)
	}
	var idx Index
	dec := json.NewDecoder(io.LimitReader(resp.Body, maxIndexBytes+1))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&idx); err != nil {
		return nil, fmt.Errorf("parse driver index: %w", err)
	}
	if idx.Version != 1 {
		return nil, fmt.Errorf("unsupported driver index version %d", idx.Version)
	}
	for i, entry := range idx.Drivers {
		if !driverNameRE.MatchString(entry.Name) || strings.TrimSpace(entry.Version) == "" || entry.OS == "" || entry.Arch == "" || entry.ArchiveURL == "" || !sha256Hex(entry.SHA256) || !validLifecycle(entry.Lifecycle) || entry.IconURL != "" && !strings.HasPrefix(entry.IconURL, "https://") {
			return nil, fmt.Errorf("invalid driver index entry %d", i)
		}
	}
	return &idx, nil
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
		return fmt.Errorf("download driver archive: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download driver archive: HTTP %d", resp.StatusCode)
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
