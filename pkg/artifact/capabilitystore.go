package artifact

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// CapabilityStore is the Store a remote task worker uses: it holds no
// bytes itself and reaches the control plane for each object, presenting
// a capability that grants exactly that one operation (ADR-033 §6).
//
// It is the counterpart to the server's blob endpoint. A worker gets its
// capabilities in the work order, one per object it is allowed to touch,
// and can do nothing else with this store -- an object it holds no
// capability for is refused here, before any request is made, because
// there is nothing to present.
//
// Deliberately NOT a general HTTP client for the blob API. There is no
// method to list, discover, or address an object by anything other than
// a capability the control plane already issued.
type CapabilityStore struct {
	// BaseURL is the control plane, e.g. "https://brokoli.example".
	BaseURL string
	// AuthHeader is the worker's own identity, which is separate from any
	// capability: the server takes the tenant from this and the grant
	// from the capability, and compares them. One cannot stand in for the
	// other.
	AuthHeader string
	// RunID, NodeID and Attempt identify the execution this worker is
	// running, and form the URL the capability is checked against.
	RunID   string
	NodeID  string
	Attempt int
	// Capabilities maps an object's content digest to the capability
	// token granting access to it. Read and write grants for the same
	// object are different tokens; whichever operation is attempted must
	// find one that permits it, and the server has the final say.
	Capabilities map[string]string
	// HTTPClient is optional; http.DefaultClient is used when nil.
	HTTPClient *http.Client
	// WriteObjectID is the digest a Put is pre-authorized to create. The
	// control plane allocates it before dispatch, because a write
	// capability has to name its object and the content is not known
	// until the task produces it.
	WriteObjectID string
}

func (c *CapabilityStore) client() *http.Client {
	if c.HTTPClient != nil {
		return c.HTTPClient
	}
	return http.DefaultClient
}

func (c *CapabilityStore) objectURL(objectID string) string {
	return fmt.Sprintf("%s/api/runs/%s/nodes/%s/attempts/%d/blobs/%s",
		strings.TrimRight(c.BaseURL, "/"), c.RunID, c.NodeID, c.Attempt, objectID)
}

// Open fetches the bytes a read capability names.
//
// The reference's checksum is verified against what actually arrived, so
// a truncated or substituted response is an error rather than short
// data. That matters more here than for a local store: the bytes crossed
// a network, and the whole point of a content-addressed reference is
// that the reader can prove it got what the writer put.
func (c *CapabilityStore) Open(ctx context.Context, ref *ArtifactRef) (io.ReadCloser, error) {
	if ref == nil || ref.Checksum == "" {
		return nil, fmt.Errorf("artifact: a capability store opens objects by checksum; the reference carries none")
	}
	token, ok := c.Capabilities[ref.Checksum]
	if !ok {
		// Refused before any request: holding no capability for an object
		// is not something to discover from a server's response.
		return nil, fmt.Errorf("artifact: no capability for object %s", ref.Checksum)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.objectURL(ref.Checksum), nil)
	if err != nil {
		return nil, err
	}
	c.setHeaders(req, token)

	resp, err := c.client().Do(req)
	if err != nil {
		return nil, fmt.Errorf("artifact: fetch object: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusNotFound:
		return nil, fmt.Errorf("%w: %s", ErrNotFound, ref.Checksum)
	default:
		return nil, fmt.Errorf("artifact: fetch object: %s: %s", resp.Status, readSnippet(resp.Body))
	}

	// Buffered rather than streamed, deliberately: the checksum cannot be
	// verified until the last byte, and handing back a reader that only
	// reports corruption at EOF invites a caller to act on bad data it
	// has already consumed. The task contract's size caps bound this.
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("artifact: read object: %w", err)
	}
	sum := sha256.Sum256(body)
	if got := "sha256:" + hex.EncodeToString(sum[:]); !strings.EqualFold(got, ref.Checksum) {
		return nil, fmt.Errorf("%w: %s arrived hashing to %s", ErrChecksumMismatch, ref.Checksum, got)
	}
	return io.NopCloser(bytes.NewReader(body)), nil
}

// Put uploads bytes under the write capability this worker was issued.
//
// namespace is accepted for the Store interface and ignored: a worker
// does not choose where its output goes, the capability does. Honouring
// a caller-supplied namespace here would let a task write outside its
// own run.
func (c *CapabilityStore) Put(ctx context.Context, namespace string, r io.Reader, opts PutOptions) (*ArtifactRef, error) {
	if c.WriteObjectID == "" {
		return nil, fmt.Errorf("artifact: this worker holds no write capability")
	}
	token, ok := c.Capabilities[c.WriteObjectID]
	if !ok {
		return nil, fmt.Errorf("artifact: no capability for object %s", c.WriteObjectID)
	}

	body, err := io.ReadAll(r)
	if err != nil {
		return nil, fmt.Errorf("artifact: read content: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, c.objectURL(c.WriteObjectID), bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	c.setHeaders(req, token)
	if opts.MediaType != "" {
		req.Header.Set("Content-Type", opts.MediaType)
	}

	resp, err := c.client().Do(req)
	if err != nil {
		return nil, fmt.Errorf("artifact: store object: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusCreated {
		return nil, fmt.Errorf("artifact: store object: %s: %s", resp.Status, readSnippet(resp.Body))
	}

	var stored struct {
		ObjectID  string `json:"object_id"`
		SizeBytes int64  `json:"size_bytes"`
		Checksum  string `json:"checksum"`
		MediaType string `json:"media_type"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&stored); err != nil {
		return nil, fmt.Errorf("artifact: store object: malformed response: %w", err)
	}

	// What the server stored must hash to what was sent. Without this the
	// worker would report a reference for bytes it never confirmed, and
	// a downstream reader's own check would fail far from the cause.
	sum := sha256.Sum256(body)
	if want := "sha256:" + hex.EncodeToString(sum[:]); !strings.EqualFold(stored.Checksum, want) {
		return nil, fmt.Errorf("%w: uploaded %s, server recorded %s", ErrChecksumMismatch, want, stored.Checksum)
	}
	return &ArtifactRef{
		URI:       stored.ObjectID,
		MediaType: stored.MediaType,
		SizeBytes: stored.SizeBytes,
		Checksum:  stored.Checksum,
	}, nil
}

// DeleteNamespace is not available to a worker.
//
// Retention is a control-plane decision (ADR-012). A worker that could
// delete a namespace could destroy another attempt's results, which no
// capability it holds grants and which nothing in the task contract
// needs.
func (c *CapabilityStore) DeleteNamespace(ctx context.Context, namespace string) error {
	return fmt.Errorf("artifact: a capability store cannot delete a namespace; retention is a control-plane operation")
}

func (c *CapabilityStore) setHeaders(req *http.Request, token string) {
	if c.AuthHeader != "" {
		req.Header.Set("Authorization", c.AuthHeader)
	}
	req.Header.Set("X-Brokoli-Data-Capability", token)
}

// readSnippet returns a bounded piece of an error response, so a server
// message reaches the log without an unbounded body doing so.
func readSnippet(r io.Reader) string {
	b, _ := io.ReadAll(io.LimitReader(r, 512))
	return strings.TrimSpace(string(b))
}

var _ Store = (*CapabilityStore)(nil)
