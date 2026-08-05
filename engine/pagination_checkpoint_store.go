package engine

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/Tnsor-Labs/brokoli/pkg/common"
	"github.com/Tnsor-Labs/brokoli/pkg/fetchers"
)

// PaginationCheckpointStore persists mid-pagination progress for a
// source_api node — the position fetchers.PaginationCheckpoint describes,
// plus the records accumulated so far — so a node-level retry or a
// ResumeRun after a crash can continue a large paginated fetch instead of
// restarting it from page one. See
// docs/adr/010-run-artifact-and-resume-semantics.md and issue #41 M2.
//
// This is deliberately a much narrower contract than ArtifactStore: a
// checkpoint is interim, in-progress state for one still-running node
// attempt, not a completed node's durable output. It is written repeatedly
// during a single fetch (every checkpoint_every pages) and deleted the
// moment that fetch succeeds — ArtifactStore's WriteArtifact takes over
// from there, exactly as it does for a node with no pagination checkpoint
// at all.
type PaginationCheckpointStore interface {
	// SaveCheckpoint durably persists checkpoint and the records
	// accumulated so far, keyed by (runID, nodeID). Called repeatedly
	// during one fetch — each call overwrites the previous checkpoint for
	// the same key, it does not append.
	SaveCheckpoint(runID, nodeID string, checkpoint fetchers.PaginationCheckpoint, recordsSoFar *common.DataSet) error

	// LoadCheckpoint retrieves the most recently saved checkpoint for
	// (runID, nodeID). Returns a wrapped ErrCheckpointNotFound if none
	// exists — the ordinary case for a node's first attempt.
	LoadCheckpoint(runID, nodeID string) (*fetchers.PaginationCheckpoint, *common.DataSet, error)

	// DeleteCheckpoint removes any checkpoint for (runID, nodeID). Called
	// once a paginated fetch completes successfully; a no-op (not an
	// error) if none exists.
	DeleteCheckpoint(runID, nodeID string) error

	// DeleteRunCheckpoints removes every checkpoint for runID, across all
	// of its nodes — the retention/GC counterpart, so purging old runs
	// (see api.systemPurge) doesn't leave stale interim checkpoint state
	// behind forever (Tnsor-Labs/brokoli#49). A no-op, not an error, if
	// nothing was ever checkpointed for runID.
	DeleteRunCheckpoints(runID string) error
}

// ErrCheckpointNotFound indicates no pagination checkpoint exists for a
// given (runID, nodeID) — the ordinary case for a node's first attempt, or
// after DeleteCheckpoint. Callers treat it the same way
// Runner.restoreSkippedNodeOutput treats ErrArtifactNotFound: "start fresh,"
// not an error to surface.
var ErrCheckpointNotFound = errors.New("pagination checkpoint not found")

// LocalDiskPaginationCheckpointStore is a minimal, single-host checkpoint
// backend: each checkpoint is one small JSON position file plus one NDJSON
// records file, both on local disk. Mirrors LocalDiskArtifactStore's
// hash-the-key / write-to-temp-then-rename pattern for the same reasons —
// see that type's doc comments.
type LocalDiskPaginationCheckpointStore struct {
	baseDir string
}

// NewLocalDiskPaginationCheckpointStore creates a store rooted at baseDir.
// The directory is created lazily on first write, not here.
func NewLocalDiskPaginationCheckpointStore(baseDir string) *LocalDiskPaginationCheckpointStore {
	return &LocalDiskPaginationCheckpointStore{baseDir: baseDir}
}

// checkpointPaths maps (runID, nodeID) to the position/records file pair,
// hashing both components exactly like LocalDiskArtifactStore.artifactPath
// — node IDs originate from pipeline JSON end users fully control, so this
// closes path traversal at the type level rather than relying on
// validation.
func (l *LocalDiskPaginationCheckpointStore) checkpointPaths(runID, nodeID string) (posPath, recordsPath string) {
	base := filepath.Join(l.runDir(runID), hex.EncodeToString(sha256Sum(nodeID)))
	return base + ".checkpoint.json", base + ".checkpoint.ndjson"
}

// runDir is every checkpoint belonging to runID's parent directory — same
// role as LocalDiskArtifactStore.runDir, and what makes DeleteRunCheckpoints
// a single os.RemoveAll instead of needing to enumerate node IDs.
func (l *LocalDiskPaginationCheckpointStore) runDir(runID string) string {
	return filepath.Join(l.baseDir, hex.EncodeToString(sha256Sum(runID)))
}

