package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Tnsor-Labs/brokoli/models"
	"github.com/Tnsor-Labs/brokoli/pkg/identity"
	"github.com/Tnsor-Labs/brokoli/pkg/netguard"
	"github.com/Tnsor-Labs/brokoli/store"
	"google.golang.org/api/option"
	storagev1 "google.golang.org/api/storage/v1"
)

/*
 * The gcs transport against fake-gcs-server:
 *
 *   docker compose -f docker-compose.test.yml up -d gcs
 *   BROKOLI_TEST_GCS_ENDPOINT=http://127.0.0.1:55543 go test ./engine -run 'TestGCS'
 *
 * Every request goes through the real credential path: an oidc connection,
 * exchanged against the fake Google STS in bigquery_credentials_test.go, so
 * the emulator receives authenticated requests exactly as Cloud Storage
 * would. Each test gets its own bucket, deleted afterwards.
 */

type gcsFixture struct {
	admin  *storagev1.Service
	bucket string
	tokens *recordingTokenSource
}

const gcsFixtureProvider = "//iam.googleapis.com/projects/123456789/locations/global/workloadIdentityPools/brokoli/providers/runs"

func newGCSFixture(t *testing.T) *gcsFixture {
	t.Helper()
	endpoint := os.Getenv("BROKOLI_TEST_GCS_ENDPOINT")
	if endpoint == "" {
		t.Skip("set BROKOLI_TEST_GCS_ENDPOINT to run the fake-gcs-server integration tests")
	}
	api := strings.TrimRight(endpoint, "/") + "/storage/v1/"
	prev := gcsAPIEndpoint
	gcsAPIEndpoint = api
	t.Cleanup(func() { gcsAPIEndpoint = prev })
	t.Cleanup(netguard.SetOutboundForTesting(netguard.Policy{AllowLoopback: true}))
	fakeGoogleFederation(t)

	admin, err := storagev1.NewService(context.Background(), option.WithEndpoint(api), option.WithoutAuthentication())
	if err != nil {
		t.Fatal(err)
	}
	bucket := fmt.Sprintf("brokoli-test-%d", time.Now().UnixNano()%1_000_000_000_000)
	if _, err := admin.Buckets.Insert("test-project", &storagev1.Bucket{Name: bucket}).Do(); err != nil {
		t.Fatalf("create bucket: %v", err)
	}
	t.Cleanup(func() {
		if list, err := admin.Objects.List(bucket).Do(); err == nil {
			for _, o := range list.Items {
				_ = admin.Objects.Delete(bucket, o.Name).Do()
			}
		}
		_ = admin.Buckets.Delete(bucket).Do()
	})
	return &gcsFixture{admin: admin, bucket: bucket, tokens: &recordingTokenSource{}}
}

func (f *gcsFixture) extra(overrides map[string]interface{}) string {
	cfg := map[string]interface{}{"bucket": f.bucket, "auth_method": "oidc", "provider": gcsFixtureProvider}
	for k, v := range overrides {
		cfg[k] = v
	}
	out, _ := json.Marshal(cfg)
	return string(out)
}

