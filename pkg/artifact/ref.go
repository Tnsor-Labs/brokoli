// Package artifact defines runtime references to data that lives outside a
// pipeline's memory, and the store that holds it.
//
// # These are not the SDK's *Ref types
//
// The Python SDK has ScalarRef, ArtifactRef, DatasetRef and CollectionRef
// (brokoli-sdk#2), and it is tempting to read the types here as their Go
// counterparts. They are not, and nothing serializes between them.
//
// The SDK's are subclasses of NodeRef, which holds (node_id, pipeline) and
// implements ">>" chaining. They are authoring-time handles naming a node in
// a DAG being built — "the node that will produce this" — and they carry no
// information about the data itself.
//
// The types here are runtime pointers to bytes that already exist: a URI, a
// media type, a size, a checksum. They come into being while a pipeline
// runs, long after the SDK has finished its work and emitted JSON.
//
// The names line up because both describe the same two shapes of result — an
// opaque blob and a table — from opposite ends of the system. Reading either
// as the wire form of the other will mislead you.
//
// See docs/adr/012-artifact-and-dataset-plane.md.
package artifact

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// ManifestVersion is the current on-disk/on-wire schema version for a
// serialized reference.
//
// It is written into every manifest and checked on read. Refusing an
// unrecognized version is deliberate: a reference whose fields cannot be
// interpreted with confidence must fail loudly rather than be read with the
// wrong assumptions, because the value on the other side of it is a pointer
// to data a pipeline is about to treat as its input.
const ManifestVersion = 1

// Kind distinguishes what a reference points at.
type Kind string

const (
	// KindArtifact is opaque bytes with no interpretation — a downloaded
	// file, an image, a PDF, a response body kept verbatim.
	KindArtifact Kind = "artifact"
	// KindDataset is tabular data: rows with named columns, the same shape
	// as common.DataSet, serialized to some Format.
	KindDataset Kind = "dataset"
)

// ArtifactRef points at a stored blob.
//
// A reference is immutable and self-describing: given one, a store can
// return the bytes, and a caller can tell how large they are and verify
// they are the bytes that were written, without consulting anything else.
type ArtifactRef struct {
	// URI locates the blob within its backing store, and its scheme says
	// which backend wrote it — "local://" today, "s3://" when there is an
	// S3 backend. It is resolvable only by a store configured for that
	// scheme; it is not a URL a browser can open.
	URI string `json:"uri"`

	// MediaType is the IANA media type of the bytes, e.g.
	// "application/x-ndjson" or "application/octet-stream". Stored rather
	// than inferred, because the producer knows and the reader cannot
	// reliably guess.
	MediaType string `json:"media_type"`

	// SizeBytes is the exact length of the stored bytes.
	SizeBytes int64 `json:"size_bytes"`

	// Checksum is "sha256:<hex>" over the stored bytes. It exists so a
	// reader can prove it got what the writer put, which matters more here
	// than usual: a corrupted artifact would otherwise be fed to a
	// downstream node as if it were real data.
	Checksum string `json:"checksum"`

	// CreatedAt is when the blob was written, in UTC.
	CreatedAt time.Time `json:"created_at"`
}

// DatasetRef points at stored tabular data.
//
// It embeds ArtifactRef because a dataset is an artifact — bytes in a store —
// plus what is needed to treat those bytes as rows without reading them:
// the serialization format, the column names, and how many rows there are.
// A planner can decide what to do with a dataset from its reference alone.
type DatasetRef struct {
	ArtifactRef

	// Format is how the rows are serialized. "ndjson" is the only format in
	// this milestone; Parquet is deferred (ADR-012).
	Format string `json:"format"`

	// Columns is the column order, mirroring common.DataSet.Columns.
	Columns []string `json:"columns"`

	// RowCount is the number of rows, so a caller can size work without
	// fetching and parsing the blob.
	RowCount int64 `json:"row_count"`
}

// FormatNDJSON is newline-delimited JSON, one object per row — the same
// encoding engine.WriteArrowJSON already uses for durable node output, and
// the only dataset format in this milestone.
const FormatNDJSON = "ndjson"

// MediaTypeNDJSON is the media type recorded for FormatNDJSON datasets.
const MediaTypeNDJSON = "application/x-ndjson"

// MediaTypeOctetStream is the fallback for artifacts whose producer did not
// say what they are.
const MediaTypeOctetStream = "application/octet-stream"

