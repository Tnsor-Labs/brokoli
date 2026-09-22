package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/Tnsor-Labs/brokoli/models"
	"github.com/Tnsor-Labs/brokoli/pkg/netguard"
	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/feature/s3/manager"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

const defaultS3DownloadLimit int64 = 10 << 30

// S3FileConfig is the customer-owned S3 configuration stored in a connection's
// extra JSON. It is deliberately independent from enterprise Strata storage.
type S3FileConfig struct {
	Bucket           string
	Region           string
	AccessKeyID      string
	SecretAccessKey  string
	Endpoint         string
	UsePathStyle     bool
	MaxDownloadBytes int64
}

// S3FileConfigFromExtra parses the same fields used by the S3 connection form.
// Credentials may be omitted when the worker's AWS environment provides them.
func S3FileConfigFromExtra(extra string) (S3FileConfig, error) {
	var raw map[string]interface{}
	if strings.TrimSpace(extra) == "" {
		return S3FileConfig{}, fmt.Errorf("S3 extra config is required")
	}
	if err := json.Unmarshal([]byte(extra), &raw); err != nil {
		return S3FileConfig{}, fmt.Errorf("S3 extra config must be a JSON object: %w", err)
	}
	str := func(key string) string {
		value, _ := raw[key].(string)
		return strings.TrimSpace(value)
	}
	cfg := S3FileConfig{
		Bucket:           str("bucket"),
		Region:           str("region"),
		AccessKeyID:      str("access_key"),
		SecretAccessKey:  str("secret_key"),
		Endpoint:         str("endpoint"),
		MaxDownloadBytes: defaultS3DownloadLimit,
	}
	if cfg.Bucket == "" {
		return S3FileConfig{}, fmt.Errorf("S3 extra config requires bucket")
	}
	if !validS3BucketName(cfg.Bucket) {
		return S3FileConfig{}, fmt.Errorf("S3 bucket target blocked: invalid bucket name")
	}
	if cfg.Region == "" {
		return S3FileConfig{}, fmt.Errorf("S3 extra config requires region")
	}
	if (cfg.AccessKeyID == "") != (cfg.SecretAccessKey == "") {
		return S3FileConfig{}, fmt.Errorf("S3 access_key and secret_key must be provided together")
	}
	if value, ok := raw["use_path_style"]; ok {
		switch v := value.(type) {
		case bool:
			cfg.UsePathStyle = v
		case string:
			cfg.UsePathStyle = strings.EqualFold(strings.TrimSpace(v), "true")
		default:
			return S3FileConfig{}, fmt.Errorf("S3 use_path_style must be a boolean")
		}
	}
	if value, ok := raw["max_download_bytes"]; ok {
		switch v := value.(type) {
		case float64:
			if v < 1 || v != float64(int64(v)) {
				return S3FileConfig{}, fmt.Errorf("S3 max_download_bytes must be a positive whole number")
			}
			cfg.MaxDownloadBytes = int64(v)
		case string:
			n, err := strconv.ParseInt(strings.TrimSpace(v), 10, 64)
			if err != nil || n < 1 {
				return S3FileConfig{}, fmt.Errorf("S3 max_download_bytes must be a positive whole number")
			}
			cfg.MaxDownloadBytes = n
		default:
			return S3FileConfig{}, fmt.Errorf("S3 max_download_bytes must be a positive whole number")
		}
	}
	return cfg, nil
}

func validS3BucketName(bucket string) bool {
	if len(bucket) < 3 || len(bucket) > 63 || bucket[0] == '.' || bucket[0] == '-' || bucket[len(bucket)-1] == '.' || bucket[len(bucket)-1] == '-' {
		return false
	}
	for i, r := range bucket {
		if (r < 'a' || r > 'z') && (r < '0' || r > '9') && r != '.' && r != '-' {
			return false
		}
		if i > 0 && r == '.' && bucket[i-1] == '.' {
			return false
		}
	}
	return true
}

// TestS3Connection authenticates against the configured bucket using the same
// AWS SDK path that file nodes use at runtime.
func TestS3Connection(ctx context.Context, extra map[string]interface{}) error {
	encoded, err := json.Marshal(extra)
	if err != nil {
		return fmt.Errorf("encode S3 config: %w", err)
	}
	client, err := newS3FileClient(ctx, &models.Connection{Extra: string(encoded)})
	if err != nil {
		return err
	}
	return client.checkBucket(ctx)
}

