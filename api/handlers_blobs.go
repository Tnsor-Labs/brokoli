package api

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"sync"

	"github.com/go-chi/chi/v5"

	"github.com/Tnsor-Labs/brokoli/engine"
	"github.com/Tnsor-Labs/brokoli/pkg/artifact"
	"github.com/Tnsor-Labs/brokoli/pkg/datacap"
	"github.com/Tnsor-Labs/brokoli/store"
)

// The data-plane transport a remote task worker uses to read its input
// and write its output (ADR-033 sections 6 and 8).
//
// A remote worker has no blob store of its own: a task declaring an
// artifact or collection output works in-process and is refused on a
// remote claimant, and a task input above the inline row cap cannot be
// dispatched at all. Both need the worker to reach bytes it does not
// hold, and ADR-033 section 6 is specific about how:
//
//	Protocol references are opaque control-plane-issued capabilities,
//	never general URLs, URIs, or host paths. Each capability is bound to
//	tenant, run, attempt, fencing generation, direction,
//	object/checksum, allowed operation, and expiry.
//
// pkg/datacap is that capability. This is the endpoint that honours it.

// CapabilityHeader carries the bearer capability. A dedicated header,
// not Authorization: a worker presents BOTH its own identity (which is
// what says which tenant it acts for) and a capability for one object.
// Collapsing them would mean either identity or grant had to be
// inferred from the other, and the whole design rests on comparing two
// independently-derived descriptions.
const CapabilityHeader = "X-Brokoli-Data-Capability"

// maxBlobBytes bounds a single upload. Generous enough for the dataset
// and artifact sizes the task contract already permits, and present so
// an unbounded body cannot exhaust the server.
const maxBlobBytes = 512 << 20

// BlobHandler serves capability-authenticated blob reads and writes.
type BlobHandler struct {
	store  store.Store
	blobs  artifact.Store
	issuer *datacap.Issuer
}

var (
	datacapIssuerOnce sync.Once
	datacapIssuer     *datacap.Issuer
	datacapIssuerErr  error
)

// DatacapIssuer returns the process-wide capability issuer, keyed from
// the server's root secret.
//
// Derived rather than reused: the root secret already signs session
// tokens, and one key for two purposes means a flaw in either weakens
// both. Operators still configure exactly one secret.
func DatacapIssuer() (*datacap.Issuer, error) {
	datacapIssuerOnce.Do(func() {
		if jwtSecret == nil {
			InitJWTSecret()
		}
		key, err := datacap.DeriveKey(jwtSecret)
		if err != nil {
			datacapIssuerErr = err
			return
		}
		datacapIssuer, datacapIssuerErr = datacap.NewIssuer(key)
	})
	return datacapIssuer, datacapIssuerErr
}

// NewBlobHandler wires the handler to the engine's artifact store.
//
// Returns nil when there is no blob store to serve, so the routes are
// simply absent rather than present and failing. An endpoint that exists
// but can never succeed is worse than one that does not exist: it looks
// like a capability the deployment has.
func NewBlobHandler(s store.Store, artifacts engine.ArtifactStore) *BlobHandler {
	provider, ok := artifacts.(engine.BlobStoreProvider)
	if !ok {
		return nil
	}
	blobs := provider.Blobs()
	if blobs == nil {
		return nil
	}
	issuer, err := DatacapIssuer()
	if err != nil {
		return nil
	}
	return &BlobHandler{store: s, blobs: blobs, issuer: issuer}
}

