package artifact

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func digestOf(body string) string {
	sum := sha256.Sum256([]byte(body))
	return "sha256:" + hex.EncodeToString(sum[:])
}

// fakeControlPlane stands in for the blob endpoint, recording what it
// was presented so the tests can assert the client sends identity AND a
// capability rather than conflating them.
type fakeControlPlane struct {
	objects   map[string]string // digest -> body
	gotCap    string
	gotAuth   string
	gotPath   string
	gotMethod string
	// corrupt makes the server return bytes that do not match the digest,
	// standing in for truncation or substitution in transit.
	corrupt bool
}

func (f *fakeControlPlane) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.gotCap = r.Header.Get("X-Brokoli-Data-Capability")
		f.gotAuth = r.Header.Get("Authorization")
		f.gotPath = r.URL.Path
		f.gotMethod = r.Method

		parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
		objectID := parts[len(parts)-1]

		switch r.Method {
		case http.MethodGet:
			body, ok := f.objects[objectID]
			if !ok {
				http.Error(w, "object not found", http.StatusNotFound)
				return
			}
			if f.corrupt {
				body = "tampered"
			}
			_, _ = io.WriteString(w, body)
		case http.MethodPut:
			raw, _ := io.ReadAll(r.Body)
			sum := sha256.Sum256(raw)
			checksum := "sha256:" + hex.EncodeToString(sum[:])
			if f.objects == nil {
				f.objects = map[string]string{}
			}
			f.objects[checksum] = string(raw)
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"object_id":  checksum,
				"size_bytes": len(raw),
				"checksum":   checksum,
				"media_type": r.Header.Get("Content-Type"),
			})
		}
	})
}

func newCapStore(t *testing.T, srv *httptest.Server, caps map[string]string, writeID string) *CapabilityStore {
	t.Helper()
	return &CapabilityStore{
		BaseURL:       srv.URL,
		AuthHeader:    "Bearer worker-token",
		RunID:         "run-1",
		NodeID:        "task",
		Attempt:       0,
		Capabilities:  caps,
		WriteObjectID: writeID,
		HTTPClient:    srv.Client(),
	}
}

func TestCapabilityStoreOpenFetchesAndVerifies(t *testing.T) {
	body := "input rows"
	d := digestOf(body)
	cp := &fakeControlPlane{objects: map[string]string{d: body}}
	srv := httptest.NewServer(cp.handler())
	defer srv.Close()

	s := newCapStore(t, srv, map[string]string{d: "cap-for-read"}, "")
	rc, err := s.Open(context.Background(), &ArtifactRef{Checksum: d})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = rc.Close() }()
	got, _ := io.ReadAll(rc)
	if string(got) != body {
		t.Errorf("body = %q, want %q", got, body)
	}

	// Identity and capability are both sent, and are different things:
	// the server takes the tenant from one and the grant from the other.
	if cp.gotAuth == "" {
		t.Error("the worker's own identity was not sent")
	}
	if cp.gotCap != "cap-for-read" {
		t.Errorf("capability header = %q, want the read capability", cp.gotCap)
	}
	// The object is addressed by digest -- one URL-safe segment, never a
	// store URI with a scheme and slashes.
	if !strings.HasSuffix(cp.gotPath, "/blobs/"+d) {
		t.Errorf("path = %q, want it to end in the object digest", cp.gotPath)
	}
}

// Holding no capability for an object must be refused BEFORE a request
// is made. Whether an object exists is not something a worker should be
// able to learn by watching a server's response.
func TestCapabilityStoreRefusesAnObjectItHoldsNoCapabilityFor(t *testing.T) {
	cp := &fakeControlPlane{objects: map[string]string{}}
	srv := httptest.NewServer(cp.handler())
	defer srv.Close()

	s := newCapStore(t, srv, map[string]string{}, "")
	_, err := s.Open(context.Background(), &ArtifactRef{Checksum: digestOf("secret")})
	if err == nil {
		t.Fatal("an object with no capability was fetched")
	}
	if cp.gotMethod != "" {
		t.Errorf("a request was made despite holding no capability: %s %s", cp.gotMethod, cp.gotPath)
	}
}

