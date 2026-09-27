package engine

import (
	"bytes"
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

	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob/bloberror"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob/sas"
	"github.com/Tnsor-Labs/brokoli/models"
	"github.com/Tnsor-Labs/brokoli/pkg/netguard"
	"github.com/Tnsor-Labs/brokoli/store"
)

/*
 * The azure_blob transport against Azurite, Microsoft's own emulator.
 *
 *   docker compose -f docker-compose.test.yml up -d --wait azurite
 *   BROKOLI_TEST_AZURE_BLOB_ENDPOINT=http://127.0.0.1:55537/devstoreaccount1 \
 *     go test ./engine -run 'TestAzureBlob'
 *
 * Each test makes its own container with a unique name and deletes it,
 * so an interrupted run leaves nothing that makes the next one fail.
 */

// The emulator's published development account. Not a secret: it is the
// same for every Azurite install and documented by Microsoft.
const (
	azuriteAccount = "devstoreaccount1"
	azuriteKey     = "Eby8vdM02xNOcqFlqUwJPLlmEtlCDXJ1OUzFT50uSRZ6IFsuFq2UVErCz4I6tq/K1SZFPTOtr/KBHBeksoGMGw=="
)

type azuriteFixture struct {
	endpoint  string
	container string
	admin     *azblob.Client
}

// newAzuriteFixture skips without an emulator, and otherwise creates a
// fresh container and a config that points at it.
func newAzuriteFixture(t *testing.T) *azuriteFixture {
	t.Helper()
	endpoint := os.Getenv("BROKOLI_TEST_AZURE_BLOB_ENDPOINT")
	if endpoint == "" {
		t.Skip("set BROKOLI_TEST_AZURE_BLOB_ENDPOINT to run the Azurite integration tests")
	}
	allowLoopback(t)

	cred, err := azblob.NewSharedKeyCredential(azuriteAccount, azuriteKey)
	if err != nil {
		t.Fatal(err)
	}
	admin, err := azblob.NewClientWithSharedKeyCredential(strings.TrimRight(endpoint, "/")+"/", cred, nil)
	if err != nil {
		t.Fatal(err)
	}
	name := fmt.Sprintf("t-%s-%d", shortHash(t.Name()), time.Now().UnixNano()%1_000_000_000)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err := admin.CreateContainer(ctx, name, nil); err != nil {
		t.Fatalf("create container: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		_, _ = admin.DeleteContainer(ctx, name, nil)
	})
	return &azuriteFixture{endpoint: endpoint, container: name, admin: admin}
}

func (f *azuriteFixture) config(extra map[string]interface{}) map[string]interface{} {
	cfg := map[string]interface{}{
		"account": azuriteAccount, "container": f.container, "key": azuriteKey, "endpoint": f.endpoint,
	}
	for k, v := range extra {
		cfg[k] = v
	}
	return cfg
}

func (f *azuriteFixture) client(t *testing.T, extra map[string]interface{}) *azureBlobFileClient {
	t.Helper()
	cfg := f.config(extra)
	for k, v := range extra {
		if v == nil {
			delete(cfg, k)
		}
	}
	encoded, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	c, err := newAzureBlobFileClient(&models.Connection{Extra: string(encoded)})
	if err != nil {
		t.Fatalf("create client: %v", err)
	}
	return c
}

