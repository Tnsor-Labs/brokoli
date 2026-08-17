package engine

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/Tnsor-Labs/brokoli/models"
	"github.com/Tnsor-Labs/brokoli/pkg/artifact"
	"github.com/Tnsor-Labs/brokoli/pkg/common"
)

// ArtifactStore persists a node's completed output durably, keyed by
// (run ID, node ID, instance key), so a resumed run can restore the REAL
// prior output of a node it is skipping instead of leaving it unpopulated —
// the empty dataset data-loss bug fixed by Tnsor-Labs/brokoli#8. See
// docs/adr/010-run-artifact-and-resume-semantics.md for the full resume
// policy this backs.
//
// instanceKey (added for ADR-017's output-reference delivery design, see
// that ADR's 2026-08-12 Update) identifies which physical instance of
// nodeID this artifact belongs to — a dynamic-expansion item, empty for
// today's whole-node case. Every existing caller passes "" and is
// unaffected; this is purely additive, the same key-widening move already
// made for store.ExecutionAttemptStore (#147) and models.WorkPoolJob (#79).
//
// This is intentionally a minimal contract — write the completed output of
// one node (or node instance), read it back by the same key — not a
// general blob/object-store abstraction. Provider selection (S3, GCS, a
// shared network volume, etc.) is explicitly out of scope for this issue;
// LocalDiskArtifactStore below is the only implementation today, and is
// enough to make resume correct on a single host or a shared volume mount.
type ArtifactStore interface {
	// WriteArtifact durably persists ds as the completed output of
	// (nodeID, instanceKey) within runID. Called once per successful,
	// non-dry-run node execution whose node type is resumable (see
	// nonResumableNodeTypes) and whose output is non-nil, and — since
	// ADR-017 — once per completed instance whose result was delivered by
	// a remote worker.
	WriteArtifact(runID, nodeID, instanceKey string, ds *common.DataSet) error

	// ReadArtifact retrieves a previously written artifact. Returns a
	// wrapped ErrArtifactNotFound if none was ever written for this
	// (runID, nodeID, instanceKey) key.
	ReadArtifact(runID, nodeID, instanceKey string) (*common.DataSet, error)

	// DeleteRunArtifacts removes every artifact written for runID, across
	// all of its nodes and instances — the retention/GC counterpart to
	// WriteArtifact, so purging old runs (see api.systemPurge) doesn't
	// leave their artifacts behind forever (Tnsor-Labs/brokoli#49). A
	// no-op, not an error, if nothing was ever written for runID.
	DeleteRunArtifacts(runID string) error
}

// ErrArtifactNotFound indicates no durable artifact exists for a given
// (runID, nodeID). Runner.restoreSkippedNodeOutput treats this the same as
// any other read failure — the point is only ever to distinguish "an
// artifact was found and restored" from "it wasn't", never to paper over a
// missing artifact with synthesized data.
var ErrArtifactNotFound = errors.New("artifact not found")

// nonResumableNodeTypes lists node types whose successful completion is not
// captured as a durable, resumable artifact even when the node happens to
// return a non-nil *common.DataSet. These nodes exist for an external side
// effect — sending a notification, running an external dbt project,
// migrating rows directly between two databases — rather than producing a
// dataset whose identity as "the same data" is meaningful to hand to a
// downstream node on resume. Explicitly excluding them from the artifact
// write path means a resume that needs their output always takes the loud
// "non-resumable" failure path in Runner.restoreSkippedNodeOutput instead
// of silently treating a coincidental pass-through result as reusable.
//
// Sink nodes (sink_file/sink_db/sink_api) are not listed here because they
// already return a nil output on success — they have no data for any
// downstream node to consume, so they are naturally covered by
// restoreSkippedNodeOutput's "no downstream dependents" allowance instead
// of needing an explicit type-based exclusion.
var nonResumableNodeTypes = map[models.NodeType]bool{
	models.NodeTypeNotify:  true,
	models.NodeTypeMigrate: true,
	models.NodeTypeDBT:     true,
}

