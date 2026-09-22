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