// Manifest is the serialized form of a reference: exactly one of Artifact or
// Dataset is set, alongside the schema version needed to read it.
//
// References are wrapped in a manifest rather than marshalled directly so
// that the version travels with the data. A bare ArtifactRef on disk would
// have no way to say which schema produced it.
type Manifest struct {
	Version  int          `json:"version"`
	Kind     Kind         `json:"kind"`
	Artifact *ArtifactRef `json:"artifact,omitempty"`
	Dataset  *DatasetRef  `json:"dataset,omitempty"`
}

// NewArtifactManifest wraps an artifact reference at the current version.
func NewArtifactManifest(ref *ArtifactRef) *Manifest {
	return &Manifest{Version: ManifestVersion, Kind: KindArtifact, Artifact: ref}
}

// NewDatasetManifest wraps a dataset reference at the current version.
func NewDatasetManifest(ref *DatasetRef) *Manifest {
	return &Manifest{Version: ManifestVersion, Kind: KindDataset, Dataset: ref}
}

// Validate reports whether the manifest is internally consistent and can be
// acted on.
//
// This is checked on every read, not only on write. A manifest is read back
// from storage that other processes and other versions of this binary can
// write, so "we wrote it, therefore it is well-formed" is not an assumption
// worth making about a pointer to a pipeline's input.
func (m *Manifest) Validate() error {
	if m == nil {
		return fmt.Errorf("artifact: nil manifest")
	}
	if m.Version != ManifestVersion {
		return fmt.Errorf("artifact: unsupported manifest version %d (this build understands %d)", m.Version, ManifestVersion)
	}
	switch m.Kind {
	case KindArtifact:
		if m.Artifact == nil {
			return fmt.Errorf("artifact: manifest kind %q with no artifact reference", m.Kind)
		}
		if m.Dataset != nil {
			return fmt.Errorf("artifact: manifest kind %q also carries a dataset reference", m.Kind)
		}
		return m.Artifact.Validate()
	case KindDataset:
		if m.Dataset == nil {
			return fmt.Errorf("artifact: manifest kind %q with no dataset reference", m.Kind)
		}
		if m.Artifact != nil {
			return fmt.Errorf("artifact: manifest kind %q also carries an artifact reference", m.Kind)
		}
		return m.Dataset.Validate()
	default:
		return fmt.Errorf("artifact: unknown manifest kind %q", m.Kind)
	}
}

// Validate reports whether the reference carries everything a store needs to
// resolve it and a reader needs to trust it.
func (r *ArtifactRef) Validate() error {
	if r == nil {
		return fmt.Errorf("artifact: nil reference")
	}
	if r.URI == "" {
		return fmt.Errorf("artifact: reference has no URI")
	}
	if r.SizeBytes < 0 {
		return fmt.Errorf("artifact: reference has negative size %d", r.SizeBytes)
	}
	if !strings.HasPrefix(r.Checksum, "sha256:") || len(r.Checksum) != len("sha256:")+64 {
		return fmt.Errorf("artifact: reference checksum %q is not a sha256:<64 hex> digest", r.Checksum)
	}
	return nil
}

// Validate reports whether the dataset reference is usable, including the
// artifact fields it embeds.
func (d *DatasetRef) Validate() error {
	if d == nil {
		return fmt.Errorf("artifact: nil dataset reference")
	}
	if err := d.ArtifactRef.Validate(); err != nil {
		return err
	}
	if d.Format == "" {
		return fmt.Errorf("artifact: dataset reference has no format")
	}
	if d.RowCount < 0 {
		return fmt.Errorf("artifact: dataset reference has negative row count %d", d.RowCount)
	}
	return nil
}

// MarshalManifest serializes a manifest, refusing to write one that is not
// valid. Writing a malformed manifest would turn a bug here into an
// unreadable artifact later, when the context needed to diagnose it is gone.
func MarshalManifest(m *Manifest) ([]byte, error) {
	if err := m.Validate(); err != nil {
		return nil, err
	}
	return json.Marshal(m)
}

// UnmarshalManifest parses and validates a manifest.
func UnmarshalManifest(data []byte) (*Manifest, error) {
	var m Manifest
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("artifact: decode manifest: %w", err)
	}
	if err := m.Validate(); err != nil {
		return nil, err
	}
	return &m, nil
}