func (f *gcsFixture) client(t *testing.T, overrides map[string]interface{}) *gcsFileClient {
	t.Helper()
	c, err := newGCSFileClient(context.Background(), &models.Connection{ID: "c-gcs", Extra: f.extra(overrides)},
		googleAuth{tokens: f.tokens, request: identity.TokenRequest{SubjectKind: "connection", SubjectID: "c-gcs"}})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// readObject returns an object's content, and whether it exists.
func (f *gcsFixture) readObject(t *testing.T, name string) (string, bool) {
	t.Helper()
	resp, err := f.admin.Objects.Get(f.bucket, name).Download()
	if err != nil {
		if strings.Contains(err.Error(), "404") {
			return "", false
		}
		t.Fatalf("read %s: %v", name, err)
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return string(b), true
}

func TestGCSRoundTrip(t *testing.T) {
	f := newGCSFixture(t)
	c := f.client(t, nil)
	ctx := context.Background()
	const content = "id,total\n1,10\n2,25\n"
	n, err := c.upload(ctx, "exports/orders.csv", writeString(content))
	if err != nil {
		t.Fatal(err)
	}
	if n != int64(len(content)) {
		t.Fatalf("uploaded %d bytes, want %d", n, len(content))
	}
	local := filepath.Join(t.TempDir(), "orders.csv")
	got, err := c.download(ctx, "exports/orders.csv", local)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(local)
	if got != int64(len(content)) || string(b) != content {
		t.Fatalf("downloaded %d bytes %q, want %q", got, b, content)
	}
	if len(f.tokens.reqs) == 0 {
		t.Fatal("the connection's token source was never asked: the requests were not authenticated")
	}
}

// A write that fails part way leaves nothing behind: no new object, and an
// existing object untouched. Both upload shapes: a single request (under
// one chunk) and a resumable session (several chunks).
func TestGCSFailedWriteLeavesNothingBehind(t *testing.T) {
	for _, tc := range []struct {
		name  string
		chunk int
		body  int
	}{
		{"single request", 16 << 20, 1 << 10},
		{"resumable, several chunks", 256 << 10, 700 << 10},
	} {
		t.Run(tc.name, func(t *testing.T) {
			prev := gcsUploadChunk
			gcsUploadChunk = tc.chunk
			t.Cleanup(func() { gcsUploadChunk = prev })
			f := newGCSFixture(t)
			c := f.client(t, nil)
			ctx := context.Background()
			boom := errors.New("the node's output failed part way")
			failing := func(w io.Writer) error {
				if _, err := w.Write([]byte(strings.Repeat("x", tc.body))); err != nil {
					return err
				}
				return boom
			}

			if _, err := c.upload(ctx, "new.csv", failing); !errors.Is(err, boom) {
				t.Fatalf("err = %v, want the write's own error", err)
			}
			if _, ok := f.readObject(t, "new.csv"); ok {
				t.Fatal("a failed write created the object")
			}

			if _, err := c.upload(ctx, "existing.csv", writeString("v1\n")); err != nil {
				t.Fatal(err)
			}
			if _, err := c.upload(ctx, "existing.csv", failing); !errors.Is(err, boom) {
				t.Fatalf("overwrite err = %v, want the write's own error", err)
			}
			if got, _ := f.readObject(t, "existing.csv"); got != "v1\n" {
				t.Fatalf("a failed overwrite changed the object to %q", got)
			}
		})
	}
}

func TestGCSMissingObjectIsNamed(t *testing.T) {
	f := newGCSFixture(t)
	_, err := f.client(t, nil).download(context.Background(), "absent.csv", filepath.Join(t.TempDir(), "x"))
	if err == nil || !strings.Contains(err.Error(), `"absent.csv"`) || !strings.Contains(err.Error(), "404") {
		t.Fatalf("err = %v, want the object named and not found", err)
	}
}

func TestGCSDownloadOverTheLimitIsRefused(t *testing.T) {
	f := newGCSFixture(t)
	ctx := context.Background()
	if _, err := f.client(t, nil).upload(ctx, "big.csv", writeString(strings.Repeat("y", 64))); err != nil {
		t.Fatal(err)
	}
	local := filepath.Join(t.TempDir(), "big.csv")
	_, err := f.client(t, map[string]interface{}{"max_download_bytes": 10}).download(ctx, "big.csv", local)
	if err == nil || !strings.Contains(err.Error(), "download limit") {
		t.Fatalf("err = %v, want the limit named", err)
	}
	if _, statErr := os.Stat(local); !os.IsNotExist(statErr) {
		t.Fatal("an over-limit object was written locally")
	}
}

// The connection test lists the bucket through the same client, and a
// missing bucket fails it.
func TestGCSConnectionTestIsReal(t *testing.T) {
	f := newGCSFixture(t)
	req := identity.TokenRequest{SubjectKind: "connection", SubjectID: "c-gcs"}
	if err := TestGCSConnection(context.Background(), f.extra(nil), f.tokens, req); err != nil {
		t.Fatalf("a reachable bucket failed the test: %v", err)
	}
	err := TestGCSConnection(context.Background(), f.extra(map[string]interface{}{"bucket": "no-such-bucket-brokoli"}), f.tokens, req)
	if err == nil || !strings.Contains(err.Error(), "no-such-bucket-brokoli") {
		t.Fatalf("a missing bucket: err = %v", err)
	}
}

// Every request goes through the outbound policy: the default refuses the
// emulator on loopback.
func TestGCSGoesThroughTheNetworkPolicy(t *testing.T) {
	f := newGCSFixture(t)
	t.Cleanup(netguard.SetOutboundForTesting(netguard.Policy{}))
	_, err := f.client(t, nil).upload(context.Background(), "blocked.csv", writeString("x"))
	if err == nil || !strings.Contains(err.Error(), "blocked") {
		t.Fatalf("err = %v, want the outbound policy's refusal", err)
	}
}

func TestGCSFileNodesRoundTripThroughRunner(t *testing.T) {
	for _, streamed := range []bool{false, true} {
		name := "batch"
		if streamed {
			name = "streamed"
		}
		t.Run(name, func(t *testing.T) {
			f := newGCSFixture(t)
			if streamed {
				t.Setenv("BROKOLI_SPILL_THRESHOLD_BYTES", "1")
				t.Setenv("BROKOLI_STREAM_THRESHOLD_BYTES", "1")
			} else {
				// A CSV source_file streams whenever spilling is on, so
				// the batch sink is reached only with spilling off.
				t.Setenv("BROKOLI_SPILL_THRESHOLD_BYTES", "-1")
			}
			root := t.TempDir()
			st, err := store.NewSQLiteStore(filepath.Join(root, "meta.db"))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = st.Close() })
			if err := st.CreateConnection(&models.Connection{
				ID: "c-gcs-runner", ConnID: "runner-gcs", Type: models.ConnTypeGCS,
				Extra: f.extra(nil), CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
			}); err != nil {
				t.Fatal(err)
			}
			eng := drainEngineOnCleanup(t, NewEngine(st))
			eng.ConnResolver = NewConnectionResolver(st, nil)
			eng.ConnResolver.SetTokenSource(f.tokens)
			t.Setenv("BROKOLI_DATA_DIRS", root)

			const content = "id,total\n1,10\n2,25\n3,7\n"
			if _, err := f.client(t, nil).upload(context.Background(), "incoming/orders.csv", writeString(content)); err != nil {
				t.Fatal(err)
			}
			pipeline := chain("gcs-"+name,
				fileNode("source", models.NodeTypeSourceFile, map[string]interface{}{
					"path": "incoming/orders.csv", "format": "csv", "conn_id": "runner-gcs",
				}),
				fileNode("sink", models.NodeTypeSinkFile, map[string]interface{}{
					"path": "processed/orders.csv", "format": "csv", "conn_id": "runner-gcs",
				}),
			)
			if err := st.CreatePipeline(pipeline); err != nil {
				t.Fatal(err)
			}
			run, err := eng.RunPipeline(pipeline.ID)
			if err != nil || run == nil {
				t.Fatalf("run pipeline: %v", err)
			}
			if run.Status != models.RunStatusSuccess {
				t.Fatalf("status = %s, error = %s", run.Status, run.Error)
			}
			if got, _ := f.readObject(t, "processed/orders.csv"); got != content {
				t.Fatalf("sink wrote %q, want %q", got, content)
			}
			// The runner's token request names the connection by its
			// immutable ID, not its slug.
			sawID := false
			for _, r := range f.tokens.reqs {
				if r.SubjectID == "c-gcs-runner" {
					sawID = true
				}
			}
			if !sawID {
				t.Fatalf("no token was requested for connection c-gcs-runner: %+v", f.tokens.reqs)
			}
			logs, err := st.GetLogs(run.ID)
			if err != nil {
				t.Fatal(err)
			}
			tookStreamed := false
			for _, l := range logs {
				if l.NodeID == "sink" && strings.Contains(l.Message, "never materialized") {
					tookStreamed = true
				}
			}
			if tookStreamed != streamed {
				t.Fatalf("streamed sink path taken = %v, want %v", tookStreamed, streamed)
			}
		})
	}
}
