package engine

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/Tnsor-Labs/brokoli/models"
	"github.com/Tnsor-Labs/brokoli/pkg/common"
)

// ArtifactStore persists a node's completed output durably, keyed by
// (run ID, node ID), so a resumed run can restore the REAL prior output of
// a node it is skipping instead of leaving it unpopulated — the empty
// dataset data-loss bug fixed by Tnsor-Labs/brokoli#8. See
// docs/adr/010-run-artifact-and-resume-semantics.md for the full resume
// policy this backs.
//
// This is intentionally a minimal contract — write the completed output of
// one node, read it back by the same key — not a general blob/object-store
// abstraction. Provider selection (S3, GCS, a shared network volume, etc.)
// is explicitly out of scope for this issue; LocalDiskArtifactStore below
// is the only implementation today, and is enough to make resume correct
// on a single host or a shared volume mount.
type ArtifactStore interface {
	// WriteArtifact durably persists ds as the completed output of nodeID
	// within runID. Called once per successful, non-dry-run node execution
	// whose node type is resumable (see nonResumableNodeTypes) and whose
	// output is non-nil.
	WriteArtifact(runID, nodeID string, ds *common.DataSet) error

	// ReadArtifact retrieves a previously written artifact. Returns a
	// wrapped ErrArtifactNotFound if none was ever written for this
	// (runID, nodeID) pair.
	ReadArtifact(runID, nodeID string) (*common.DataSet, error)

	// DeleteRunArtifacts removes every artifact written for runID, across
	// all of its nodes — the retention/GC counterpart to WriteArtifact, so
	// purging old runs (see api.systemPurge) doesn't leave their artifacts
	// behind forever (Tnsor-Labs/brokoli#49). A no-op, not an error, if
	// nothing was ever written for runID.
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

// LocalDiskArtifactStore is a minimal, single-host artifact backend: each
// artifact is one NDJSON file on local disk, reusing the same
// newline-delimited-JSON format as arrow_transfer.go's WriteArrowJSON /
// ReadArrowJSON — the closest existing precedent in this codebase for
// durable inter-node data transfer (used today by the code-node subprocess
// bridge). A pluggable, object-store-backed implementation is future work;
// this is enough to make resume correct for a single-process deployment or
// one sharing a network volume across workers.
type LocalDiskArtifactStore struct {
	baseDir string
}

// NewLocalDiskArtifactStore creates a store rooted at baseDir. The
// directory is created lazily on first write, not here.
func NewLocalDiskArtifactStore(baseDir string) *LocalDiskArtifactStore {
	return &LocalDiskArtifactStore{baseDir: baseDir}
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
func (l *LocalDiskArtifactStore) WriteArtifact(runID, nodeID string, ds *common.DataSet) error {
	if runID == "" || nodeID == "" {
		return fmt.Errorf("write artifact: runID and nodeID are required")
	}
	path := l.artifactPath(runID, nodeID)
	// 0o750: artifacts may contain a node's full dataset (potentially
	// sensitive row data), so the directory is not group/world-readable.
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return fmt.Errorf("create artifact directory: %w", err)
	}
	// Write to a temp file and rename into place so a concurrent reader (or
	// a crash mid-write) never observes a partially written artifact. The
	// temp name includes a nanosecond suffix (not just path+".tmp") so two
	// overlapping writers for the same (runID, nodeID) — not expected from
	// today's single caller in Runner.executeNode, which only writes a
	// given node's artifact once per run after that node's single
	// successful attempt, but not guaranteed by this store's contract
	// either — can't have one's os.Remove(tmp) or os.Rename race the
	// other's in-flight temp file.
	tmp := fmt.Sprintf("%s.%d.tmp", path, time.Now().UnixNano())
	if err := WriteArrowJSON(tmp, ds); err != nil {
		_ = os.Remove(tmp) // best-effort cleanup of the partial temp file
		return fmt.Errorf("write artifact: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp) // best-effort cleanup of the partial temp file
		return fmt.Errorf("finalize artifact: %w", err)
	}
	return nil
}

// ReadArtifact implements ArtifactStore.
func (l *LocalDiskArtifactStore) ReadArtifact(runID, nodeID string) (*common.DataSet, error) {
	if runID == "" || nodeID == "" {
		return nil, fmt.Errorf("read artifact: runID and nodeID are required")
	}
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
