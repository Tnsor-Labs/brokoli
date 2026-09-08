package artifact

import (
	"context"
	"errors"
	"io"
)

// ErrNotFound indicates the bytes a reference points at are not in the
// store. It is returned wrapped, so callers should test with errors.Is.
var ErrNotFound = errors.New("artifact: not found")

// ErrChecksumMismatch indicates stored bytes did not hash to the checksum
// recorded in the reference that pointed at them.
//
// This is deliberately distinct from ErrNotFound: "the data is gone" and
// "the data is not what was written" call for very different responses, and
// collapsing them would let silent corruption look like a cache miss.
var ErrChecksumMismatch = errors.New("artifact: checksum mismatch")

// PutOptions describes bytes being written, in the terms only the producer
// knows.
type PutOptions struct {
	// MediaType is the IANA media type of the bytes. Defaults to
	// MediaTypeOctetStream when empty.
	MediaType string
}

// Store holds bytes and hands back references to them.
//
// Writes are content-addressed: the store decides where bytes live and
// reports it in the returned reference. Callers never construct a URI, which
// is what allows a different backend to be swapped in without every producer
// learning its addressing scheme.
//
// Everything is scoped to a namespace — a run ID, in practice. Blobs are not
// shared across namespaces even when their contents are identical, so that
// deleting a namespace can reclaim every byte it wrote without reference
// counting, and so one run's retention can never delete data another run is
// still pointing at. Deduplication therefore applies within a run, which is
// where a pipeline actually repeats itself.
type Store interface {
	// Put stores everything readable from r and returns a reference to it.
	// Identical content written twice in one namespace resolves to the same
	// reference.
	Put(ctx context.Context, namespace string, r io.Reader, opts PutOptions) (*ArtifactRef, error)

	// Open returns the bytes a reference points at. The caller closes the
	// reader. Returns ErrNotFound if the blob is absent, and
	// ErrChecksumMismatch if it is present but altered.
	Open(ctx context.Context, ref *ArtifactRef) (io.ReadCloser, error)

	// DeleteNamespace removes every blob written under a namespace. It is a
	// no-op, not an error, when nothing was written.
	DeleteNamespace(ctx context.Context, namespace string) error
}

// DigestResolver is implemented by a Store that can name a blob from its
// namespace and content digest alone, without being handed a URI.
//
// Optional and discovered by type assertion, the same way
// engine.BlobStoreProvider is, so a backend that cannot do this stays a
// valid Store.
//
// It exists for ADR-033 section 6, which requires that a reference
// handed to a remote worker be "an opaque control-plane-issued
// capability, never a general URL, URI, or host path". A store URI is a
// location: it carries a scheme and a layout. Letting a capability name
// a blob by content digest keeps the location entirely server-side,
// which is the property the ADR is actually asking for.
type DigestResolver interface {
	// ResolveDigest returns a reference to the blob stored under
	// namespace with the given "sha256:<hex>" checksum. It does not
	// promise the blob exists; Open reports that.
	ResolveDigest(namespace, checksum string) (*ArtifactRef, error)
}
