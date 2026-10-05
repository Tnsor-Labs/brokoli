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
	"strings"
	"time"
)

const (
	DefaultIndexURL       = "https://raw.githubusercontent.com/Tnsor-Labs/brokoli-adbc-drivers/main/index.json"
	IndexEnvVar           = "BROKOLI_DRIVER_INDEX"
	maxIndexBytes   int64 = 8 << 20
	indexTimeout          = 15 * time.Second
	downloadTimeout       = 60 * time.Second
)

var ErrDigestMismatch = errors.New("driver archive SHA-256 does not match expected digest")

// Index is the curated static native-driver catalog.
type Index struct {
	Version int          `json:"version"`
	Drivers []IndexEntry `json:"drivers"`
}

type IndexEntry struct {
	Name       string `json:"name"`
	Version    string `json:"version"`
	OS         string `json:"os"`
	Arch       string `json:"arch"`
	ArchiveURL string `json:"archive_url"`
	SHA256     string `json:"sha256"`
}

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
	client := &http.Client{Timeout: indexTimeout}
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
		if !driverNameRE.MatchString(entry.Name) || strings.TrimSpace(entry.Version) == "" || entry.OS == "" || entry.Arch == "" || entry.ArchiveURL == "" || !sha256Hex(entry.SHA256) {
			return nil, fmt.Errorf("invalid driver index entry %d", i)
		}
	}
	return &idx, nil
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
	client := &http.Client{Timeout: downloadTimeout}
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
