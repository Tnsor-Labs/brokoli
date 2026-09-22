package engine

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Tnsor-Labs/brokoli/models"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

func TestS3FileRoundTripMinIO(t *testing.T) {
	endpoint := os.Getenv("BROKOLI_TEST_S3_ENDPOINT")
	if endpoint == "" {
		t.Skip("set BROKOLI_TEST_S3_ENDPOINT to run the MinIO integration test")
	}

	accessKey := getenvDefault("BROKOLI_TEST_S3_ACCESS_KEY", "brokoli-test")
	secretKey := getenvDefault("BROKOLI_TEST_S3_SECRET_KEY", "brokoli-test-secret")
	bucket := "brokoli-test-" + shortHash(t.Name())
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	conn := &models.Connection{Extra: fmt.Sprintf(`{"bucket":%q,"region":"us-east-1","access_key":%q,"secret_key":%q,"endpoint":%q,"use_path_style":true}`, bucket, accessKey, secretKey, endpoint)}
	client, err := newS3FileClient(ctx, conn)
	if err != nil {
		t.Fatalf("create S3 client: %v", err)
	}
	if _, err := client.client.CreateBucket(ctx, &s3.CreateBucketInput{Bucket: aws.String(bucket)}); err != nil {
		t.Fatalf("create bucket: %v", err)
	}

	key := "integration/multipart.bin"
	content := bytes.Repeat([]byte("brokoli-minio-round-trip\n"), 400000)
	written, err := client.upload(ctx, key, func(w io.Writer) error {
		_, err := w.Write(content)
		return err
	})
	if err != nil {
		t.Fatalf("upload: %v", err)
	}
	if written != int64(len(content)) {
		t.Fatalf("uploaded bytes = %d, want %d", written, len(content))
	}

	destination := filepath.Join(t.TempDir(), "download.bin")
	read, err := client.download(ctx, key, destination)
	if err != nil {
		t.Fatalf("download: %v", err)
	}
	if read != int64(len(content)) {
		t.Fatalf("downloaded bytes = %d, want %d", read, len(content))
	}
	got, err := os.ReadFile(destination)
	if err != nil {
		t.Fatalf("read downloaded file: %v", err)
	}
	if !bytes.Equal(got, content) {
		t.Fatalf("downloaded content hash = %s, want %s", hashBytes(got), hashBytes(content))
	}
}

func getenvDefault(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}

func shortHash(value string) string {
	hash := sha256.Sum256([]byte(value))
	return hex.EncodeToString(hash[:])[:12]
}

func hashBytes(value []byte) string {
	hash := sha256.Sum256(value)
	return hex.EncodeToString(hash[:])
}
