package api

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/Tnsor-Labs/brokoli/models"
	"github.com/Tnsor-Labs/brokoli/pkg/artifact"
	"github.com/Tnsor-Labs/brokoli/pkg/datacap"
	"github.com/Tnsor-Labs/brokoli/store"
)

// The data-plane endpoint a remote task worker uses. Its whole job is
// refusing things, so these tests are mostly about what it must NOT
// serve: another tenant's object, another attempt's, the opposite
// direction, or a superseded attempt's.

// blobFakeStore supplies just the one thing the handler asks the store
// for: the attempt's current fencing generation. Embedding the
// interfaces keeps it satisfying them without spelling out methods this
// handler never calls.
type blobFakeStore struct {
	store.Store
	store.ExecutionAttemptStore
	fencingGeneration int64
}

func (f *blobFakeStore) GetExecutionAttempt(runID, nodeID, instanceKey string, attempt int) (*models.ExecutionAttempt, error) {
	return &models.ExecutionAttempt{
		RunID: runID, NodeID: nodeID, InstanceKey: instanceKey, Attempt: attempt,
		FencingGeneration: f.fencingGeneration,
	}, nil
}

// blobTestRig wires a handler to an in-memory blob store and an attempt
// record, and returns a chi router serving both routes.
type blobTestRig struct {
	h      *BlobHandler
	issuer *datacap.Issuer
	blobs  artifact.Store
	router chi.Router
}

func newBlobRig(t *testing.T, fencingGeneration int64) *blobTestRig {
	t.Helper()
	key := []byte("0123456789abcdef0123456789abcdef")
	issuer, err := datacap.NewIssuer(key)
	if err != nil {
		t.Fatal(err)
	}
	blobs := artifact.NewLocalDiskStore(t.TempDir())
	h := &BlobHandler{
		store:  &blobFakeStore{fencingGeneration: fencingGeneration},
		blobs:  blobs,
		issuer: issuer,
	}
	r := chi.NewRouter()
	r.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			// Stand in for the auth middleware: the tenant comes from the
			// authenticated principal, never from the capability.
			org := req.Header.Get("X-Test-Org")
			if org != "" {
				req = req.WithContext(context.WithValue(req.Context(), OrgIDContextKey{}, org))
			}
			next.ServeHTTP(w, req)
		})
	})
	r.Get("/runs/{runID}/nodes/{nodeID}/attempts/{attempt}/blobs/{objectID}", h.Get)
	r.Put("/runs/{runID}/nodes/{nodeID}/attempts/{attempt}/blobs/{objectID}", h.Put)
	return &blobTestRig{h: h, issuer: issuer, blobs: blobs, router: r}
}

func (rig *blobTestRig) token(t *testing.T, cap datacap.Capability) string {
	t.Helper()
	tok, err := rig.issuer.Issue(cap)
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	return tok
}

func readCap(objectID string) datacap.Capability {
	return datacap.Capability{
		OrgID: "org-1", RunID: "run-1", NodeID: "task", Attempt: 0,
		FencingGeneration: 7, Direction: datacap.DirectionRead,
		Namespace: "run-1", ObjectID: objectID,
		NotAfter: time.Now().Add(time.Hour),
	}
}

func writeCap(objectID string) datacap.Capability {
	c := readCap(objectID)
	c.Direction = datacap.DirectionWrite
	return c
}

func (rig *blobTestRig) do(t *testing.T, method, objectID, token, org string, body []byte) *httptest.ResponseRecorder {
	t.Helper()
	var r io.Reader
	if body != nil {
		r = bytes.NewReader(body)
	}
	req := httptest.NewRequest(method, "/runs/run-1/nodes/task/attempts/0/blobs/"+objectID, r)
	if token != "" {
		req.Header.Set(CapabilityHeader, token)
	}
	if org != "" {
		req.Header.Set("X-Test-Org", org)
	}
	w := httptest.NewRecorder()
	rig.router.ServeHTTP(w, req)
	return w
}

