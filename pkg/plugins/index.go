package plugins

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
	"strings"
	"time"
)

// The curated static index (ADR-016 §5): a static, versioned JSON catalog
// of installable plugins served over HTTPS — names, versions, archive
// URLs, and sha256 digests — so browse/install needs no registry
// infrastructure. Publishing a plugin is a PR to the index. The "will it
// run here?" question is deliberately NOT answered from the index: that is
// resolved at install time against the archive's own payloads, with a
// named reason (SelectPayload), so the index stays a thin catalog.

// DefaultIndexURL is the curated community index, backed by the
// brokoli-plugins repository. BROKOLI_PLUGIN_INDEX overrides it for
// mirrors and private indexes.
const DefaultIndexURL = "https://raw.githubusercontent.com/Tnsor-Labs/brokoli-plugins/main/index.json"

// IndexEnvVar is the environment override for the index URL.
const IndexEnvVar = "BROKOLI_PLUGIN_INDEX"

// maxIndexBytes bounds a fetched index so a hostile or misconfigured URL
// cannot exhaust memory. Generous for a catalog of names and URLs.
const maxIndexBytes = 8 << 20 // 8 MiB

// indexFetchTimeout bounds a single index fetch.
const indexFetchTimeout = 15 * time.Second

// archiveDownloadTimeout bounds a single archive download — larger than an
// index fetch because it moves the actual payload bytes.
const archiveDownloadTimeout = 60 * time.Second

// ErrDigestMismatch is returned when a downloaded archive's sha256 does
// not match the digest the index declared. No bytes from a mismatched
// archive are installed.
var ErrDigestMismatch = errors.New("archive sha256 does not match the index digest")

// IndexEntry is one plugin available in a curated index.
type IndexEntry struct {
	Name        string `json:"name"`
	Version     string `json:"version"`
	Description string `json:"description,omitempty"`
	ArchiveURL  string `json:"archive_url"`
	SHA256      string `json:"sha256"`
}

// Index is a static, versioned catalog of installable plugins.
type Index struct {
	Version int          `json:"version"`
	Plugins []IndexEntry `json:"plugins"`
}

// FindEntry returns the index entry with the given name, or nil.
func (idx *Index) FindEntry(name string) *IndexEntry {
	for i := range idx.Plugins {
		if idx.Plugins[i].Name == name {
			return &idx.Plugins[i]
		}
	}
	return nil
}

// DownloadArchive fetches the archive at url into destPath, verifying its
// sha256 against expectedSHA256 (hex). It refuses an empty expected digest
// — an unverifiable entry is not installable — and returns ErrDigestMismatch
// on a mismatch, so no bytes from an archive the index did not vouch for
// are ever handed to the installer.
func DownloadArchive(ctx context.Context, url, expectedSHA256, destPath string, maxBytes int64) error {
	if strings.TrimSpace(expectedSHA256) == "" {
		return errors.New("index entry has no sha256; refusing to install an unverifiable archive")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return fmt.Errorf("build archive request: %w", err)
	}
	client := &http.Client{Timeout: archiveDownloadTimeout}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("download archive: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download archive: HTTP %d from %s", resp.StatusCode, url)
	}
	// #nosec G304 -- destPath is a caller-provided server temp file, not
	// request-controlled.
	f, err := os.Create(destPath)
	if err != nil {
		return fmt.Errorf("create temp archive: %w", err)
	}
	h := sha256.New()
	n, copyErr := io.Copy(io.MultiWriter(f, h), io.LimitReader(resp.Body, maxBytes+1))
	closeErr := f.Close()
	if copyErr != nil {
		return fmt.Errorf("download archive: %w", copyErr)
	}
	if closeErr != nil {
		return fmt.Errorf("write archive: %w", closeErr)
	}
	if n > maxBytes {
		return fmt.Errorf("archive exceeds the %d-byte limit", maxBytes)
	}
	got := hex.EncodeToString(h.Sum(nil))
	if !strings.EqualFold(got, strings.TrimSpace(expectedSHA256)) {
		return fmt.Errorf("%w: index=%s downloaded=%s", ErrDigestMismatch, expectedSHA256, got)
	}
	return nil
}

// IndexURL returns the configured index URL: BROKOLI_PLUGIN_INDEX if set,
// otherwise DefaultIndexURL.
func IndexURL() string {
	if u := os.Getenv(IndexEnvVar); u != "" {
		return u
	}
	return DefaultIndexURL
}

// FetchIndex GETs and parses the plugin index at url. The response is size-
// bounded and the whole call is context-bounded by the caller.
func FetchIndex(ctx context.Context, url string) (*Index, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("build index request: %w", err)
	}
	// client.Do with a prepared request (not http.Get) so a config-driven
	// URL is not a gosec G107 taint; the URL is admin-controlled by design
	// (mirrors, private indexes).
	client := &http.Client{Timeout: indexFetchTimeout}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch plugin index: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("fetch plugin index: HTTP %d from %s", resp.StatusCode, url)
	}
	var idx Index
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxIndexBytes)).Decode(&idx); err != nil {
		return nil, fmt.Errorf("parse plugin index: %w", err)
	}
	return &idx, nil
}
