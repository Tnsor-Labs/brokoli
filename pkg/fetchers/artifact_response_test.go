package fetchers

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Tnsor-Labs/brokoli/pkg/artifact"
)

// recordingSink is a stand-in for the engine's run-scoped artifact sink.
type recordingSink struct {
	store  *artifact.LocalDiskStore
	stored []byte
	err    error
	calls  int
}

func (s *recordingSink) PutArtifact(ctx context.Context, r io.Reader, mediaType string) (*artifact.ArtifactRef, error) {
	s.calls++
	if s.err != nil {
		return nil, s.err
	}
	body, err := io.ReadAll(r)
	if err != nil {
		return nil, err
	}
	s.stored = body
	return s.store.Put(ctx, "run-1", strings.NewReader(string(body)), artifact.PutOptions{MediaType: mediaType})
}

func artifactServer(t *testing.T, body string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv
}

// With a sink wired, response="artifact" stores the body and returns a
// reference to it — the point of the milestone. The row describes where the
// bytes are; it is no longer the bytes.
func TestRESTFetcher_Artifact_StoresByReference(t *testing.T) {
	body := strings.Repeat("binary-ish payload ", 500)
	srv := artifactServer(t, body)
	sink := &recordingSink{store: artifact.NewLocalDiskStore(t.TempDir())}

	f := &RESTFetcher{}
	f.SetArtifactSink(sink)

	ds, err := f.Fetch(srv.URL, map[string]interface{}{"response": "artifact"})
	if err != nil {
		t.Fatal(err)
	}
	if len(ds.Rows) != 1 {
		t.Fatalf("rows = %d, want 1", len(ds.Rows))
	}
	row := ds.Rows[0]

	// The body must not be inlined any more — that is the whole change.
	if v, ok := row["value"]; ok {
		t.Errorf("body is still inlined under \"value\": %.40v", v)
	}
	uri, _ := row[ArtifactColURI].(string)
	if uri == "" {
		t.Fatalf("no URI in the returned row: %+v", row)
	}
	if got := row[ArtifactColSizeBytes]; got != int64(len(body)) {
		t.Errorf("size = %v, want %d", got, len(body))
	}
	if sum, _ := row[ArtifactColChecksum].(string); !strings.HasPrefix(sum, "sha256:") {
		t.Errorf("checksum = %q, want a sha256 digest", sum)
	}
	if string(sink.stored) != body {
		t.Errorf("stored %d bytes, want the %d-byte response", len(sink.stored), len(body))
	}
}

// The stored bytes must be retrievable through the reference the node
// returned — a reference nothing can resolve would be worse than the shim.
func TestRESTFetcher_Artifact_ReferenceResolvesToTheBody(t *testing.T) {
	body := "the exact bytes that came back"
	srv := artifactServer(t, body)
	store := artifact.NewLocalDiskStore(t.TempDir())
	sink := &recordingSink{store: store}

	f := &RESTFetcher{}
	f.SetArtifactSink(sink)
	ds, err := f.Fetch(srv.URL, map[string]interface{}{"response": "artifact"})
	if err != nil {
		t.Fatal(err)
	}
	row := ds.Rows[0]

	ref := &artifact.ArtifactRef{
		URI:      row[ArtifactColURI].(string),
		Checksum: row[ArtifactColChecksum].(string),
	}
	rc, err := store.Open(context.Background(), ref)
	if err != nil {
		t.Fatalf("the reference the node returned does not resolve: %v", err)
	}
	defer rc.Close()
	got, _ := io.ReadAll(rc)
	if string(got) != body {
		t.Errorf("resolved to %q, want %q", got, body)
	}
}

// A store failure must fail the node. Quietly inlining a body that was
// explicitly asked to be stored would defeat the reason for asking.
func TestRESTFetcher_Artifact_StoreFailureIsAnError(t *testing.T) {
	srv := artifactServer(t, "payload")
	sink := &recordingSink{store: artifact.NewLocalDiskStore(t.TempDir()), err: errors.New("disk full")}

	f := &RESTFetcher{}
	f.SetArtifactSink(sink)

	ds, err := f.Fetch(srv.URL, map[string]interface{}{"response": "artifact"})
	if err == nil {
		t.Fatalf("store failure was swallowed, returned %+v", ds)
	}
	if !strings.Contains(err.Error(), "store artifact response") {
		t.Errorf("error should say storing the artifact failed, got: %v", err)
	}
}

// Without a sink the previous behaviour is kept exactly — this is what
// validation, dry runs and bare fetchers rely on.
func TestRESTFetcher_Artifact_NoSinkInlinesAsBefore(t *testing.T) {
	body := "not-json-at-all"
	srv := artifactServer(t, body)

	ds, err := (&RESTFetcher{}).Fetch(srv.URL, map[string]interface{}{"response": "artifact"})
	if err != nil {
		t.Fatal(err)
	}
	if len(ds.Rows) != 1 || ds.Rows[0]["value"] != body {
		t.Errorf("want the body inlined under \"value\", got %+v", ds.Rows)
	}
}

// The sink must only be consulted for response="artifact" — a dataset or
// scalar response is parsed, not stored.
func TestRESTFetcher_OtherResponseTypesDoNotTouchTheSink(t *testing.T) {
	srv := artifactServer(t, `[{"id":1},{"id":2}]`)
	sink := &recordingSink{store: artifact.NewLocalDiskStore(t.TempDir())}

	f := &RESTFetcher{}
	f.SetArtifactSink(sink)

	for _, opts := range []map[string]interface{}{
		{},
		{"response": "dataset"},
	} {
		if _, err := f.Fetch(srv.URL, opts); err != nil {
			t.Fatalf("opts %v: %v", opts, err)
		}
	}
	if sink.calls != 0 {
		t.Errorf("sink was called %d times for non-artifact responses", sink.calls)
	}
}

// RESTFetcher must satisfy the optional interface the engine type-asserts,
// or the wiring silently does nothing.
func TestRESTFetcher_IsArtifactAware(t *testing.T) {
	var _ ArtifactAwareFetcher = (*RESTFetcher)(nil)
	if _, ok := interface{}(&RESTFetcher{}).(ArtifactAwareFetcher); !ok {
		t.Fatal("RESTFetcher no longer implements ArtifactAwareFetcher")
	}
}