// LocalDiskArtifactStore is a minimal, single-host artifact backend for
// resume: each node's completed output is stored durably and read back by
// the same (runID, nodeID) key.
//
// The bytes themselves are held by a pkg/artifact.Store — content-addressed,
// checksummed, and reachable by reference — while this type keeps the small
// keyed mapping from (runID, nodeID) to the manifest describing them. The
// two layers answer different questions: "what did node X of run Y produce"
// is a lookup, "here are some bytes, hold them and give me a reference" is
// storage, and only the second is what the wider artifact plane needs
// (Tnsor-Labs/brokoli#38, docs/adr/012-artifact-and-dataset-plane.md).
//
// Both layers share one base directory and one per-run subdirectory, so
// DeleteRunArtifacts stays a single removal that reclaims manifests and
// blobs together.
//
// The dataset encoding is still the newline-delimited JSON of
// arrow_transfer.go's EncodeArrowJSON/DecodeArrowJSON, unchanged.
type LocalDiskArtifactStore struct {
	baseDir string
	blobs   artifact.Store
}

// NewLocalDiskArtifactStore creates a store rooted at baseDir. The
// directory is created lazily on first write, not here.
func NewLocalDiskArtifactStore(baseDir string) *LocalDiskArtifactStore {
	return &LocalDiskArtifactStore{
		baseDir: baseDir,
		blobs:   artifact.NewLocalDiskStore(baseDir),
	}
}

// TransientBlobJanitor is an optional ArtifactStore capability
// (Tnsor-Labs/brokoli#215): reclaim a run's per-run blob scratch namespace
// once the run is terminal, WITHOUT touching persisted artifacts.
//
// Only implementations whose artifact persistence does not alias the blob
// namespace may offer it. SQLArtifactStore qualifies: its artifacts live
// in database rows, so the local-disk blobs under a run's namespace are
// pure transport — spill parking and ADR-019 reference-passing hand-off —
// and are dead weight the moment the run finalizes and its artifacts are
// persisted. LocalDiskArtifactStore deliberately does NOT implement this:
// its WriteArtifactRef is a zero-copy manifest over the SAME namespace,
// so deleting the namespace would destroy the artifacts the ADR-010
// resume contract depends on; there, blob lifetime is artifact lifetime
// (DeleteRunArtifacts, retention).
//
// Measured need: a 3-hour soak grew each worker's blob directory linearly
// at ~450MB/hour with nothing reclaiming it — node disk pressure was a
// matter of days on any stable fleet.
type TransientBlobJanitor interface {
	// DeleteTransientBlobs removes the run's blob scratch namespace. A
	// no-op, not an error, when nothing was written.
	DeleteTransientBlobs(runID string) error
}

// BlobStoreProvider is implemented by an ArtifactStore that can also hold
// arbitrary bytes by reference, not only keyed node output.
//
// It is an optional capability, discovered by type assertion, so that
// ArtifactStore's contract stays the narrow one resume needs and an
// implementation without a blob store underneath is still valid.
type BlobStoreProvider interface {
	Blobs() artifact.Store
}

// Blobs implements BlobStoreProvider, exposing the content-addressed store
// beneath this one so callers that need to hold bytes by reference — a
// source_api node fetching an artifact, for instance — can share the same
// per-run storage and the same lifetime, including DeleteRunArtifacts.
func (l *LocalDiskArtifactStore) Blobs() artifact.Store { return l.blobs }

// instanceArtifactKey combines nodeID and instanceKey into the single
// string manifestPath/artifactPath hash — deliberately producing the exact
// same value as bare nodeID when instanceKey is empty (the "\x00instance\x00"
// marker only appears for a non-empty instanceKey), so every artifact
// written before ADR-017 hashes identically after this change and stays
// readable. A run already in flight when a binary is upgraded must still
// resume — the same principle readLegacyArtifact below already exists for.
func instanceArtifactKey(nodeID, instanceKey string) string {
	if instanceKey == "" {
		return nodeID
	}
	return nodeID + "\x00instance\x00" + instanceKey
}