func TestBlobPutThenGetRoundTrips(t *testing.T) {
	rig := newBlobRig(t, 7)

	w := rig.do(t, http.MethodPut, "out-1", rig.token(t, writeCap("out-1")), "org-1", []byte("hello blob"))
	if w.Code != http.StatusCreated {
		t.Fatalf("PUT = %d, want 201: %s", w.Code, w.Body.String())
	}
	var stored struct {
		ObjectID string `json:"object_id"`
		Checksum string `json:"checksum"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &stored); err != nil {
		t.Fatalf("PUT response is not JSON: %v", err)
	}
	if stored.ObjectID == "" || stored.Checksum == "" {
		t.Fatalf("PUT returned no reference: %s", w.Body.String())
	}

	// Read it back through a capability naming the stored object.
	// The object id is a content digest, so it is one URL-safe path
	// segment -- which a store URI (scheme, slashes) could never be.
	cap := readCap(stored.ObjectID)
	cap.Checksum = stored.Checksum
	g := rig.do(t, http.MethodGet, stored.ObjectID, rig.token(t, cap), "org-1", nil)
	if g.Code != http.StatusOK {
		t.Fatalf("GET = %d, want 200: %s", g.Code, g.Body.String())
	}
	if g.Body.String() != "hello blob" {
		t.Errorf("GET body = %q, want the bytes that were written", g.Body.String())
	}
}

// The tenant comes from the authenticated principal, never the
// capability. A capability minted for one org must not work when
// presented by another, or a worker could read across tenants by
// replaying a token.
func TestBlobRefusesACapabilityFromAnotherTenant(t *testing.T) {
	rig := newBlobRig(t, 7)
	tok := rig.token(t, writeCap("out-1"))

	w := rig.do(t, http.MethodPut, "out-1", tok, "org-2", []byte("x"))
	if w.Code != http.StatusForbidden {
		t.Fatalf("PUT as another tenant = %d, want 403: %s", w.Code, w.Body.String())
	}
}

// A read capability must not authorize a write. They are separate
// grants precisely so this cannot happen.
func TestBlobReadCapabilityCannotWrite(t *testing.T) {
	rig := newBlobRig(t, 7)
	w := rig.do(t, http.MethodPut, "out-1", rig.token(t, readCap("out-1")), "org-1", []byte("x"))
	if w.Code != http.StatusForbidden {
		t.Fatalf("PUT with a read capability = %d, want 403", w.Code)
	}
}

// The object is part of the grant: holding a capability for one object
// must not open another.
func TestBlobCapabilityIsBoundToOneObject(t *testing.T) {
	rig := newBlobRig(t, 7)
	tok := rig.token(t, readCap("out-1"))
	w := rig.do(t, http.MethodGet, "out-2", tok, "org-1", nil)
	if w.Code != http.StatusForbidden {
		t.Fatalf("GET of another object = %d, want 403", w.Code)
	}
}

// The fencing generation comes from the SERVER's attempt record, not the
// request. Once a retry claims the attempt at a higher generation, the
// superseded worker's capability stops matching -- which is what stops
// it writing over the winner's result (ADR-017).
func TestBlobRefusesASupersededAttempt(t *testing.T) {
	// The server's record has moved on to generation 8; the worker still
	// holds a capability minted at 7.
	rig := newBlobRig(t, 8)
	w := rig.do(t, http.MethodPut, "out-1", rig.token(t, writeCap("out-1")), "org-1", []byte("stale"))
	if w.Code != http.StatusForbidden {
		t.Fatalf("PUT from a superseded attempt = %d, want 403: %s", w.Code, w.Body.String())
	}
}

func TestBlobRequiresACapability(t *testing.T) {
	rig := newBlobRig(t, 7)
	w := rig.do(t, http.MethodGet, "out-1", "", "org-1", nil)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("GET with no capability = %d, want 401", w.Code)
	}
}

// An expired capability is distinguished from a wrong one: it is the
// single denial worth retrying after re-issue, rather than a bug or an
// attack.
func TestBlobExpiredCapabilityIsDistinguished(t *testing.T) {
	rig := newBlobRig(t, 7)
	// Issued already past: Issue bounds how FAR ahead an expiry may be,
	// not that it is ahead, so this needs no clock injection.
	cap := readCap("out-1")
	cap.NotAfter = time.Now().Add(-time.Minute)
	tok := rig.token(t, cap)

	w := rig.do(t, http.MethodGet, "out-1", tok, "org-1", nil)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("GET with an expired capability = %d, want 401 (retryable), got body %s", w.Code, w.Body.String())
	}
}
