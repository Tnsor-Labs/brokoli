package fetchers

import (
	"context"
	"io"

	"github.com/Tnsor-Labs/brokoli/pkg/artifact"
	"github.com/Tnsor-Labs/brokoli/pkg/common"
)

// ArtifactSink stores an opaque response body and returns a reference to it.
//
// It is the narrowest slice of pkg/artifact.Store a fetcher needs: a fetcher
// writes bytes and never reads them back, and it has no business knowing
// about namespaces or deletion. The engine binds those before handing the
// sink over, so a fetcher cannot write outside the run it belongs to.
type ArtifactSink interface {
	PutArtifact(ctx context.Context, r io.Reader, mediaType string) (*artifact.ArtifactRef, error)
}

// ArtifactAwareFetcher is implemented by fetchers that can store a
// response="artifact" body by reference instead of inlining it.
//
// It is optional, in the same way CheckpointingFetcher is: the engine offers
// a sink when it has one, and a fetcher without a sink keeps its previous
// behaviour. That is what lets validation, dry runs and unit tests construct
// a bare fetcher and still get a usable result.
type ArtifactAwareFetcher interface {
	SetArtifactSink(sink ArtifactSink)
}

// Artifact reference column names, as they appear in the single-row dataset
// a source_api node emits for response="artifact".
//
// The reference travels inside a *common.DataSet because that is still the
// only thing a node can return — making a reference a first-class node
// output is the spill work (Tnsor-Labs/brokoli#38 M3), which has not
// happened. What changed here is that the row now describes where the bytes
// are rather than being the bytes.
const (
	ArtifactColURI       = "uri"
	ArtifactColMediaType = "media_type"
	ArtifactColSizeBytes = "size_bytes"
	ArtifactColChecksum  = "checksum"
)

// artifactRefDataSet renders a stored artifact's reference as the one-row
// dataset a node returns.
func artifactRefDataSet(ref *artifact.ArtifactRef) *common.DataSet {
	return &common.DataSet{
		Columns: []string{ArtifactColURI, ArtifactColMediaType, ArtifactColSizeBytes, ArtifactColChecksum},
		Rows: []common.DataRow{{
			ArtifactColURI:       ref.URI,
			ArtifactColMediaType: ref.MediaType,
			ArtifactColSizeBytes: ref.SizeBytes,
			ArtifactColChecksum:  ref.Checksum,
		}},
	}
}