// The bytes crossed a network, so the reference's checksum is the only
// thing that makes "what the writer put" and "what the reader got" the
// same claim.
func TestCapabilityStoreDetectsCorruptionInTransit(t *testing.T) {
	body := "input rows"
	d := digestOf(body)
	cp := &fakeControlPlane{objects: map[string]string{d: body}, corrupt: true}
	srv := httptest.NewServer(cp.handler())
	defer srv.Close()

	s := newCapStore(t, srv, map[string]string{d: "cap"}, "")
	_, err := s.Open(context.Background(), &ArtifactRef{Checksum: d})
	if !errors.Is(err, ErrChecksumMismatch) {
		t.Fatalf("err = %v, want a checksum mismatch -- altered bytes were accepted", err)
	}
}

func TestCapabilityStoreOpenReportsNotFound(t *testing.T) {
	d := digestOf("absent")
	cp := &fakeControlPlane{objects: map[string]string{}}
	srv := httptest.NewServer(cp.handler())
	defer srv.Close()

	s := newCapStore(t, srv, map[string]string{d: "cap"}, "")
	_, err := s.Open(context.Background(), &ArtifactRef{Checksum: d})
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

func TestCapabilityStorePutUploadsUnderItsWriteCapability(t *testing.T) {
	cp := &fakeControlPlane{}
	srv := httptest.NewServer(cp.handler())
	defer srv.Close()

	// The control plane allocates the output object id before dispatch,
	// because a write capability must name its object and the content is
	// not known until the task produces it.
	writeID := "sha256:" + strings.Repeat("a", 64)
	s := newCapStore(t, srv, map[string]string{writeID: "cap-for-write"}, writeID)

	ref, err := s.Put(context.Background(), "ignored", strings.NewReader("output bytes"), PutOptions{MediaType: "text/plain"})
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	if ref.Checksum != digestOf("output bytes") {
		t.Errorf("checksum = %q, want the hash of what was sent", ref.Checksum)
	}
	if cp.gotCap != "cap-for-write" {
		t.Errorf("capability header = %q, want the write capability", cp.gotCap)
	}
	if !strings.HasSuffix(cp.gotPath, "/blobs/"+writeID) {
		t.Errorf("path = %q, want the pre-authorized object id", cp.gotPath)
	}
}

// A worker holding no write capability must fail locally, not by
// attempting an upload the server would refuse.
func TestCapabilityStorePutWithoutAWriteCapability(t *testing.T) {
	cp := &fakeControlPlane{}
	srv := httptest.NewServer(cp.handler())
	defer srv.Close()

	s := newCapStore(t, srv, map[string]string{}, "")
	if _, err := s.Put(context.Background(), "ns", strings.NewReader("x"), PutOptions{}); err == nil {
		t.Fatal("Put succeeded with no write capability")
	}
	if cp.gotMethod != "" {
		t.Errorf("a request was made with no write capability: %s", cp.gotMethod)
	}
}

// The namespace argument exists for the Store interface and must be
// ignored: a worker does not choose where its output goes, its
// capability does. Honouring it would let a task write outside its run.
func TestCapabilityStorePutIgnoresTheCallerSuppliedNamespace(t *testing.T) {
	cp := &fakeControlPlane{}
	srv := httptest.NewServer(cp.handler())
	defer srv.Close()

	writeID := "sha256:" + strings.Repeat("b", 64)
	s := newCapStore(t, srv, map[string]string{writeID: "cap"}, writeID)
	if _, err := s.Put(context.Background(), "../../another-run", strings.NewReader("x"), PutOptions{}); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if strings.Contains(cp.gotPath, "another-run") {
		t.Fatalf("a caller-supplied namespace reached the URL: %s", cp.gotPath)
	}
	if !strings.Contains(cp.gotPath, "/runs/run-1/") {
		t.Errorf("path = %q, want the run this worker is executing", cp.gotPath)
	}
}

// Retention is a control-plane decision (ADR-012). A worker that could
// delete a namespace could destroy another attempt's results.
func TestCapabilityStoreCannotDeleteANamespace(t *testing.T) {
	s := &CapabilityStore{}
	if err := s.DeleteNamespace(context.Background(), "run-1"); err == nil {
		t.Fatal("a worker was able to delete a namespace")
	}
}