// readBlob returns a blob's content through the admin client, independent
// of the code under test.
func (f *azuriteFixture) readBlob(t *testing.T, name string) (string, bool) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	resp, err := f.admin.DownloadStream(ctx, f.container, name, nil)
	if bloberror.HasCode(err, bloberror.BlobNotFound) {
		return "", false
	}
	if err != nil {
		t.Fatalf("read blob %q: %v", name, err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return string(data), true
}

func writeString(s string) func(io.Writer) error {
	return func(w io.Writer) error {
		_, err := io.WriteString(w, s)
		return err
	}
}

func TestAzureBlobRoundTrip(t *testing.T) {
	f := newAzuriteFixture(t)
	c := f.client(t, nil)
	ctx := context.Background()

	const content = "id,total\n1,10\n2,25\n"
	n, err := c.upload(ctx, "exports/orders.csv", writeString(content))
	if err != nil {
		t.Fatalf("upload: %v", err)
	}
	if n != int64(len(content)) {
		t.Errorf("upload counted %d bytes, want %d", n, len(content))
	}
	local := filepath.Join(t.TempDir(), "orders.csv")
	if _, err := c.download(ctx, "exports/orders.csv", local); err != nil {
		t.Fatalf("download: %v", err)
	}
	got, _ := os.ReadFile(local)
	if string(got) != content {
		t.Errorf("round trip = %q, want %q", got, content)
	}
}

// The acceptance criterion that matters most: a failed write fails, and
// nothing appears where a reader would find it. Two sizes, because the
// SDK takes two paths -- a single Put Blob for a body under one block,
// staged blocks and a commit for anything larger -- and both must hold
// back until the body has been read to the end.
func TestAzureBlobFailedWriteLeavesNothingBehind(t *testing.T) {
	f := newAzuriteFixture(t)
	c := f.client(t, nil)
	ctx := context.Background()
	boom := errors.New("encoder failed half way")

	for _, size := range []struct {
		name  string
		bytes int
	}{
		{"under one block (single put)", 1024},
		{"several blocks (staged, then commit)", 12 << 20},
	} {
		t.Run(size.name, func(t *testing.T) {
			failing := func(w io.Writer) error {
				if _, err := w.Write(bytes.Repeat([]byte("x"), size.bytes)); err != nil {
					return err
				}
				return boom
			}

			// A new blob: must not exist afterwards.
			name := "new-" + shortHash(size.name) + ".csv"
			if _, err := c.upload(ctx, name, failing); !errors.Is(err, boom) {
				t.Fatalf("upload error = %v, want the write's own error", err)
			}
			if _, exists := f.readBlob(t, name); exists {
				t.Fatal("a failed write created the blob")
			}

			// An existing blob: must be untouched.
			existing := "existing-" + shortHash(size.name) + ".csv"
			if _, err := c.upload(ctx, existing, writeString("v1\n")); err != nil {
				t.Fatalf("seed: %v", err)
			}
			if _, err := c.upload(ctx, existing, failing); !errors.Is(err, boom) {
				t.Fatalf("upload error = %v, want the write's own error", err)
			}
			if got, _ := f.readBlob(t, existing); got != "v1\n" {
				t.Fatalf("a failed write replaced the existing blob: now %d bytes", len(got))
			}
		})
	}
}

func TestAzureBlobMissingBlobIsNamed(t *testing.T) {
	f := newAzuriteFixture(t)
	c := f.client(t, nil)
	_, err := c.download(context.Background(), "not-there.csv", filepath.Join(t.TempDir(), "x"))
	if err == nil || !strings.Contains(err.Error(), "not-there.csv") || !strings.Contains(err.Error(), "BlobNotFound") {
		t.Fatalf("error = %v, want it to name the blob and say it does not exist", err)
	}
}

func TestAzureBlobDownloadOverTheLimitIsRefused(t *testing.T) {
	f := newAzuriteFixture(t)
	ctx := context.Background()
	if _, err := f.client(t, nil).upload(ctx, "big.csv", writeString(strings.Repeat("x", 4096))); err != nil {
		t.Fatal(err)
	}
	limited := f.client(t, map[string]interface{}{"max_download_bytes": 1024})
	_, err := limited.download(ctx, "big.csv", filepath.Join(t.TempDir(), "x"))
	if err == nil || !strings.Contains(err.Error(), "download limit") {
		t.Fatalf("error = %v, want the download limit named", err)
	}
}

// The connection test authenticates for real. A test that only checked
// the fields would pass a wrong key, which is #680's complaint about the
// database types.
func TestAzureBlobConnectionTestIsReal(t *testing.T) {
	f := newAzuriteFixture(t)
	ctx := context.Background()

	if err := TestAzureBlobConnection(ctx, f.config(nil)); err != nil {
		t.Fatalf("correct credentials refused: %v", err)
	}

	wrongKey := f.config(map[string]interface{}{"key": "d3Jvbmcta2V5LXRoYXQtaXMtbm90LXRoZS1hY2NvdW50cy1rZXk="})
	if err := TestAzureBlobConnection(ctx, wrongKey); err == nil {
		t.Fatal("a wrong key passed the connection test")
	} else {
		t.Logf("wrong key: %v", err)
	}

	missing := f.config(map[string]interface{}{"container": "no-such-container"})
	if err := TestAzureBlobConnection(ctx, missing); err == nil || !strings.Contains(err.Error(), "ContainerNotFound") {
		t.Fatalf("error = %v, want the missing container named", err)
	}
}

// A SAS token is the other supported credential. A read-only one must
// read and must fail to write with a reason, not quietly.
func TestAzureBlobSASToken(t *testing.T) {
	f := newAzuriteFixture(t)
	ctx := context.Background()
	if _, err := f.client(t, nil).upload(ctx, "seed.csv", writeString("a\n1\n")); err != nil {
		t.Fatal(err)
	}

	cred, err := azblob.NewSharedKeyCredential(azuriteAccount, azuriteKey)
	if err != nil {
		t.Fatal(err)
	}
	sign := func(perms sas.ContainerPermissions) string {
		q, err := sas.BlobSignatureValues{
			Protocol:      sas.ProtocolHTTPSandHTTP,
			ExpiryTime:    time.Now().UTC().Add(time.Hour),
			ContainerName: f.container,
			Permissions:   perms.String(),
		}.SignWithSharedKey(cred)
		if err != nil {
			t.Fatal(err)
		}
		return q.Encode()
	}

	rw := f.client(t, map[string]interface{}{"key": nil, "sas_token": sign(sas.ContainerPermissions{Read: true, Write: true, Create: true, List: true})})
	if _, err := rw.upload(ctx, "via-sas.csv", writeString("b\n2\n")); err != nil {
		t.Fatalf("upload with a read-write SAS: %v", err)
	}
	if got, _ := f.readBlob(t, "via-sas.csv"); got != "b\n2\n" {
		t.Fatalf("SAS upload wrote %q", got)
	}

	ro := f.client(t, map[string]interface{}{"key": nil, "sas_token": sign(sas.ContainerPermissions{Read: true, List: true})})
	if _, err := ro.download(ctx, "seed.csv", filepath.Join(t.TempDir(), "s")); err != nil {
		t.Fatalf("download with a read-only SAS: %v", err)
	}
	_, err = ro.upload(ctx, "denied.csv", writeString("c\n"))
	if err == nil {
		t.Fatal("a read-only SAS wrote a blob")
	}
	t.Logf("read-only SAS write: %v", err)
	if _, exists := f.readBlob(t, "denied.csv"); exists {
		t.Fatal("a refused write left a blob behind")
	}
}

// Every request goes through the outbound policy. Under the default
// policy, which refuses loopback, the emulator must be unreachable.
func TestAzureBlobGoesThroughTheNetworkPolicy(t *testing.T) {
	f := newAzuriteFixture(t)
	t.Cleanup(netguard.SetOutboundForTesting(netguard.Policy{}))
	err := TestAzureBlobConnection(context.Background(), f.config(nil))
	if err == nil || !strings.Contains(err.Error(), "blocked") {
		t.Fatalf("error = %v, want the outbound policy to refuse loopback", err)
	}
}

// The whole path a user takes: a pipeline reads a CSV from the container
// and writes it back, on both the batch and the streaming sink.
func TestAzureBlobFileNodesRoundTripThroughRunner(t *testing.T) {
	for _, streamed := range []bool{false, true} {
		name := "batch"
		if streamed {
			name = "streamed"
		}
		t.Run(name, func(t *testing.T) {
			f := newAzuriteFixture(t)
			if streamed {
				// Spill and stream everything, so the sink receives a
				// reference and takes runSinkFileStreamed.
				t.Setenv("BROKOLI_SPILL_THRESHOLD_BYTES", "1")
				t.Setenv("BROKOLI_STREAM_THRESHOLD_BYTES", "1")
			} else {
				// A CSV source_file streams whenever spilling is enabled,
				// whatever its size, so the batch sink is reached only
				// with spilling off. Without this both subtests exercised
				// the streamed path -- found by the assertion below.
				t.Setenv("BROKOLI_SPILL_THRESHOLD_BYTES", "-1")
			}
			extra, err := json.Marshal(f.config(nil))
			if err != nil {
				t.Fatal(err)
			}
			root := t.TempDir()
			st, err := store.NewSQLiteStore(filepath.Join(root, "meta.db"))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = st.Close() })
			if err := st.CreateConnection(&models.Connection{
				ID: "c-az", ConnID: "runner-az", Type: models.ConnTypeAzureBlob,
				Extra: string(extra), CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
			}); err != nil {
				t.Fatal(err)
			}
			eng := drainEngineOnCleanup(t, NewEngine(st))
			eng.ConnResolver = NewConnectionResolver(st, nil)
			t.Setenv("BROKOLI_DATA_DIRS", root)

			const content = "id,total\n1,10\n2,25\n3,7\n"
			if _, err := f.client(t, nil).upload(context.Background(), "incoming/orders.csv", writeString(content)); err != nil {
				t.Fatal(err)
			}
			pipeline := chain("az-"+name,
				fileNode("source", models.NodeTypeSourceFile, map[string]interface{}{
					"path": "incoming/orders.csv", "format": "csv", "conn_id": "runner-az",
				}),
				fileNode("sink", models.NodeTypeSinkFile, map[string]interface{}{
					"path": "processed/orders.csv", "format": "csv", "conn_id": "runner-az",
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
			if got, _ := f.readBlob(t, "processed/orders.csv"); got != content {
				t.Fatalf("sink wrote %q, want %q", got, content)
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

// The acceptance criterion through a real run: a sink that cannot write
// fails the run, the run says why, and the output lands nowhere else --
// not in the container, and not on the worker's disk at the same path,
// which is the local fallback ADR-040 forbids.
func TestAzureBlobRefusedSinkFailsTheRunAndWritesNowhereElse(t *testing.T) {
	f := newAzuriteFixture(t)
	cred, err := azblob.NewSharedKeyCredential(azuriteAccount, azuriteKey)
	if err != nil {
		t.Fatal(err)
	}
	q, err := sas.BlobSignatureValues{
		Protocol:      sas.ProtocolHTTPSandHTTP,
		ExpiryTime:    time.Now().UTC().Add(time.Hour),
		ContainerName: f.container,
		Permissions:   (&sas.ContainerPermissions{Read: true, List: true}).String(),
	}.SignWithSharedKey(cred)
	if err != nil {
		t.Fatal(err)
	}
	cfg := f.config(map[string]interface{}{"sas_token": q.Encode()})
	delete(cfg, "key")
	extra, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}

	root := t.TempDir()
	st, err := store.NewSQLiteStore(filepath.Join(root, "meta.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if err := st.CreateConnection(&models.Connection{
		ID: "c-az-ro", ConnID: "az-readonly", Type: models.ConnTypeAzureBlob,
		Extra: string(extra), CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	eng := drainEngineOnCleanup(t, NewEngine(st))
	eng.ConnResolver = NewConnectionResolver(st, nil)
	t.Setenv("BROKOLI_DATA_DIRS", root)
	t.Chdir(root)

	src := filepath.Join(root, "in.csv")
	if err := os.WriteFile(src, []byte("id\n1\n2\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	pipeline := chain("az-refused",
		fileNode("source", models.NodeTypeSourceFile, map[string]interface{}{"path": src, "format": "csv"}),
		fileNode("sink", models.NodeTypeSinkFile, map[string]interface{}{
			"path": "out/refused.csv", "format": "csv", "conn_id": "az-readonly",
		}),
	)
	if err := st.CreatePipeline(pipeline); err != nil {
		t.Fatal(err)
	}
	run, _ := eng.RunPipeline(pipeline.ID)
	if run == nil {
		t.Fatal("no run returned")
	}
	if run.Status != models.RunStatusFailed {
		t.Fatalf("status = %s; a sink the credentials cannot write must fail the run", run.Status)
	}
	stored, err := st.GetRun(run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stored.Error, "do not grant this operation") {
		t.Errorf("run error = %q, want the refusal explained", stored.Error)
	}
	if _, exists := f.readBlob(t, "out/refused.csv"); exists {
		t.Error("the refused write left a blob")
	}
	for _, local := range []string{filepath.Join(root, "out/refused.csv"), "out/refused.csv"} {
		if _, err := os.Stat(local); err == nil {
			t.Errorf("the output fell back to the local disk at %s", local)
		}
	}
}