// manifestPath is where the reference describing (nodeID, instanceKey)'s
// output lives. It sits beside the blobs in the same per-run directory; the
// distinct suffix keeps the two apart.
func (l *LocalDiskArtifactStore) manifestPath(runID, nodeID, instanceKey string) string {
	return filepath.Join(l.runDir(runID), hex.EncodeToString(sha256Sum(instanceArtifactKey(nodeID, instanceKey)))+".manifest.json")
}

// artifactPath maps (runID, nodeID) to a filesystem path without ever
// interpolating either caller-controlled string directly into a path
// component. Node IDs in particular originate from pipeline JSON that end
// users — pipeline editors, imported or git-synced definitions — fully
// control; an adversarial node ID such as "../../../etc/cron.d/x" must not
// be able to escape baseDir. Hashing both components to fixed-width hex
// sidesteps path traversal, path separators, and null bytes in one step, at
// the cost of file names no longer being human-readable — an acceptable
// trade-off for an internal cache keyed by opaque IDs.
//
// No instanceKey parameter: this is only ever used by readLegacyArtifact,
// for the pre-manifest, pre-ADR-017 layout that predates instances
// entirely — see that method's own doc comment.
func (l *LocalDiskArtifactStore) artifactPath(runID, nodeID string) string {
	return filepath.Join(l.runDir(runID), hex.EncodeToString(sha256Sum(nodeID))+".ndjson")
}

// runDir is every artifact belonging to runID's parent directory — every
// (runID, nodeID) key for the same runID hashes into a file directly under
// this one directory, which is what makes DeleteRunArtifacts a single
// os.RemoveAll instead of needing to enumerate node IDs.
func (l *LocalDiskArtifactStore) runDir(runID string) string {
	return filepath.Join(l.baseDir, hex.EncodeToString(sha256Sum(runID)))
}

func sha256Sum(s string) []byte {
	sum := sha256.Sum256([]byte(s))
	return sum[:]
}

// WriteArtifact implements ArtifactStore.
//
// The rows are streamed into the blob store and a manifest recording the
// resulting reference is written beside them. Streaming rather than staging
// the encoded bytes in memory or in a second file is the point: the datasets
// worth persisting are the large ones.
func (l *LocalDiskArtifactStore) WriteArtifact(runID, nodeID, instanceKey string, ds *common.DataSet) error {
	if runID == "" || nodeID == "" {
		return fmt.Errorf("write artifact: runID and nodeID are required")
	}

	// io.Pipe so the encoder and the store run concurrently and neither
	// materializes the encoded form. A pipe write blocks until the store
	// reads, so this is bounded by the store's read buffer, not by the size
	// of the dataset.
	pr, pw := io.Pipe()
	go func() {
		// CloseWithError(nil) behaves as Close, so the reader sees a clean
		// EOF on success and the encoder's error otherwise — which is what
		// makes a failed encode surface as a failed Put rather than a
		// silently truncated artifact.
		pw.CloseWithError(EncodeArrowJSON(pw, ds))
	}()

	ref, err := l.blobs.Put(context.Background(), runID, pr, artifact.PutOptions{
		MediaType: artifact.MediaTypeNDJSON,
	})
	if err != nil {
		_ = pr.CloseWithError(err)
		return fmt.Errorf("write artifact: %w", err)
	}

	// Columns are recorded here because the NDJSON rows do not preserve
	// their order — DecodeArrowJSON would otherwise have to recover them by
	// iterating a map, which returns them in an arbitrary order.
	cols := []string{}
	rowCount := 0
	if ds != nil {
		if ds.Columns != nil {
			cols = ds.Columns
		}
		rowCount = len(ds.Rows)
	}
	return l.writeDatasetManifest(runID, nodeID, instanceKey, &artifact.DatasetRef{
		ArtifactRef: *ref,
		Format:      artifact.FormatNDJSON,
		Columns:     cols,
		RowCount:    int64(rowCount),
	})
}

