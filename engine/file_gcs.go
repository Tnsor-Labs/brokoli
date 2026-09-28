package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"cloud.google.com/go/storage"
	"github.com/Tnsor-Labs/brokoli/models"
	"github.com/Tnsor-Labs/brokoli/pkg/netguard"
	"google.golang.org/api/option"
)

const defaultGCSDownloadLimit int64 = 10 << 30

// GCSFileConfig is the customer-owned Google Cloud Storage configuration
// stored in a connection's encrypted extra JSON.
type GCSFileConfig struct {
	Bucket           string
	CredentialsJSON  string
	Endpoint         string
	MaxDownloadBytes int64
}

// GCSFileConfigFromExtra parses and validates the extra JSON used by GCS file
// nodes. A service-account key is required explicitly; ambient credentials are
// intentionally not used for customer-owned locations.
func GCSFileConfigFromExtra(extra string) (GCSFileConfig, error) {
	var raw map[string]interface{}
	if strings.TrimSpace(extra) == "" {
		return GCSFileConfig{}, fmt.Errorf("GCS extra config is required")
	}
	if err := json.Unmarshal([]byte(extra), &raw); err != nil {
		return GCSFileConfig{}, fmt.Errorf("GCS extra config must be a JSON object: %w", err)
	}
	str := func(key string) string {
		value, _ := raw[key].(string)
		return strings.TrimSpace(value)
	}
	cfg := GCSFileConfig{
		Bucket:           str("bucket"),
		CredentialsJSON:  str("credentials"),
		Endpoint:         str("endpoint"),
		MaxDownloadBytes: defaultGCSDownloadLimit,
	}
	if cfg.CredentialsJSON == "" {
		cfg.CredentialsJSON = str("credentials_json")
	}
	if cfg.Bucket == "" {
		return GCSFileConfig{}, fmt.Errorf("GCS extra config requires bucket")
	}
	if !validGCSBucketName(cfg.Bucket) {
		return GCSFileConfig{}, fmt.Errorf("GCS bucket name is invalid")
	}
	if cfg.CredentialsJSON == "" {
		return GCSFileConfig{}, fmt.Errorf("GCS extra config requires service-account credentials")
	}
	var credentialType struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal([]byte(cfg.CredentialsJSON), &credentialType); err != nil || credentialType.Type != "service_account" {
		return GCSFileConfig{}, fmt.Errorf("GCS credentials must be a service-account JSON key")
	}
	if cfg.Endpoint != "" {
		u, err := url.Parse(cfg.Endpoint)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.RawQuery != "" {
			return GCSFileConfig{}, fmt.Errorf("GCS endpoint must be an http or https URL without a query string")
		}
	}
	if value, ok := raw["max_download_bytes"]; ok {
		n, err := positiveWholeNumber(value)
		if err != nil {
			return GCSFileConfig{}, fmt.Errorf("GCS max_download_bytes must be a positive whole number")
		}
		cfg.MaxDownloadBytes = n
	}
	return cfg, nil
}

func validGCSBucketName(name string) bool {
	if len(name) < 3 || len(name) > 63 || name[0] == '.' || name[0] == '-' || name[len(name)-1] == '.' || name[len(name)-1] == '-' {
		return false
	}
	for i, r := range name {
		if (r < 'a' || r > 'z') && (r < '0' || r > '9') && r != '.' && r != '-' && r != '_' {
			return false
		}
		if i > 0 && r == '.' && name[i-1] == '.' {
			return false
		}
	}
	return true
}

type gcsFileClient struct {
	client *storage.Client
	bucket *storage.BucketHandle
	name   string
	limit  int64
}

func newGCSFileClient(ctx context.Context, conn *models.Connection) (*gcsFileClient, error) {
	cfg, err := GCSFileConfigFromExtra(conn.Extra)
	if err != nil {
		return nil, err
	}
	opts := []option.ClientOption{
		option.WithCredentialsJSON([]byte(cfg.CredentialsJSON)),
		option.WithHTTPClient(netguard.Outbound().Client(0)),
	}
	if cfg.Endpoint != "" {
		opts = append(opts, option.WithEndpoint(cfg.Endpoint))
	}
	client, err := storage.NewClient(ctx, opts...)
	if err != nil {
		return nil, fmt.Errorf("create GCS client: %w", err)
	}
	return &gcsFileClient{client: client, bucket: client.Bucket(cfg.Bucket), name: cfg.Bucket, limit: cfg.MaxDownloadBytes}, nil
}

// TestGCSConnection authenticates against the configured bucket through the
// same client path used by file nodes.
func TestGCSConnection(ctx context.Context, extra map[string]interface{}) error {
	encoded, err := json.Marshal(extra)
	if err != nil {
		return fmt.Errorf("encode GCS config: %w", err)
	}
	client, err := newGCSFileClient(ctx, &models.Connection{Extra: string(encoded)})
	if err != nil {
		return err
	}
	_, err = client.bucket.Attrs(ctx)
	if err != nil {
		return fmt.Errorf("check GCS bucket %q: %w", client.name, err)
	}
	return nil
}

func (c *gcsFileClient) download(ctx context.Context, key, destination string) (int64, error) {
	if key == "" {
		return 0, fmt.Errorf("GCS object name is required")
	}
	reader, err := c.bucket.Object(key).NewReader(ctx)
	if err != nil {
		return 0, fmt.Errorf("open GCS object %q: %w", key, err)
	}
	defer reader.Close() //nolint:errcheck
	if reader.Attrs.Size > c.limit {
		return 0, fmt.Errorf("GCS object %q is %d bytes, over the %d-byte download limit", key, reader.Attrs.Size, c.limit)
	}
	if err := os.MkdirAll(filepath.Dir(destination), 0o700); err != nil {
		return 0, fmt.Errorf("create GCS download directory: %w", err)
	}
	f, err := os.Create(destination) // #nosec G304 -- destination is a fixed filename inside a process-created scratch directory.
	if err != nil {
		return 0, fmt.Errorf("create GCS download: %w", err)
	}
	defer f.Close() //nolint:errcheck
	counted := &countingWriter{w: f}
	if _, err := io.Copy(counted, io.LimitReader(reader, c.limit+1)); err != nil {
		return 0, fmt.Errorf("download GCS object %q: %w", key, err)
	}
	if counted.n > c.limit {
		return 0, fmt.Errorf("GCS object %q exceeds the %d-byte download limit", key, c.limit)
	}
	if err := f.Close(); err != nil {
		return 0, fmt.Errorf("finish GCS download: %w", err)
	}
	return counted.n, nil
}

func (c *gcsFileClient) upload(ctx context.Context, key string, write func(io.Writer) error) (int64, error) {
	if key == "" {
		return 0, fmt.Errorf("GCS object name is required")
	}
	writer := c.bucket.Object(key).NewWriter(ctx)
	counted := &countingWriter{w: writer}
	writeErr := write(counted)
	if writeErr != nil {
		_ = writer.Close()
		return 0, writeErr
	}
	if err := writer.Close(); err != nil {
		return 0, fmt.Errorf("upload GCS object %q: %w", key, err)
	}
	return counted.n, nil
}
