package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	googlehttp "cloud.google.com/go/auth/httptransport"
	"github.com/Tnsor-Labs/brokoli/models"
	"github.com/Tnsor-Labs/brokoli/pkg/identity"
	"github.com/Tnsor-Labs/brokoli/pkg/netguard"
	"google.golang.org/api/googleapi"
	"google.golang.org/api/option"
	storagev1 "google.golang.org/api/storage/v1"
)

const defaultGCSDownloadLimit int64 = 10 << 30

// GCSFileConfig is the customer-owned Google Cloud Storage configuration
// stored in a connection's encrypted extra JSON.
type GCSFileConfig struct {
	Bucket           string
	MaxDownloadBytes int64
}

// GCSFileConfigFromExtra parses and validates the extra JSON used by GCS file
// nodes. Authentication is googleCredentials': a service-account key under
// "credentials", auth_method "oidc", or the machine's own identity where
// ambient identity is allowed.
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
		MaxDownloadBytes: defaultGCSDownloadLimit,
	}
	if cfg.Bucket == "" {
		return GCSFileConfig{}, fmt.Errorf("GCS extra config requires bucket")
	}
	if !validGCSBucketName(cfg.Bucket) {
		return GCSFileConfig{}, fmt.Errorf("GCS bucket name is invalid")
	}
	// The API endpoint is not a connection setting: whoever controls it
	// receives the connection's access token. It is set only by tests
	// (gcsAPIEndpoint), as BigQuery's is.
	if str("endpoint") != "" {
		return GCSFileConfig{}, fmt.Errorf("GCS endpoint is not a connection setting")
	}
	// A key, when given, must be a service-account key; the full identity
	// rules (oidc, the machine's identity) are googleCredentials'. Checked
	// here too so a bad key is refused when the connection is saved and
	// tested, not only when a node runs.
	if key := str("credentials"); key != "" {
		var credentialType struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal([]byte(key), &credentialType); err != nil || credentialType.Type != "service_account" {
			return GCSFileConfig{}, fmt.Errorf("GCS credentials must be a service-account JSON key")
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

// gcsAPIEndpoint replaces the Cloud Storage JSON API endpoint. Tests only
// (the fake-gcs-server integration tests); a connection cannot set it.
var gcsAPIEndpoint string

const gcsScope = "https://www.googleapis.com/auth/devstorage.read_write"

// gcsFileClient talks to Cloud Storage through the JSON API
// (google.golang.org/api/storage/v1) rather than cloud.google.com/go/storage,
// which carries a gRPC stack worth about 22 MB of binary for no feature file
// nodes use. It is plain HTTP, so the one authenticated client below is all
// it has: credentials layered over the netguard transport (the BigQuery
// lesson -- option.WithHTTPClient beside option.WithAuthCredentials sends
// every request without credentials).
type gcsFileClient struct {
	svc    *storagev1.Service
	bucket string
	limit  int64
}

func newGCSFileClient(ctx context.Context, conn *models.Connection, auth googleAuth) (*gcsFileClient, error) {
	cfg, err := GCSFileConfigFromExtra(conn.Extra)
	if err != nil {
		return nil, err
	}
	auth.settings = conn.Extra
	httpClient := netguard.Outbound().Client(0)
	creds, err := googleCredentials("GCS", auth, httpClient, gcsScope)
	if err != nil {
		return nil, err
	}
	authClient, err := googlehttp.NewClient(&googlehttp.Options{
		BaseRoundTripper: httpClient.Transport,
		Credentials:      creds,
	})
	if err != nil {
		return nil, fmt.Errorf("GCS authenticated client: %w", err)
	}
	opts := []option.ClientOption{option.WithHTTPClient(authClient)}
	if gcsAPIEndpoint != "" {
		opts = append(opts, option.WithEndpoint(gcsAPIEndpoint))
	}
	svc, err := storagev1.NewService(ctx, opts...)
	if err != nil {
		return nil, fmt.Errorf("create GCS client: %w", err)
	}
	return &gcsFileClient{svc: svc, bucket: cfg.Bucket, limit: cfg.MaxDownloadBytes}, nil
}

// TestGCSConnection authenticates and lists one object in the configured
// bucket, through the same client file nodes use. Listing needs
// storage.objects.list, which the roles file nodes use grant; reading the
// bucket's own metadata (storage.buckets.get) is not in them.
func TestGCSConnection(ctx context.Context, extra string, tokens identity.TokenSource, req identity.TokenRequest) error {
	client, err := newGCSFileClient(ctx, &models.Connection{Extra: extra}, googleAuth{tokens: tokens, request: req})
	if err != nil {
		return err
	}
	if _, err := client.svc.Objects.List(client.bucket).MaxResults(1).Context(ctx).Do(); err != nil {
		return fmt.Errorf("list GCS bucket %q: %w", client.bucket, explainGCSError(err))
	}
	return nil
}

func (c *gcsFileClient) download(ctx context.Context, key, destination string) (int64, error) {
	if key == "" {
		return 0, fmt.Errorf("GCS object name is required")
	}
	meta, err := c.svc.Objects.Get(c.bucket, key).Context(ctx).Do()
	if err != nil {
		return 0, fmt.Errorf("open GCS object %q: %w", key, explainGCSError(err))
	}
	if meta.Size > uint64(c.limit) { // #nosec G115 -- limit is a validated positive int64, so the conversion is exact.
		return 0, fmt.Errorf("GCS object %q is %d bytes, over the %d-byte download limit", key, meta.Size, c.limit)
	}
	// The generation that was measured is the one downloaded, so a
	// replacement written in between cannot slip past the limit check.
	resp, err := c.svc.Objects.Get(c.bucket, key).Generation(meta.Generation).Context(ctx).Download()
	if err != nil {
		return 0, fmt.Errorf("download GCS object %q: %w", key, explainGCSError(err))
	}
	defer resp.Body.Close() //nolint:errcheck
	if err := os.MkdirAll(filepath.Dir(destination), 0o700); err != nil {
		return 0, fmt.Errorf("create GCS download directory: %w", err)
	}
	f, err := os.Create(destination) // #nosec G304 -- destination is a fixed filename inside a process-created scratch directory.
	if err != nil {
		return 0, fmt.Errorf("create GCS download: %w", err)
	}
	defer f.Close() //nolint:errcheck
	counted := &countingWriter{w: f}
	if _, err := io.Copy(counted, io.LimitReader(resp.Body, c.limit+1)); err != nil {
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

// upload streams the node's output into one object. Cloud Storage creates
// or replaces the object only when the upload completes: a single request
// whose body is cut short, or a resumable session whose final chunk never
// arrives, leaves no object and an existing one untouched. So a write that
// fails part way leaves nothing behind -- the fileTransport contract.
func (c *gcsFileClient) upload(ctx context.Context, key string, write func(io.Writer) error) (int64, error) {
	if key == "" {
		return 0, fmt.Errorf("GCS object name is required")
	}
	reader, writer := io.Pipe()
	result := make(chan error, 1)
	go func() {
		_, err := c.svc.Objects.Insert(c.bucket, &storagev1.Object{Name: key}).
			Media(reader, googleapi.ChunkSize(gcsUploadChunk)).Context(ctx).Do()
		// Unblock the writer if the upload stopped reading early.
		_ = reader.CloseWithError(err)
		result <- err
	}()

	counted := &countingWriter{w: writer}
	writeErr := write(counted)
	if writeErr != nil {
		_ = writer.CloseWithError(writeErr)
	} else {
		_ = writer.Close()
	}
	uploadErr := <-result
	// The node's own failure is the cause when the upload stopped on it;
	// when the service failed first, the write then fails on the closed
	// pipe, and the service's reason is the one worth reporting.
	if writeErr != nil && (uploadErr == nil || errors.Is(uploadErr, writeErr)) {
		return 0, writeErr
	}
	if uploadErr != nil {
		return 0, fmt.Errorf("upload GCS object %q: %w", key, explainGCSError(uploadErr))
	}
	return counted.n, nil
}

// gcsUploadChunk is the resumable-upload chunk size: the most one upload
// holds in memory, and the size below which the object goes up in a single
// request.
var gcsUploadChunk = 16 << 20

// explainGCSError puts the reason first, ahead of the API's request dump.
func explainGCSError(err error) error {
	var apiErr *googleapi.Error
	if !errors.As(err, &apiErr) {
		return err
	}
	switch apiErr.Code {
	case http.StatusNotFound:
		return fmt.Errorf("not found (404): the bucket or object does not exist: %w", err)
	case http.StatusUnauthorized:
		return fmt.Errorf("the credentials were refused (401): %w", err)
	case http.StatusForbidden:
		return fmt.Errorf("the credentials do not grant this operation (403): %w", err)
	}
	return err
}