type s3FileClient struct {
	client *s3.Client
	bucket string
	limit  int64
}

func newS3FileClient(ctx context.Context, conn *models.Connection) (*s3FileClient, error) {
	cfg, err := S3FileConfigFromExtra(conn.Extra)
	if err != nil {
		return nil, err
	}
	loadOpts := []func(*awsconfig.LoadOptions) error{
		awsconfig.WithRegion(cfg.Region),
		awsconfig.WithHTTPClient(netguard.Outbound().Client(0)),
	}
	if cfg.AccessKeyID != "" {
		loadOpts = append(loadOpts, awsconfig.WithCredentialsProvider(
			credentials.NewStaticCredentialsProvider(cfg.AccessKeyID, cfg.SecretAccessKey, ""),
		))
	}
	awsCfg, err := awsconfig.LoadDefaultConfig(ctx, loadOpts...)
	if err != nil {
		return nil, fmt.Errorf("load S3 config: %w", err)
	}
	client := s3.NewFromConfig(awsCfg, func(options *s3.Options) {
		if cfg.Endpoint != "" {
			options.BaseEndpoint = aws.String(cfg.Endpoint)
		}
		options.UsePathStyle = cfg.UsePathStyle
	})
	return &s3FileClient{client: client, bucket: cfg.Bucket, limit: cfg.MaxDownloadBytes}, nil
}

func (c *s3FileClient) checkBucket(ctx context.Context) error {
	_, err := c.client.HeadBucket(ctx, &s3.HeadBucketInput{Bucket: aws.String(c.bucket)})
	if err != nil {
		return fmt.Errorf("check S3 bucket %q: %w", c.bucket, err)
	}
	return nil
}

func (c *s3FileClient) download(ctx context.Context, key, destination string) (int64, error) {
	if key == "" {
		return 0, fmt.Errorf("S3 object key is required")
	}
	head, err := c.client.HeadObject(ctx, &s3.HeadObjectInput{Bucket: aws.String(c.bucket), Key: aws.String(key)})
	if err != nil {
		return 0, fmt.Errorf("head S3 object %q: %w", key, err)
	}
	if head.ContentLength != nil && *head.ContentLength > c.limit {
		return 0, fmt.Errorf("S3 object %q is %d bytes, over the %d-byte download limit", key, *head.ContentLength, c.limit)
	}
	object, err := c.client.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(c.bucket), Key: aws.String(key)})
	if err != nil {
		return 0, fmt.Errorf("get S3 object %q: %w", key, err)
	}
	defer object.Body.Close() //nolint:errcheck

	if err := os.MkdirAll(filepath.Dir(destination), 0o755); err != nil {
		return 0, fmt.Errorf("create S3 download directory: %w", err)
	}
	f, err := os.Create(destination)
	if err != nil {
		return 0, fmt.Errorf("create S3 download: %w", err)
	}
	defer f.Close() //nolint:errcheck
	counted := &countingWriter{w: f}
	if _, err := io.Copy(counted, io.LimitReader(object.Body, c.limit+1)); err != nil {
		return 0, fmt.Errorf("download S3 object %q: %w", key, err)
	}
	if counted.n > c.limit {
		return 0, fmt.Errorf("S3 object %q exceeds the %d-byte download limit", key, c.limit)
	}
	if err := f.Close(); err != nil {
		return 0, fmt.Errorf("finish S3 download: %w", err)
	}
	return counted.n, nil
}

func (c *s3FileClient) upload(ctx context.Context, key string, write func(io.Writer) error) (int64, error) {
	if key == "" {
		return 0, fmt.Errorf("S3 object key is required")
	}
	reader, writer := io.Pipe()
	uploader := manager.NewUploader(c.client)
	result := make(chan error, 1)
	go func() {
		_, err := uploader.Upload(ctx, &s3.PutObjectInput{
			Bucket: aws.String(c.bucket), Key: aws.String(key), Body: reader,
		})
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
	if writeErr != nil {
		return 0, writeErr
	}
	if uploadErr != nil {
		return 0, fmt.Errorf("upload S3 object %q: %w", key, uploadErr)
	}
	return counted.n, nil
}

func s3FileExtension(key string) string {
	if i := strings.LastIndexByte(key, '.'); i >= 0 {
		return key[i:]
	}
	return ""
}