// authorize verifies the presented capability against what this request
// is independently known to be.
//
// Every field of the Request is derived from something OTHER than the
// capability: the tenant from the authenticated principal, the identity
// from the URL, the direction from the HTTP method, and the fencing
// generation from the server's own attempt record. That is the point --
// filling any of it in from the token would verify the token against
// itself and always pass.
//
// The fencing generation in particular is why a superseded attempt
// cannot write: it comes from the store, so once a retry claims the
// attempt at a higher generation, the old worker's capability stops
// matching (ADR-017).
func (h *BlobHandler) authorize(w http.ResponseWriter, r *http.Request, direction datacap.Direction) (*datacap.Capability, bool) {
	token := r.Header.Get(CapabilityHeader)
	if token == "" {
		http.Error(w, "missing "+CapabilityHeader, http.StatusUnauthorized)
		return nil, false
	}
	orgID := GetOrgIDFromRequest(r)
	if orgID == "" {
		http.Error(w, "request carries no tenant", http.StatusUnauthorized)
		return nil, false
	}

	runID := chi.URLParam(r, "runID")
	nodeID := chi.URLParam(r, "nodeID")
	objectID := chi.URLParam(r, "objectID")
	attempt, err := strconv.Atoi(chi.URLParam(r, "attempt"))
	if err != nil {
		http.Error(w, "attempt must be an integer", http.StatusBadRequest)
		return nil, false
	}

	attemptStore, ok := h.store.(store.ExecutionAttemptStore)
	if !ok {
		http.Error(w, "this server cannot verify attempt fencing", http.StatusInternalServerError)
		return nil, false
	}
	rec, err := attemptStore.GetExecutionAttempt(runID, nodeID, "", attempt)
	if err != nil || rec == nil {
		// Deliberately not distinguished from a bad capability: probing
		// which attempts exist is not something a bearer should be able
		// to do by watching status codes.
		http.Error(w, "capability does not grant this operation", http.StatusForbidden)
		return nil, false
	}

	cap, err := h.issuer.Verify(token, datacap.Request{
		OrgID:             orgID,
		RunID:             runID,
		NodeID:            nodeID,
		Attempt:           attempt,
		FencingGeneration: rec.FencingGeneration,
		Direction:         direction,
		Namespace:         runID,
		ObjectID:          objectID,
	})
	if err != nil {
		status := http.StatusForbidden
		if errors.Is(err, datacap.ErrExpired) {
			// Distinguished because it is the one denial worth retrying
			// after re-issue, rather than a bug or an attack.
			status = http.StatusUnauthorized
		}
		http.Error(w, err.Error(), status)
		return nil, false
	}
	return cap, true
}

// Get streams the bytes a read capability names.
func (h *BlobHandler) Get(w http.ResponseWriter, r *http.Request) {
	cap, ok := h.authorize(w, r, datacap.DirectionRead)
	if !ok {
		return
	}
	// The capability names the object by CONTENT DIGEST, never by store
	// URI. A URI carries a scheme and a layout -- it is a location, and
	// ADR-033 section 6 is explicit that a reference handed to a worker
	// must not be one. Resolving the digest here keeps the location
	// entirely server-side.
	resolver, ok := h.blobs.(artifact.DigestResolver)
	if !ok {
		http.Error(w, "this server's blob store cannot resolve objects by digest", http.StatusInternalServerError)
		return
	}
	ref, err := resolver.ResolveDigest(cap.Namespace, cap.ObjectID)
	if err != nil {
		http.Error(w, "capability does not grant this operation", http.StatusForbidden)
		return
	}
	rc, err := h.blobs.Open(r.Context(), ref)
	if err != nil {
		if errors.Is(err, artifact.ErrNotFound) {
			http.Error(w, "object not found", http.StatusNotFound)
			return
		}
		http.Error(w, fmt.Sprintf("open object: %v", err), http.StatusInternalServerError)
		return
	}
	defer func() { _ = rc.Close() }()

	w.Header().Set("Content-Type", "application/octet-stream")
	if _, err := io.Copy(w, rc); err != nil {
		// The response is already streaming, so the status is sent. A
		// truncated body is what the client sees, and the checksum on
		// its side is what turns that into a detected error rather than
		// silently short data.
		return
	}
}

// Put stores the bytes a write capability names.
func (h *BlobHandler) Put(w http.ResponseWriter, r *http.Request) {
	cap, ok := h.authorize(w, r, datacap.DirectionWrite)
	if !ok {
		return
	}
	defer func() { _ = r.Body.Close() }()

	ref, err := h.blobs.Put(r.Context(), cap.Namespace, io.LimitReader(r.Body, maxBlobBytes), artifact.PutOptions{
		MediaType: r.Header.Get("Content-Type"),
	})
	if err != nil {
		http.Error(w, fmt.Sprintf("store object: %v", err), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]interface{}{
		// object_id, not uri: this is what a subsequent read capability
		// binds to, and it must stay a content digest rather than a
		// location.
		"object_id":  ref.Checksum,
		"size_bytes": ref.SizeBytes,
		"checksum":   ref.Checksum,
		"media_type": ref.MediaType,
	})
}
