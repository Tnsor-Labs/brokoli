package engine

// Reading a task's artifact output (ADR-033 rollout, following the
// dataset work in task_dataset.go).
//
// ADR-032 section 6: an artifact is "opaque bytes addressed by an
// artifact reference", and "an artifact port carries an ADR-012
// reference and optional media-type set". So unlike a dataset -- which
// is decoded into rows -- an artifact's bytes stay opaque to the engine:
// they are moved into the content-addressed blob store and the node's
// output becomes the REFERENCE, never the content.
//
// The representation is deliberately the one this codebase already uses
// for an artifact-valued node output: pkg/fetchers' four-column
// uri/media_type/size_bytes/checksum row, produced today by a source_api
// node with response="artifact". A second, task-only shape for the same
// concept would make two things downstream had to understand.
//
// The staged file is read with exactly the same care as a dataset
// (ADR-033 section 7 rule 6): opened beneath a trusted staging
// descriptor with no-follow semantics, regular files only, and verified
// against the manifest's declared size and checksum -- see
// openStagedOutput, which both paths share.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"strings"

	"github.com/Tnsor-Labs/brokoli/pkg/artifact"
	"github.com/Tnsor-Labs/brokoli/pkg/common"
	"github.com/Tnsor-Labs/brokoli/pkg/fetchers"
	"github.com/Tnsor-Labs/brokoli/pkg/taskinterface"
)

// maxTaskArtifactBytes caps a single artifact output. Larger than the
// dataset cap on purpose: a dataset is decoded into rows held in memory,
// while an artifact is streamed into the blob store and never
// materialized, so the limiting factor is storage rather than heap.
const maxTaskArtifactBytes = 512 << 20 // 512 MiB

// readTaskArtifactOutput verifies a staged artifact and moves it into the
// blob store, returning the reference as the node's output.
//
// blobs is required: with nowhere to put the bytes there is no reference
// to return, and the alternative -- inlining the content into a row --
// would defeat the point of the artifact kind and can be enormous. That
// is a clear refusal rather than a silent fallback, unlike
// pkg/fetchers' inline path, which exists for small API response bodies.
func readTaskArtifactOutput(ctx context.Context, blobs artifact.Store, namespace, stagingDir, rel, declaredMediaType string, wantSize int64, wantChecksum string, port taskinterface.PortValue) (*common.DataSet, error) {
	if blobs == nil {
		return nil, fmt.Errorf("task produced an artifact output, but this server has no artifact blob store to hold it")
	}

	f, info, err := openStagedOutput(stagingDir, rel, maxTaskArtifactBytes, wantSize)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	// Verify BEFORE storing, so bytes that fail their own manifest never
	// enter the store at all. Two passes over one already-open handle,
	// never a second path lookup, so the bytes hashed are the bytes
	// stored even if the task replaces the name concurrently.
	sum := sha256.New()
	if _, err := io.Copy(sum, io.LimitReader(f, maxTaskArtifactBytes)); err != nil {
		return nil, fmt.Errorf("hash task artifact output %q: %w", rel, err)
	}
	if got := "sha256:" + hex.EncodeToString(sum.Sum(nil)); !strings.EqualFold(got, wantChecksum) {
		return nil, fmt.Errorf("task artifact output %q failed integrity verification: manifest declares %s, content hashes to %s", rel, wantChecksum, got)
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return nil, fmt.Errorf("rewind task artifact output %q: %w", rel, err)
	}

	mediaType, err := artifactMediaType(declaredMediaType, port)
	if err != nil {
		return nil, err
	}

	ref, err := blobs.Put(ctx, namespace, io.LimitReader(f, info.Size()), artifact.PutOptions{MediaType: mediaType})
	if err != nil {
		return nil, fmt.Errorf("store task artifact output %q: %w", rel, err)
	}
	return &common.DataSet{
		Columns: []string{fetchers.ArtifactColURI, fetchers.ArtifactColMediaType, fetchers.ArtifactColSizeBytes, fetchers.ArtifactColChecksum},
		Rows: []common.DataRow{{
			fetchers.ArtifactColURI:       ref.URI,
			fetchers.ArtifactColMediaType: ref.MediaType,
			fetchers.ArtifactColSizeBytes: ref.SizeBytes,
			fetchers.ArtifactColChecksum:  ref.Checksum,
		}},
	}, nil
}

// artifactMediaType decides what the stored bytes are, and enforces the
// port's constraint on that.
//
// The task-result-v1 manifest has no media_type field -- `codec` is its
// only per-output free-form descriptor -- so for an artifact that is
// where the producer states its media type, and an absent one falls back
// to the opaque default rather than being guessed from content.
//
// ADR-032 section 6: "Artifacts may constrain media_types". A port that
// does constrain them rejects anything outside the set, by name, since
// silently accepting it would let a downstream consumer receive bytes
// its own contract said it would never see. A port that constrains
// nothing accepts whatever the producer declared.
func artifactMediaType(declared string, port taskinterface.PortValue) (string, error) {
	mediaType := strings.TrimSpace(declared)
	if mediaType == "" {
		mediaType = artifact.MediaTypeOctetStream
	}
	if len(port.MediaTypes) == 0 {
		return mediaType, nil
	}
	for _, allowed := range port.MediaTypes {
		if strings.EqualFold(allowed, mediaType) {
			return mediaType, nil
		}
	}
	return "", fmt.Errorf(
		"task artifact output declares media type %q, which its port does not allow (declared media_types: %s)",
		mediaType, strings.Join(port.MediaTypes, ", "),
	)
}
