package artifact

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// LocalDiskScheme is the URI scheme LocalDiskStore stamps into the
// references it produces. Open refuses any other scheme rather than
// guessing, so a reference produced by a future S3 backend cannot be
// silently resolved against the wrong store.
const LocalDiskScheme = "local"

// LocalDiskStore keeps blobs as files under a base directory.
//
// It is the only backend in this milestone. On a single host, or on several
// workers sharing one volume, it is sufficient; a worker that cannot see the
// filesystem another worker wrote to will get ErrNotFound, which ADR-012
// records as a known limitation of local disk rather than something this
// type tries to hide.
type LocalDiskStore struct {
	baseDir string
}

// NewLocalDiskStore creates a store rooted at baseDir. Directories are
// created on write, not here.
func NewLocalDiskStore(baseDir string) *LocalDiskStore {
	return &LocalDiskStore{baseDir: baseDir}
}

// namespaceDir maps a namespace to a directory without letting the caller's
// string reach the path.
//
// Namespaces are run IDs, and node IDs and similar identifiers elsewhere in
// this codebase come from pipeline JSON that users fully control. Hashing to
// fixed-width hex removes path traversal, separators and null bytes in one
// step — the same reasoning, and the same technique, as
// engine.LocalDiskArtifactStore.artifactPath.
func (s *LocalDiskStore) namespaceDir(namespace string) string {
	sum := sha256.Sum256([]byte(namespace))
	return filepath.Join(s.baseDir, hex.EncodeToString(sum[:]))
}

// blobPath is where content with the given hex digest lives in a namespace.
func (s *LocalDiskStore) blobPath(namespace, digest string) string {
	return filepath.Join(s.namespaceDir(namespace), digest+".blob")
}

// Put implements Store.
func (s *LocalDiskStore) Put(ctx context.Context, namespace string, r io.Reader, opts PutOptions) (*ArtifactRef, error) {
	if namespace == "" {
		return nil, fmt.Errorf("artifact: namespace is required")
	}
	if r == nil {
		return nil, fmt.Errorf("artifact: reader is required")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	dir := s.namespaceDir(namespace)
	// 0o750: artifacts hold pipeline data, which may be sensitive. Not
	// group- or world-readable, matching engine.LocalDiskArtifactStore.
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return nil, fmt.Errorf("artifact: create namespace directory: %w", err)
	}

	// The digest is not known until every byte has been read, so the content
	// is streamed to a temp file while hashing, then renamed into its final
	// content-addressed name. Streaming rather than buffering is the whole
	// point of this subsystem: an artifact large enough to be worth spilling
	// must never be held in memory to store it.
	tmp, err := os.CreateTemp(dir, ".incoming-*")
	if err != nil {
		return nil, fmt.Errorf("artifact: create temp file: %w", err)
	}
	tmpName := tmp.Name()
	defer func() {
		// Harmless once the rename below has succeeded — the name is gone.
		_ = os.Remove(tmpName)
	}()

	hasher := sha256.New()
	size, err := io.Copy(io.MultiWriter(tmp, hasher), r)
	if err != nil {
		_ = tmp.Close()
		return nil, fmt.Errorf("artifact: write blob: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return nil, fmt.Errorf("artifact: close blob: %w", err)
	}

	digest := hex.EncodeToString(hasher.Sum(nil))
	final := s.blobPath(namespace, digest)

	// Rename publishes the blob atomically, so a reader never observes a
	// partial file. Identical content in the same namespace lands on the
	// same name, which makes a repeat write a no-op rather than a duplicate.
	if err := os.Rename(tmpName, final); err != nil {
		return nil, fmt.Errorf("artifact: finalize blob: %w", err)
	}

	mediaType := opts.MediaType
	if mediaType == "" {
		mediaType = MediaTypeOctetStream
	}
	return &ArtifactRef{
		URI:       fmt.Sprintf("%s://%s/%s", LocalDiskScheme, filepath.Base(dir), digest),
		MediaType: mediaType,
		SizeBytes: size,
		Checksum:  "sha256:" + digest,
		CreatedAt: time.Now().UTC(),
	}, nil
}

// parseURI splits a local:// reference into its namespace directory and
// digest. It returns an error rather than a best guess for anything it does
// not recognize, including another backend's scheme.
func parseURI(uri string) (nsDir, digest string, err error) {
	rest, ok := strings.CutPrefix(uri, LocalDiskScheme+"://")
	if !ok {
		return "", "", fmt.Errorf("artifact: %q is not a %s:// reference", uri, LocalDiskScheme)
	}
	nsDir, digest, ok = strings.Cut(rest, "/")
	if !ok || nsDir == "" || digest == "" {
		return "", "", fmt.Errorf("artifact: malformed reference %q", uri)
	}
	// Both halves are hex digests this store produced. Validating that
	// keeps a hand-edited or hostile URI from reaching filepath.Join with
	// separators or dot segments in it.
	if !isHex(nsDir) || !isHex(digest) {
		return "", "", fmt.Errorf("artifact: reference %q has non-hex components", uri)
	}
	return nsDir, digest, nil
}

func isHex(s string) bool {
	if s == "" {
		return false
	}
	_, err := hex.DecodeString(s)
	return err == nil
}

// Open implements Store.
func (s *LocalDiskStore) Open(ctx context.Context, ref *ArtifactRef) (io.ReadCloser, error) {
	if err := ref.Validate(); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	nsDir, digest, err := parseURI(ref.URI)
	if err != nil {
		return nil, err
	}
	path := filepath.Join(s.baseDir, nsDir, digest+".blob")

	f, err := os.Open(path) // #nosec G304 -- path is baseDir joined with two hex digests validated by parseURI above; no caller-supplied path component reaches it.
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("%w: %s", ErrNotFound, ref.URI)
		}
		return nil, fmt.Errorf("artifact: open blob: %w", err)
	}

	// The name a blob is stored under is its content hash, so a mismatch
	// against the reference means the file was altered after it was written.
	// Verifying costs a full read of data the caller is about to read
	// anyway, and the alternative — handing corrupted bytes to a node as its
	// input — is exactly what a checksum exists to prevent.
	if err := verifyChecksum(f, ref.Checksum); err != nil {
		_ = f.Close()
		return nil, err
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("artifact: rewind blob: %w", err)
	}
	return f, nil
}

func verifyChecksum(f *os.File, want string) error {
	hasher := sha256.New()
	if _, err := io.Copy(hasher, f); err != nil {
		return fmt.Errorf("artifact: verify blob: %w", err)
	}
	got := "sha256:" + hex.EncodeToString(hasher.Sum(nil))
	if got != want {
		return fmt.Errorf("%w: stored %s, reference says %s", ErrChecksumMismatch, got, want)
	}
	return nil
}

// DeleteNamespace implements Store. Every blob for a namespace lives under
// one directory, so this is a single removal — the reason blobs are scoped
// per namespace rather than shared globally.
func (s *LocalDiskStore) DeleteNamespace(ctx context.Context, namespace string) error {
	if namespace == "" {
		return fmt.Errorf("artifact: namespace is required")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := os.RemoveAll(s.namespaceDir(namespace)); err != nil {
		return fmt.Errorf("artifact: delete namespace: %w", err)
	}
	return nil
}
