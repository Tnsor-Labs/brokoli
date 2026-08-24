package artifact

import (
	"context"
	"errors"
	"io"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/johannesboyne/gofakes3"
	"github.com/johannesboyne/gofakes3/backend/s3mem"
)

// newTestS3Store spins up an in-memory, real-HTTP fake S3 server (not a
// mocked client) so these tests exercise the actual AWS SDK request/
// response path — multipart upload, CopyObject, ListObjectsV2/
// DeleteObjects pagination shape — the same way the client behaves against
// real S3 or a real S3-compatible provider, just without a network
// dependency in CI.
func newTestS3Store(t *testing.T) *S3Store {
	t.Helper()
	backend := s3mem.New()
	faker := gofakes3.New(backend)
	ts := httptest.NewServer(faker.Server())
	t.Cleanup(ts.Close)

	const bucket = "test-bucket"
	if err := backend.CreateBucket(bucket); err != nil {
		t.Fatalf("create test bucket: %v", err)
	}

	st, err := NewS3Store(context.Background(), S3StoreConfig{
		Bucket:          bucket,
		Region:          "us-east-1",
		Endpoint:        ts.URL,
		AccessKeyID:     "test",
		SecretAccessKey: "test",
		UsePathStyle:    true,
	})
	if err != nil {
		t.Fatalf("NewS3Store: %v", err)
	}
	return st
}

func TestS3Store_PutOpenRoundTrip(t *testing.T) {
	st := newTestS3Store(t)
	content := strings.Repeat("payload-", 5000) // ~40KB, past a single buffer

	ref, err := st.Put(context.Background(), "run-1", strings.NewReader(content), PutOptions{MediaType: "text/plain"})
	if err != nil {
		t.Fatal(err)
	}
	if ref.SizeBytes != int64(len(content)) {
		t.Errorf("size = %d, want %d", ref.SizeBytes, len(content))
	}
	if ref.MediaType != "text/plain" {
		t.Errorf("media type = %q", ref.MediaType)
	}
	if !strings.HasPrefix(ref.URI, S3Scheme+"://") {
		t.Errorf("URI = %q, want %s:// prefix", ref.URI, S3Scheme)
	}
	if err := ref.Validate(); err != nil {
		t.Errorf("Put produced a reference that does not validate: %v", err)
	}

	rc, err := st.Open(context.Background(), ref)
	if err != nil {
		t.Fatal(err)
	}
	defer rc.Close()
	got, err := io.ReadAll(rc)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != content {
		t.Errorf("read back %d bytes, want %d", len(got), len(content))
	}
}

func TestS3Store_EmptyContent(t *testing.T) {
	st := newTestS3Store(t)
	ref, err := st.Put(context.Background(), "run-1", strings.NewReader(""), PutOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if ref.SizeBytes != 0 {
		t.Errorf("size = %d, want 0", ref.SizeBytes)
	}
	rc, err := st.Open(context.Background(), ref)
	if err != nil {
		t.Fatalf("could not read back an empty artifact: %v", err)
	}
	defer rc.Close()
	got, err := io.ReadAll(rc)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Errorf("read %d bytes from an empty artifact", len(got))
	}
}