// writeDatasetManifest records dsRef as the artifact for (runID, nodeID,
// instanceKey) — the shared tail of WriteArtifact and WriteArtifactRef.
func (l *LocalDiskArtifactStore) writeDatasetManifest(runID, nodeID, instanceKey string, dsRef *artifact.DatasetRef) error {
	manifest := artifact.NewDatasetManifest(dsRef)
	data, err := artifact.MarshalManifest(manifest)
	if err != nil {
		return fmt.Errorf("write artifact: %w", err)
	}

	path := l.manifestPath(runID, nodeID, instanceKey)
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return fmt.Errorf("create artifact directory: %w", err)
	}
	// Temp-and-rename so a reader never observes a partial manifest. The
	// nanosecond suffix keeps two overlapping writers for the same key —
	// not expected from Runner.executeNode's single write per node per run,
	// but not excluded by this store's contract either — from racing each
	// other's cleanup.
	tmp := fmt.Sprintf("%s.%d.tmp", path, time.Now().UnixNano())
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("write artifact manifest: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("finalize artifact manifest: %w", err)
	}
	return nil
}

// RefArtifactWriter is the optional capability (discovered by type
// assertion, like BlobStoreProvider above and every other optional
// capability in this codebase) an ArtifactStore may support to record an
// output that ALREADY lives in a blob store as a durable resume artifact,
// without materializing it (docs/adr/019-execution-segments-and-streaming.md,
// Milestone 1). A store without it gets the materialize-and-WriteArtifact
// fallback — exactly today's memory cost, only for those stores.
type RefArtifactWriter interface {
	WriteArtifactRef(runID, nodeID, instanceKey string, ref *artifact.DatasetRef) error
}

// FencedArtifactWriter is the optional capability an ArtifactStore may
// support to guard an instance-level artifact write against a write from
// an attempt that has already been superseded (ADR-017). Found live:
// HandleReportInstanceResult/executeInstanceJobContext write the artifact
// and only afterward settle the attempt through the fencing-checked store —
// so a late report from an attempt already superseded by a retry (worker
// stalled past the dispatcher's timeout, then finally delivers) reaches
// WriteArtifact with nothing to stop it silently overwriting the winning
// attempt's result. Only the attempt's status transition was ever
// fencing-protected; the payload was not.
//
// Ordering is (attempt, fencingGeneration) lexicographic, not
// fencingGeneration alone: store.ExecutionAttemptStore.ClaimAttempt's
// fencing_generation is a per-(run, node, instanceKey, attempt) row
// counter — it protects a single attempt against being double-claimed
// after its own lease expires, and restarts near 1 for every new attempt
// number, so attempt 0 and attempt 1 of the same instance both typically
// carry fencing_generation 1 and are not orderable by that value alone.
// attempt IS monotonic per instance (each retry is a strictly higher
// attempt number — see pagination_remote.go/expansion.go's own retry
// loops), so it is the primary key; fencingGeneration only breaks ties
// within the same attempt (a reclaim after a lease timeout).
//
// WriteArtifactFenced returns written=false, nil when (attempt,
// fencingGeneration) at least as high already owns this
// (runID, nodeID, instanceKey) key — the same "settle this race honestly
// rather than silently overwriting" rule FailAttempt/CompleteAttempt already
// apply, extended to cover the data those calls guard the identity of. A
// store without this capability falls back to plain WriteArtifact,
// unprotected — today's behavior, unchanged for callers with no attempt
// identity to check (whole-node writes, instanceKey == "").
type FencedArtifactWriter interface {
	WriteArtifactFenced(runID, nodeID, instanceKey string, ds *common.DataSet, attempt int, fencingGeneration int64) (written bool, err error)
}

// WriteArtifactRef implements RefArtifactWriter. The runner's spill blobs
// and this store's artifact blobs are the same content-addressed store
// under the same per-run namespace (see Blobs and Runner.newOutputs), so
// recording a streamed output as a resume artifact is a manifest write
// pointing at a blob that is already durably in place — no bytes move.
func (l *LocalDiskArtifactStore) WriteArtifactRef(runID, nodeID, instanceKey string, ref *artifact.DatasetRef) error {
	if runID == "" || nodeID == "" {
		return fmt.Errorf("write artifact ref: runID and nodeID are required")
	}
	if ref == nil {
		return fmt.Errorf("write artifact ref: nil ref")
	}
	return l.writeDatasetManifest(runID, nodeID, instanceKey, ref)
}

