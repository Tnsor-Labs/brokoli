package plugins

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
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