func TestS3Store_DefaultsMediaType(t *testing.T) {
	st := newTestS3Store(t)
	ref, err := st.Put(context.Background(), "run-1", strings.NewReader("x"), PutOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if ref.MediaType != MediaTypeOctetStream {
		t.Errorf("media type = %q, want %q", ref.MediaType, MediaTypeOctetStream)
	}
}

// Identical content in one namespace resolves to the same reference,
// mirroring LocalDiskStore's dedup guarantee — the interface's own
// contract, not an S3-specific extra.
func TestS3Store_DeduplicatesWithinNamespace(t *testing.T) {
	st := newTestS3Store(t)
	ctx := context.Background()

	ref1, err := st.Put(ctx, "run-1", strings.NewReader("same content"), PutOptions{})
	if err != nil {
		t.Fatal(err)
	}
	ref2, err := st.Put(ctx, "run-1", strings.NewReader("same content"), PutOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if ref1.URI != ref2.URI {
		t.Errorf("identical content produced different references: %q vs %q", ref1.URI, ref2.URI)
	}
}

// Identical content in different namespaces gets different references —
// namespaces are isolated, not a shared dedup pool, so DeleteNamespace on
// one can never remove data another namespace still points at.
func TestS3Store_NamespacesAreIsolated(t *testing.T) {
	st := newTestS3Store(t)
	ctx := context.Background()

	refA, err := st.Put(ctx, "run-a", strings.NewReader("shared bytes"), PutOptions{})
	if err != nil {
		t.Fatal(err)
	}
	refB, err := st.Put(ctx, "run-b", strings.NewReader("shared bytes"), PutOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if refA.URI == refB.URI {
		t.Errorf("different namespaces produced the same reference: %q", refA.URI)
	}

	if err := st.DeleteNamespace(ctx, "run-a"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Open(ctx, refB); err != nil {
		t.Errorf("deleting run-a's namespace affected run-b's blob: %v", err)
	}
}

func TestS3Store_DeleteNamespaceIsIdempotent(t *testing.T) {
	st := newTestS3Store(t)
	if err := st.DeleteNamespace(context.Background(), "never-written"); err != nil {
		t.Errorf("deleting an empty namespace should be a no-op, got: %v", err)
	}
}

// DeleteNamespace must reclaim every blob under a namespace even when
// there are more than one page's worth of keys — ListObjectsV2 caps a
// single response at 1000 keys, so this exercises the continuation-token
// loop, not just the single-page happy path.
func TestS3Store_DeleteNamespaceHandlesManyKeys(t *testing.T) {
	st := newTestS3Store(t)
	ctx := context.Background()

	const n = 1500
	var lastRef *ArtifactRef
	for i := 0; i < n; i++ {
		ref, err := st.Put(ctx, "big-run", strings.NewReader(uniqueContent(i)), PutOptions{})
		if err != nil {
			t.Fatalf("put %d: %v", i, err)
		}
		lastRef = ref
	}

	if err := st.DeleteNamespace(ctx, "big-run"); err != nil {
		t.Fatalf("delete namespace with %d keys: %v", n, err)
	}
	if _, err := st.Open(ctx, lastRef); !errors.Is(err, ErrNotFound) {
		t.Errorf("expected ErrNotFound after namespace delete, got: %v", err)
	}
}

func uniqueContent(i int) string {
	return "content-" + string(rune('a'+i%26)) + string(rune(i))
}

func TestS3Store_OpenMissingIsNotFound(t *testing.T) {
	st := newTestS3Store(t)
	ref := &ArtifactRef{
		URI:      S3Scheme + "://" + st.namespacePrefix("run-1") + "/" + strings.Repeat("0", 64),
		Checksum: "sha256:" + strings.Repeat("0", 64),
	}
	_, err := st.Open(context.Background(), ref)
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("expected ErrNotFound, got: %v", err)
	}
}

// A caller that reads the whole stream and finds the bytes don't match
// the reference's checksum must learn about it, even though verification
// here is incremental rather than eager (see Open's doc comment).
func TestS3Store_DetectsCorruption(t *testing.T) {
	st := newTestS3Store(t)
	ctx := context.Background()

	ref, err := st.Put(ctx, "run-1", strings.NewReader("original content"), PutOptions{})
	if err != nil {
		t.Fatal(err)
	}

	// Corrupt the reference's own checksum to simulate stored bytes no
	// longer matching what a manifest says they should be — the same
	// effect as the underlying object being altered after it was written.
	corrupted := *ref
	corrupted.Checksum = "sha256:" + strings.Repeat("f", 64)

	rc, err := st.Open(ctx, &corrupted)
	if err != nil {
		t.Fatal(err)
	}
	defer rc.Close()
	_, err = io.ReadAll(rc)
	if !errors.Is(err, ErrChecksumMismatch) {
		t.Errorf("expected ErrChecksumMismatch, got: %v", err)
	}
}

func TestS3Store_RejectsForeignURI(t *testing.T) {
	st := newTestS3Store(t)
	ref := &ArtifactRef{
		URI:      "local://deadbeef/" + strings.Repeat("0", 64),
		Checksum: "sha256:" + strings.Repeat("0", 64),
	}
	_, err := st.Open(context.Background(), ref)
	if err == nil {
		t.Fatal("expected an error opening a local:// reference against an S3Store")
	}
}

// This is the acceptance criterion Tnsor-Labs/brokoli#268 exists to prove,
// not just assert by code review: a large Put must never hand the whole
// value to a single Read call the way io.ReadAll(r)-then-upload would.
// maxReadReader records the biggest single []byte a caller ever requested;
// a genuinely streaming multipart upload reads in bounded, part-sized
// chunks (5 MiB by default), while a buffer-then-upload implementation
// would drive its reader to fill one allocation sized for the whole input.
// 24 MiB is comfortably more than one part, so this also exercises a
// real multi-part upload, not just a single-part happy path.
type maxReadReader struct {
	r       io.Reader
	maxRead int
}

func (m *maxReadReader) Read(p []byte) (int, error) {
	if len(p) > m.maxRead {
		m.maxRead = len(p)
	}
	return m.r.Read(p)
}

func TestS3Store_PutStreamsRatherThanBuffers(t *testing.T) {
	st := newTestS3Store(t)
	const size = 24 * 1024 * 1024 // 24 MiB — several multipart parts at the SDK's 5 MiB default

	tracked := &maxReadReader{r: io.LimitReader(zeroReader{}, size)}
	ref, err := st.Put(context.Background(), "run-1", tracked, PutOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if ref.SizeBytes != size {
		t.Fatalf("size = %d, want %d", ref.SizeBytes, size)
	}

	// A single-shot io.ReadAll(r)-then-PutObject implementation would
	// eventually request a read sized for (close to) the whole remaining
	// input as its internal buffer grows to fit it. Requiring the largest
	// single request stay well under the total size is a direct,
	// deterministic (no GC-timing-dependent memory sampling) signal that
	// the SDK is consuming this reader in bounded chunks instead.
	const boundedChunkCeiling = 8 * 1024 * 1024 // generous margin above the 5 MiB default part size
	if tracked.maxRead > boundedChunkCeiling {
		t.Errorf("largest single Read request was %d bytes (%.1f%% of the %d-byte payload) — looks like the whole value was buffered instead of streamed in bounded chunks",
			tracked.maxRead, 100*float64(tracked.maxRead)/float64(size), size)
	}
}

// zeroReader is an infinite source of zero bytes, wrapped in io.LimitReader
// above — avoids allocating a 24 MiB buffer in the test itself just to
// prove the code under test doesn't allocate one either.
type zeroReader struct{}

func (zeroReader) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = 0
	}
	return len(p), nil
}

func TestS3Store_PutRejectsEmptyNamespace(t *testing.T) {
	st := newTestS3Store(t)
	_, err := st.Put(context.Background(), "", strings.NewReader("x"), PutOptions{})
	if err == nil {
		t.Fatal("expected an error for an empty namespace")
	}
}

func TestS3Store_HonoursContextCancellation(t *testing.T) {
	st := newTestS3Store(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := st.Put(ctx, "run-1", strings.NewReader("x"), PutOptions{}); err == nil {
		t.Error("expected Put to fail with a cancelled context")
	}
	if err := st.DeleteNamespace(ctx, "run-1"); err == nil {
		t.Error("expected DeleteNamespace to fail with a cancelled context")
	}
}

// A caller-supplied CredentialsProvider must take precedence over
// AccessKeyID/SecretAccessKey — the whole point of exposing it is to let
// a caller hand through credentials (e.g. an STS-minted, auto-refreshing
// session) it already resolved itself, not have this package silently
// prefer a static pair set alongside it by mistake.
func TestNewS3Store_CredentialsProviderTakesPrecedence(t *testing.T) {
	backend := s3mem.New()
	faker := gofakes3.New(backend)
	ts := httptest.NewServer(faker.Server())
	defer ts.Close()
	if err := backend.CreateBucket("bucket"); err != nil {
		t.Fatalf("create test bucket: %v", err)
	}

	provider := credentials.NewStaticCredentialsProvider("from-provider", "from-provider-secret", "")
	st, err := NewS3Store(context.Background(), S3StoreConfig{
		Bucket:              "bucket",
		Region:              "us-east-1",
		Endpoint:            ts.URL,
		UsePathStyle:        true,
		AccessKeyID:         "from-static-fields",
		SecretAccessKey:     "from-static-fields-secret",
		CredentialsProvider: provider,
	})
	if err != nil {
		t.Fatalf("NewS3Store: %v", err)
	}

	got, err := st.client.Options().Credentials.Retrieve(context.Background())
	if err != nil {
		t.Fatalf("retrieve resolved credentials: %v", err)
	}
	if got.AccessKeyID != "from-provider" {
		t.Errorf("resolved AccessKeyID = %q, want %q (the injected CredentialsProvider, not the static fields)", got.AccessKeyID, "from-provider")
	}
}

func TestNewS3Store_RequiresBucket(t *testing.T) {
	_, err := NewS3Store(context.Background(), S3StoreConfig{})
	if err == nil {
		t.Fatal("expected an error for a missing bucket")
	}
}