// ReadArtifact implements ArtifactStore.
func (l *LocalDiskArtifactStore) ReadArtifact(runID, nodeID, instanceKey string) (*common.DataSet, error) {
	if runID == "" || nodeID == "" {
		return nil, fmt.Errorf("read artifact: runID and nodeID are required")
	}

	data, err := os.ReadFile(l.manifestPath(runID, nodeID, instanceKey)) // #nosec G304 -- path is baseDir joined with a sha256 hex digest; no caller string reaches it, see manifestPath/instanceArtifactKey.
	if err != nil {
		if os.IsNotExist(err) {
			if instanceKey != "" {
				// The pre-manifest legacy layout predates ADR-017's
				// instance concept entirely — there is no legacy file an
				// instance-keyed lookup could ever fall back to.
				return nil, fmt.Errorf("%w: run=%s node=%s instance=%s", ErrArtifactNotFound, runID, nodeID, instanceKey)
			}
			// No manifest: either nothing was written, or this artifact
			// predates manifests. Runs already in flight when a binary is
			// upgraded must still resume, so the older layout is read
			// rather than reported as missing.
			return l.readLegacyArtifact(runID, nodeID)
		}
		return nil, fmt.Errorf("read artifact manifest: %w", err)
	}

	manifest, err := artifact.UnmarshalManifest(data)
	if err != nil {
		return nil, fmt.Errorf("read artifact: %w", err)
	}
	if manifest.Kind != artifact.KindDataset {
		return nil, fmt.Errorf("read artifact: run=%s node=%s holds a %s, not a dataset", runID, nodeID, manifest.Kind)
	}
	ref := manifest.Dataset
	if ref.Format != artifact.FormatNDJSON {
		return nil, fmt.Errorf("read artifact: unsupported dataset format %q", ref.Format)
	}

	rc, err := l.blobs.Open(context.Background(), &ref.ArtifactRef)
	if err != nil {
		if errors.Is(err, artifact.ErrNotFound) {
			return nil, fmt.Errorf("%w: run=%s node=%s", ErrArtifactNotFound, runID, nodeID)
		}
		// A checksum mismatch is deliberately not folded into
		// ErrArtifactNotFound: restoreSkippedNodeOutput treats "not found"
		// as a recoverable miss, and corrupted output must not take that
		// path — it has to fail loudly rather than be re-derived as absent.
		return nil, fmt.Errorf("read artifact: %w", err)
	}
	defer rc.Close()

	ds, err := DecodeArrowJSON(rc, ref.Columns)
	if err != nil {
		return nil, fmt.Errorf("read artifact: %w", err)
	}
	return ds, nil
}

// readLegacyArtifact reads the pre-manifest layout: the NDJSON rows written
// directly to a per-node file. Kept so a run that started under an older
// binary can still be resumed by a newer one.
func (l *LocalDiskArtifactStore) readLegacyArtifact(runID, nodeID string) (*common.DataSet, error) {
	path := l.artifactPath(runID, nodeID)
	if _, err := os.Stat(path); err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("%w: run=%s node=%s", ErrArtifactNotFound, runID, nodeID)
		}
		return nil, fmt.Errorf("stat artifact: %w", err)
	}
	ds, err := ReadArrowJSON(path)
	if err != nil {
		return nil, fmt.Errorf("read artifact: %w", err)
	}
	return ds, nil
}

// DeleteRunArtifacts implements ArtifactStore. Every artifact for runID
// lives under the same runDir(runID) regardless of node ID, so this is one
// directory removal rather than needing to know which nodes ran.
func (l *LocalDiskArtifactStore) DeleteRunArtifacts(runID string) error {
	if runID == "" {
		return fmt.Errorf("delete run artifacts: runID is required")
	}
	if err := os.RemoveAll(l.runDir(runID)); err != nil {
		return fmt.Errorf("delete run artifacts: %w", err)
	}
	return nil
}