// SaveCheckpoint implements PaginationCheckpointStore.
func (l *LocalDiskPaginationCheckpointStore) SaveCheckpoint(runID, nodeID string, checkpoint fetchers.PaginationCheckpoint, recordsSoFar *common.DataSet) error {
	if runID == "" || nodeID == "" {
		return fmt.Errorf("save checkpoint: runID and nodeID are required")
	}
	posPath, recordsPath := l.checkpointPaths(runID, nodeID)
	// 0o750: a checkpoint's records file may contain a node's full
	// in-progress dataset, same sensitivity as an ArtifactStore artifact.
	if err := os.MkdirAll(filepath.Dir(posPath), 0o750); err != nil {
		return fmt.Errorf("create checkpoint directory: %w", err)
	}

	posBytes, err := json.Marshal(checkpoint)
	if err != nil {
		return fmt.Errorf("encode checkpoint position: %w", err)
	}
	posTmp := fmt.Sprintf("%s.%d.tmp", posPath, time.Now().UnixNano())
	if err := os.WriteFile(posTmp, posBytes, 0o600); err != nil {
		_ = os.Remove(posTmp)
		return fmt.Errorf("write checkpoint position: %w", err)
	}

	recordsTmp := fmt.Sprintf("%s.%d.tmp", recordsPath, time.Now().UnixNano())
	if err := WriteArrowJSON(recordsTmp, recordsSoFar); err != nil {
		_ = os.Remove(posTmp)
		_ = os.Remove(recordsTmp)
		return fmt.Errorf("write checkpoint records: %w", err)
	}

	// Rename records first, then position — a reader that sees a fresh
	// position file always finds a matching (or newer) records file
	// alongside it, never a stale one from a prior checkpoint.
	if err := os.Rename(recordsTmp, recordsPath); err != nil {
		_ = os.Remove(posTmp)
		_ = os.Remove(recordsTmp)
		return fmt.Errorf("finalize checkpoint records: %w", err)
	}
	if err := os.Rename(posTmp, posPath); err != nil {
		_ = os.Remove(posTmp)
		return fmt.Errorf("finalize checkpoint position: %w", err)
	}
	return nil
}

// LoadCheckpoint implements PaginationCheckpointStore.
func (l *LocalDiskPaginationCheckpointStore) LoadCheckpoint(runID, nodeID string) (*fetchers.PaginationCheckpoint, *common.DataSet, error) {
	if runID == "" || nodeID == "" {
		return nil, nil, fmt.Errorf("load checkpoint: runID and nodeID are required")
	}
	posPath, recordsPath := l.checkpointPaths(runID, nodeID)

	// posPath is derived from checkpointPaths, which hashes both key
	// components (see its doc comment) — never an attacker-controlled path.
	// #nosec G304
	posBytes, err := os.ReadFile(posPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil, fmt.Errorf("%w: run=%s node=%s", ErrCheckpointNotFound, runID, nodeID)
		}
		return nil, nil, fmt.Errorf("read checkpoint position: %w", err)
	}
	var checkpoint fetchers.PaginationCheckpoint
	if err := json.Unmarshal(posBytes, &checkpoint); err != nil {
		return nil, nil, fmt.Errorf("decode checkpoint position: %w", err)
	}

	ds, err := ReadArrowJSON(recordsPath)
	if err != nil {
		return nil, nil, fmt.Errorf("read checkpoint records: %w", err)
	}
	return &checkpoint, ds, nil
}

// DeleteCheckpoint implements PaginationCheckpointStore.
func (l *LocalDiskPaginationCheckpointStore) DeleteCheckpoint(runID, nodeID string) error {
	if runID == "" || nodeID == "" {
		return fmt.Errorf("delete checkpoint: runID and nodeID are required")
	}
	posPath, recordsPath := l.checkpointPaths(runID, nodeID)
	if err := os.Remove(posPath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("delete checkpoint position: %w", err)
	}
	if err := os.Remove(recordsPath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("delete checkpoint records: %w", err)
	}
	return nil
}

// DeleteRunCheckpoints implements PaginationCheckpointStore. Every
// checkpoint for runID lives under the same runDir(runID) regardless of
// node ID, so this is one directory removal rather than needing to know
// which nodes were checkpointed.
func (l *LocalDiskPaginationCheckpointStore) DeleteRunCheckpoints(runID string) error {
	if runID == "" {
		return fmt.Errorf("delete run checkpoints: runID is required")
	}
	if err := os.RemoveAll(l.runDir(runID)); err != nil {
		return fmt.Errorf("delete run checkpoints: %w", err)
	}
	return nil
}
