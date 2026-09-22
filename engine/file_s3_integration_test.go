package engine

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/Tnsor-Labs/brokoli/models"
	"github.com/Tnsor-Labs/brokoli/store"
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
	sessionToken := os.Getenv("BROKOLI_TEST_S3_SESSION_TOKEN")
	bucket := "brokoli-test-" + shortHash(t.Name())
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	conn := &models.Connection{Extra: fmt.Sprintf(`{"bucket":%q,"region":"us-east-1","access_key":%q,"secret_key":%q,"session_token":%q,"endpoint":%q,"use_path_style":true}`, bucket, accessKey, secretKey, sessionToken, endpoint)}
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

// TestS3FileProviderCompatibility runs the same real S3 API smoke test against
// any provider configured by the environment. It intentionally does not create
// or delete a bucket, so it is safe for shared AWS and hosted-provider buckets.
// Set BROKOLI_TEST_S3_PROVIDER to a label such as aws, r2, wasabi, b2, ceph, or
// minio to enable it, and provide a pre-created test bucket.
func TestS3FileProviderCompatibility(t *testing.T) {
	provider := os.Getenv("BROKOLI_TEST_S3_PROVIDER")
	if provider == "" {
		t.Skip("set BROKOLI_TEST_S3_PROVIDER to run provider compatibility checks")
	}
	endpoint := os.Getenv("BROKOLI_TEST_S3_ENDPOINT")
	if endpoint == "" && provider != "aws" {
		t.Fatal("BROKOLI_TEST_S3_ENDPOINT is required when provider compatibility checks are enabled")
	}
	bucket := os.Getenv("BROKOLI_TEST_S3_BUCKET")
	if bucket == "" {
		t.Fatal("BROKOLI_TEST_S3_BUCKET is required when provider compatibility checks are enabled")
	}
	pathStyle, err := strconv.ParseBool(getenvDefault("BROKOLI_TEST_S3_PATH_STYLE", "false"))
	if err != nil {
		t.Fatalf("BROKOLI_TEST_S3_PATH_STYLE: %v", err)
	}
	config := map[string]interface{}{
		"bucket":         bucket,
		"region":         getenvDefault("BROKOLI_TEST_S3_REGION", "us-east-1"),
		"access_key":     getenvDefault("BROKOLI_TEST_S3_ACCESS_KEY", ""),
		"secret_key":     getenvDefault("BROKOLI_TEST_S3_SECRET_KEY", ""),
		"endpoint":       endpoint,
		"use_path_style": pathStyle,
	}
	if token := os.Getenv("BROKOLI_TEST_S3_SESSION_TOKEN"); token != "" {
		config["session_token"] = token
	}
	extra, err := json.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	client, err := newS3FileClient(ctx, &models.Connection{Extra: string(extra)})
	if err != nil {
		t.Fatalf("%s: create S3 client: %v", provider, err)
	}
	if err := client.checkBucket(ctx); err != nil {
		t.Fatalf("%s: check bucket: %v", provider, err)
	}

	key := "integration/provider-compatibility-" + shortHash(t.Name()) + ".txt"
	want := []byte("provider compatibility check\n")
	if _, err := client.upload(ctx, key, func(w io.Writer) error {
		_, err := w.Write(want)
		return err
	}); err != nil {
		t.Fatalf("%s: upload: %v", provider, err)
	}
	destination := filepath.Join(t.TempDir(), "provider.txt")
	if _, err := client.download(ctx, key, destination); err != nil {
		t.Fatalf("%s: download: %v", provider, err)
	}
	got, err := os.ReadFile(destination)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("%s: downloaded content = %q, want %q", provider, got, want)
	}
}

func TestS3FileNodesRoundTripThroughRunner(t *testing.T) {
	endpoint := os.Getenv("BROKOLI_TEST_S3_ENDPOINT")
	if endpoint == "" {
		t.Skip("set BROKOLI_TEST_S3_ENDPOINT to run the MinIO integration test")
	}
	allowLoopback(t)

	accessKey := getenvDefault("BROKOLI_TEST_S3_ACCESS_KEY", "brokoli-test")
	secretKey := getenvDefault("BROKOLI_TEST_S3_SECRET_KEY", "brokoli-test-secret")
	bucket := "brokoli-runner-" + shortHash(t.Name())
	config := map[string]interface{}{
		"bucket": bucket, "region": "us-east-1", "access_key": accessKey,
		"secret_key": secretKey, "endpoint": endpoint, "use_path_style": true,
	}
	extra, err := json.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}

	root := t.TempDir()
	st, err := store.NewSQLiteStore(filepath.Join(root, "meta.db"))
	if err != nil {
		t.Fatalf("create store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if err := st.CreateConnection(&models.Connection{
		ID: "c-s3-runner", ConnID: "runner-s3", Type: models.ConnTypeS3,
		Extra: string(extra), CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("create connection: %v", err)
	}
	eng := drainEngineOnCleanup(t, NewEngine(st))
	eng.ConnResolver = NewConnectionResolver(st, nil)
	t.Setenv("BROKOLI_DATA_DIRS", root)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	client, err := newS3FileClient(ctx, &models.Connection{Extra: string(extra)})
	if err != nil {
		t.Fatalf("create S3 client: %v", err)
	}
	if _, err := client.client.CreateBucket(ctx, &s3.CreateBucketInput{Bucket: aws.String(bucket)}); err != nil {
		t.Fatalf("create bucket: %v", err)
	}
	const content = "id,total\n1,10\n2,25\n3,7\n"
	if _, err := client.upload(ctx, "incoming/orders.csv", func(w io.Writer) error {
		_, err := io.WriteString(w, content)
		return err
	}); err != nil {
		t.Fatalf("seed source object: %v", err)
	}

	pipeline := chain("s3-runner-round-trip",
		fileNode("source", models.NodeTypeSourceFile, map[string]interface{}{
			"path": "incoming/orders.csv", "format": "csv", "conn_id": "runner-s3",
		}),
		fileNode("sink", models.NodeTypeSinkFile, map[string]interface{}{
			"path": "processed/orders.csv", "format": "csv", "conn_id": "runner-s3",
		}),
	)
	if err := st.CreatePipeline(pipeline); err != nil {
		t.Fatalf("create pipeline: %v", err)
	}
	run, err := eng.RunPipeline(pipeline.ID)
	if err != nil || run == nil {
		t.Fatalf("run pipeline: %v", err)
	}
	if run.Status != models.RunStatusSuccess {
		t.Fatalf("status = %s, error = %s", run.Status, run.Error)
	}

	destination := filepath.Join(root, "output.csv")
	if _, err := client.download(ctx, "processed/orders.csv", destination); err != nil {
		t.Fatalf("read sink object: %v", err)
	}
	got, err := os.ReadFile(destination)
	if err != nil {
		t.Fatalf("read downloaded sink: %v", err)
	}
	if string(got) != content {
		t.Fatalf("sink content = %q, want %q", got, content)
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
