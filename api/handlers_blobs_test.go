package api

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
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
	// Mounted under /api exactly as routes.go does. A rig that served
	// these at the root would pass while the real server 404s -- which is
	// precisely what the cross-check below caught.
	r.Route("/api", func(r chi.Router) {
		r.Get("/runs/{runID}/nodes/{nodeID}/attempts/{attempt}/blobs/{objectID}", h.Get)
		r.Put("/runs/{runID}/nodes/{nodeID}/attempts/{attempt}/blobs/{objectID}", h.Put)
		r.Post("/runs/{runID}/nodes/{nodeID}/attempts/{attempt}/blobs", h.Create)
	})
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

// anyObjectWriteCap is the attempt-scoped grant a task's outputs need:
// it names no object, because their ids are content digests nobody can
// know before the task runs.
func anyObjectWriteCap() datacap.Capability {
	return writeCap(datacap.AnyObject)
}

func (rig *blobTestRig) do(t *testing.T, method, objectID, token, org string, body []byte) *httptest.ResponseRecorder {
	t.Helper()
	var r io.Reader
	if body != nil {
		r = bytes.NewReader(body)
	}
	req := httptest.NewRequest(method, "/api/runs/run-1/nodes/task/attempts/0/blobs/"+objectID, r)
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

// The two halves against each other: the real handler on one side, the
// real CapabilityStore a worker uses on the other.
//
// Each side has its own tests and each would keep passing if they
// disagreed about the object-id field name, the header, or the URL
// shape. A contract between two components is exactly where that kind of
// mismatch survives, so it gets a test that spans both -- the same
// reason the SDK is verified against a real server rather than a stub.
func TestBlobEndpointAndCapabilityStoreAgree(t *testing.T) {
	rig := newBlobRig(t, 7)
	srv := httptest.NewServer(rig.router)
	defer srv.Close()

	writeID := "sha256:" + strings.Repeat("c", 64)
	writeTok := rig.token(t, writeCap(writeID))

	worker := &artifact.CapabilityStore{
		BaseURL:       srv.URL,
		RunID:         "run-1",
		NodeID:        "task",
		Attempt:       0,
		Capabilities:  map[string]string{writeID: writeTok},
		WriteObjectID: writeID,
		HTTPClient:    srv.Client(),
	}
	// The rig's middleware reads the tenant from a header; a real
	// deployment reads it from the authenticated session.
	worker.HTTPClient = &http.Client{Transport: orgInjector{base: srv.Client().Transport, org: "org-1"}}

	ref, err := worker.Put(context.Background(), "ignored", bytes.NewReader([]byte("worker output")), artifact.PutOptions{MediaType: "text/plain"})
	if err != nil {
		t.Fatalf("worker could not write through the real endpoint: %v", err)
	}
	if ref.Checksum == "" {
		t.Fatal("no checksum came back")
	}

	// Read it back with a capability for the object that was actually
	// stored -- which is the digest the server reported, proving both
	// sides agree on what an object id is.
	readTok := rig.token(t, func() datacap.Capability {
		c := readCap(ref.Checksum)
		c.Checksum = ref.Checksum
		return c
	}())
	worker.Capabilities[ref.Checksum] = readTok

	rc, err := worker.Open(context.Background(), &artifact.ArtifactRef{Checksum: ref.Checksum})
	if err != nil {
		t.Fatalf("worker could not read back what it wrote: %v", err)
	}
	defer func() { _ = rc.Close() }()
	got, _ := io.ReadAll(rc)
	if string(got) != "worker output" {
		t.Errorf("round trip returned %q", got)
	}
}

// orgInjector stands in for the auth middleware, which in a real
// deployment sets the tenant from the authenticated session.
type orgInjector struct {
	base http.RoundTripper
	org  string
}

func (o orgInjector) RoundTrip(r *http.Request) (*http.Response, error) {
	r.Header.Set("X-Test-Org", o.org)
	base := o.base
	if base == nil {
		base = http.DefaultTransport
	}
	return base.RoundTrip(r)
}

// ---------------------------------------------------------------------
// The create route: attempt-scoped writes.
// ---------------------------------------------------------------------

func (rig *blobTestRig) create(t *testing.T, token, org string, body []byte) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/runs/run-1/nodes/task/attempts/0/blobs", bytes.NewReader(body))
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

func TestBlobCreateAssignsTheObjectIDByContent(t *testing.T) {
	rig := newBlobRig(t, 7)
	w := rig.create(t, rig.token(t, anyObjectWriteCap()), "org-1", []byte("task output"))
	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	var got map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	// The id the server assigns is the content digest, which is what a
	// later read capability binds to.
	if got["object_id"] != got["checksum"] {
		t.Errorf("object_id %v != checksum %v -- an id that is not the content digest is a location", got["object_id"], got["checksum"])
	}
	if !strings.HasPrefix(got["object_id"].(string), "sha256:") {
		t.Errorf("object_id = %v, want a content digest", got["object_id"])
	}
}

// The reason the grant exists: a collection port becomes one stored
// object per item, a count decided at runtime.
func TestBlobCreateAcceptsManyObjectsUnderOneGrant(t *testing.T) {
	rig := newBlobRig(t, 7)
	token := rig.token(t, anyObjectWriteCap())

	ids := map[string]bool{}
	for _, body := range []string{"item one", "item two", "item three"} {
		w := rig.create(t, token, "org-1", []byte(body))
		if w.Code != http.StatusCreated {
			t.Fatalf("%q: status = %d, body = %s", body, w.Code, w.Body.String())
		}
		var got map[string]interface{}
		_ = json.Unmarshal(w.Body.Bytes(), &got)
		ids[got["object_id"].(string)] = true
	}
	if len(ids) != 3 {
		t.Errorf("3 distinct bodies produced %d distinct ids", len(ids))
	}
}

// The two routes are not interchangeable. A grant naming ONE object must
// not be usable on the route that creates unnamed ones, or the object
// binding would be trivially escapable.
func TestBlobCreateRefusesASingleObjectGrant(t *testing.T) {
	rig := newBlobRig(t, 7)
	named := rig.token(t, writeCap("sha256:"+strings.Repeat("d", 64)))
	w := rig.create(t, named, "org-1", []byte("x"))
	if w.Code == http.StatusCreated {
		t.Fatal("a single-object grant created an unnamed object")
	}
}

// And the reverse: an attempt-scoped grant is not a skeleton key for the
// named route either.
func TestBlobPutRefusesAnAttemptScopedGrant(t *testing.T) {
	rig := newBlobRig(t, 7)
	anyTok := rig.token(t, anyObjectWriteCap())
	w := rig.do(t, http.MethodPut, "sha256:"+strings.Repeat("e", 64), anyTok, "org-1", []byte("x"))
	if w.Code == http.StatusCreated {
		t.Fatal("an attempt-scoped grant wrote to a named object")
	}
}

// ...including when the client puts the wildcard sentinel in the URL to
// make the two ids match. Object ids here are content digests, so "*" is
// never one, and the named route must not accept it. Without this the
// routes are interchangeable in one direction and the test above is
// satisfied by a comparison that happens to agree rather than by a rule.
func TestBlobPutRefusesTheWildcardAsAnObjectID(t *testing.T) {
	rig := newBlobRig(t, 7)
	anyTok := rig.token(t, anyObjectWriteCap())
	if w := rig.do(t, http.MethodPut, datacap.AnyObject, anyTok, "org-1", []byte("x")); w.Code == http.StatusCreated {
		t.Fatal("the named route accepted the wildcard sentinel as an object id")
	}
	// A read is refused there too, so the sentinel is not a way to probe.
	if w := rig.do(t, http.MethodGet, datacap.AnyObject, anyTok, "org-1", nil); w.Code == http.StatusOK {
		t.Fatal("the wildcard sentinel served a read")
	}
}

func TestBlobCreateRefusesAReadGrant(t *testing.T) {
	rig := newBlobRig(t, 7)
	// A read grant naming AnyObject cannot even be issued, so the realistic
	// attack is a read grant for a real object presented at the create route.
	readTok := rig.token(t, readCap("sha256:"+strings.Repeat("f", 64)))
	if w := rig.create(t, readTok, "org-1", []byte("x")); w.Code == http.StatusCreated {
		t.Fatal("a read grant created an object")
	}
}

func TestBlobCreateRefusesAnotherTenant(t *testing.T) {
	rig := newBlobRig(t, 7)
	token := rig.token(t, anyObjectWriteCap())
	if w := rig.create(t, token, "org-2", []byte("x")); w.Code == http.StatusCreated {
		t.Fatal("another tenant wrote using this tenant's grant")
	}
}

// Fencing: once a retry claims the attempt at a higher generation, the
// old worker's grant stops working with nothing having to revoke it.
func TestBlobCreateRefusesASupersededAttempt(t *testing.T) {
	rig := newBlobRig(t, 9) // the store says generation 9
	stale := rig.token(t, anyObjectWriteCap())
	if w := rig.create(t, stale, "org-1", []byte("x")); w.Code == http.StatusCreated {
		t.Fatal("a superseded attempt wrote its output")
	}
}

// Capability.Checksum documented itself as binding a write to content
// agreed in advance, and until now nothing checked it. A binding nothing
// enforces is not a binding.
func TestBlobPutEnforcesAPinnedChecksum(t *testing.T) {
	rig := newBlobRig(t, 7)
	objectID := "sha256:" + strings.Repeat("a", 64)

	pinned := writeCap(objectID)
	pinned.Checksum = "sha256:" + strings.Repeat("b", 64) // not what we send

	w := rig.do(t, http.MethodPut, objectID, rig.token(t, pinned), "org-1", []byte("different content"))
	if w.Code == http.StatusCreated {
		t.Fatal("content that does not match the pinned checksum was stored")
	}
}

// A worker writing through the real endpoint with a real attempt-scoped
// grant, end to end -- the same cross-check that caught the rig serving
// routes the real server does not.
func TestCreateEndpointAndCapabilityStoreAgree(t *testing.T) {
	rig := newBlobRig(t, 7)
	srv := httptest.NewServer(rig.router)
	defer srv.Close()

	worker := &artifact.CapabilityStore{
		BaseURL:         srv.URL,
		RunID:           "run-1",
		NodeID:          "task",
		Attempt:         0,
		WriteCapability: rig.token(t, anyObjectWriteCap()),
		HTTPClient:      &http.Client{Transport: orgInjector{base: srv.Client().Transport, org: "org-1"}},
	}

	// Two objects under one grant, which is the whole point.
	var refs []string
	for _, body := range []string{"first artifact", "second artifact"} {
		ref, err := worker.Put(context.Background(), "ignored", bytes.NewReader([]byte(body)), artifact.PutOptions{MediaType: "application/octet-stream"})
		if err != nil {
			t.Fatalf("worker could not write %q through the real endpoint: %v", body, err)
		}
		refs = append(refs, ref.Checksum)
	}
	if refs[0] == refs[1] {
		t.Fatal("two different artifacts landed under one id")
	}

	// Read one back, proving both sides agree on what an object id is.
	readTok := rig.token(t, func() datacap.Capability {
		c := readCap(refs[0])
		c.Checksum = refs[0]
		return c
	}())
	worker.Capabilities = map[string]string{refs[0]: readTok}
	rc, err := worker.Open(context.Background(), &artifact.ArtifactRef{Checksum: refs[0]})
	if err != nil {
		t.Fatalf("could not read back what was written: %v", err)
	}
	defer func() { _ = rc.Close() }()
	got, _ := io.ReadAll(rc)
	if string(got) != "first artifact" {
		t.Errorf("round trip returned %q", got)
	}
}
