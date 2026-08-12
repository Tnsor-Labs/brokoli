package engine

import (
	"encoding/hex"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Tnsor-Labs/brokoli/pkg/common"
)

func TestLocalDiskArtifactStore_WriteReadRoundTrip(t *testing.T) {
	store := NewLocalDiskArtifactStore(t.TempDir())
	ds := &common.DataSet{
		Columns: []string{"id", "name"},
		Rows: []common.DataRow{
			{"id": float64(1), "name": "brokoli"},
			{"id": float64(2), "name": "sql"},
		},
	}

	if err := store.WriteArtifact("run-1", "node-a", "", ds); err != nil {
		t.Fatalf("write artifact: %v", err)
	}

	got, err := store.ReadArtifact("run-1", "node-a", "")
	if err != nil {
		t.Fatalf("read artifact: %v", err)
	}
	if len(got.Rows) != 2 {
		t.Fatalf("got %d rows, want 2", len(got.Rows))
	}
	if got.Rows[0]["name"] != "brokoli" || got.Rows[1]["name"] != "sql" {
		t.Fatalf("round-tripped rows do not match: %+v", got.Rows)
	}
}

func TestLocalDiskArtifactStore_ReadMissingReturnsErrArtifactNotFound(t *testing.T) {
	store := NewLocalDiskArtifactStore(t.TempDir())
	_, err := store.ReadArtifact("run-missing", "node-missing", "")
	if err == nil {
		t.Fatal("expected an error for a missing artifact")
	}
	if !errors.Is(err, ErrArtifactNotFound) {
		t.Fatalf("error = %v, want it to wrap ErrArtifactNotFound", err)
	}
}

func TestLocalDiskArtifactStore_DifferentKeysDoNotCollide(t *testing.T) {
	store := NewLocalDiskArtifactStore(t.TempDir())
	dsA := &common.DataSet{Columns: []string{"v"}, Rows: []common.DataRow{{"v": "a"}}}
	dsB := &common.DataSet{Columns: []string{"v"}, Rows: []common.DataRow{{"v": "b"}}}

	if err := store.WriteArtifact("run-1", "node-a", "", dsA); err != nil {
		t.Fatalf("write A: %v", err)
	}
	if err := store.WriteArtifact("run-1", "node-b", "", dsB); err != nil {
		t.Fatalf("write B: %v", err)
	}
	if err := store.WriteArtifact("run-2", "node-a", "", dsB); err != nil {
		t.Fatalf("write run-2/node-a: %v", err)
	}

	gotA, err := store.ReadArtifact("run-1", "node-a", "")
	if err != nil {
		t.Fatalf("read A: %v", err)
	}
	if gotA.Rows[0]["v"] != "a" {
		t.Fatalf("run-1/node-a = %v, want v=a (run_id:node_id key collision)", gotA.Rows[0])
	}

	gotRun2 := mustReadArtifact(t, store, "run-2", "node-a")
	if gotRun2.Rows[0]["v"] != "b" {
		t.Fatalf("run-2/node-a = %v, want v=b — same node ID under a different run must not collide", gotRun2.Rows[0])
	}
}

// TestLocalDiskArtifactStore_EmptyInstanceKeyPathUnchanged proves
// ADR-017's instanceKey dimension is byte-for-byte backward compatible: an
// artifact written with instanceKey="" — every artifact written before
// this dimension existed — lands at exactly the path a bare-nodeID hash
// (the pre-ADR-017 formula) would produce, so every artifact a production
// deployment already has stays readable across the upgrade.
func TestLocalDiskArtifactStore_EmptyInstanceKeyPathUnchanged(t *testing.T) {
	store := NewLocalDiskArtifactStore(t.TempDir())
	want := filepath.Join(store.runDir("run-1"), hex.EncodeToString(sha256Sum("node-a"))+".manifest.json")
	got := store.manifestPath("run-1", "node-a", "")
	if got != want {
		t.Fatalf("manifestPath with empty instanceKey = %q, want %q (must match the pre-ADR-017 bare-nodeID hash exactly)", got, want)
	}
}

// TestLocalDiskArtifactStore_InstanceKeysDoNotCollideWithWholeNodeOrEachOther
// proves a dynamic-expansion item's artifact (a non-empty instanceKey) is
// independent both from the same node's whole-node artifact (empty
// instanceKey) and from a sibling item's artifact — the entire point of
// widening this store's key for ADR-017's output-reference delivery design.
func TestLocalDiskArtifactStore_InstanceKeysDoNotCollideWithWholeNodeOrEachOther(t *testing.T) {
	store := NewLocalDiskArtifactStore(t.TempDir())
	dsWhole := &common.DataSet{Columns: []string{"v"}, Rows: []common.DataRow{{"v": "whole"}}}
	dsItem0 := &common.DataSet{Columns: []string{"v"}, Rows: []common.DataRow{{"v": "item-0"}}}
	dsItem1 := &common.DataSet{Columns: []string{"v"}, Rows: []common.DataRow{{"v": "item-1"}}}

	if err := store.WriteArtifact("run-1", "parse", "", dsWhole); err != nil {
		t.Fatalf("write whole-node: %v", err)
	}
	if err := store.WriteArtifact("run-1", "parse", "idx:0", dsItem0); err != nil {
		t.Fatalf("write idx:0: %v", err)
	}
	if err := store.WriteArtifact("run-1", "parse", "idx:1", dsItem1); err != nil {
		t.Fatalf("write idx:1: %v", err)
	}

	gotWhole, err := store.ReadArtifact("run-1", "parse", "")
	if err != nil {
		t.Fatalf("read whole-node: %v", err)
	}
	if gotWhole.Rows[0]["v"] != "whole" {
		t.Fatalf("whole-node artifact = %v, want v=whole — an instance write must not overwrite it", gotWhole.Rows[0])
	}

	gotItem0, err := store.ReadArtifact("run-1", "parse", "idx:0")
	if err != nil {
		t.Fatalf("read idx:0: %v", err)
	}
	if gotItem0.Rows[0]["v"] != "item-0" {
		t.Fatalf("idx:0 artifact = %v, want v=item-0", gotItem0.Rows[0])
	}

	gotItem1, err := store.ReadArtifact("run-1", "parse", "idx:1")
	if err != nil {
		t.Fatalf("read idx:1: %v", err)
	}
	if gotItem1.Rows[0]["v"] != "item-1" {
		t.Fatalf("idx:1 artifact = %v, want v=item-1 — must not collide with idx:0", gotItem1.Rows[0])
	}
}

func mustReadArtifact(t *testing.T, s *LocalDiskArtifactStore, runID, nodeID string) *common.DataSet {
	t.Helper()
	ds, err := s.ReadArtifact(runID, nodeID, "")
	if err != nil {
		t.Fatalf("read artifact %s/%s: %v", runID, nodeID, err)
	}
	return ds
}

// TestLocalDiskArtifactStore_PathTraversalKeysAreContained is the security
// regression test for the concern flagged in the artifact store's design:
// node IDs and run IDs come from data end users fully control (pipeline
// JSON, imported/git-synced definitions), so a malicious key must never be
// able to write or read outside baseDir. artifactPath hashes both
// components instead of interpolating them into the path, so this asserts
// that property holds for classic traversal payloads.
func TestLocalDiskArtifactStore_PathTraversalKeysAreContained(t *testing.T) {
	base := t.TempDir()
	store := NewLocalDiskArtifactStore(base)

	maliciousIDs := []string{
		"../../../etc/passwd",
		"../../outside",
		"..\\..\\windows\\system32",
		"/etc/passwd",
		"a/../../b",
		"....//....//etc",
		string([]byte{0}) + "nullbyte",
	}

	ds := &common.DataSet{Columns: []string{"v"}, Rows: []common.DataRow{{"v": "x"}}}

	for _, id := range maliciousIDs {
		t.Run(id, func(t *testing.T) {
			if err := store.WriteArtifact(id, "node", "", ds); err != nil {
				t.Fatalf("write with malicious runID %q: %v", id, err)
			}
			if err := store.WriteArtifact("run", id, "", ds); err != nil {
				t.Fatalf("write with malicious nodeID %q: %v", id, err)
			}

			path1 := store.artifactPath(id, "node")
			path2 := store.artifactPath("run", id)
			for _, p := range []string{path1, path2} {
				abs, err := filepath.Abs(p)
				if err != nil {
					t.Fatalf("abs path: %v", err)
				}
				absBase, err := filepath.Abs(base)
				if err != nil {
					t.Fatalf("abs base: %v", err)
				}
				if !strings.HasPrefix(abs, absBase+string(filepath.Separator)) {
					t.Fatalf("artifact path %q escapes baseDir %q for malicious key %q", abs, absBase, id)
				}
			}

			// Round-trips correctly despite the hostile key.
			got, err := store.ReadArtifact(id, "node", "")
			if err != nil {
				t.Fatalf("read back with malicious runID %q: %v", id, err)
			}
			if got.Rows[0]["v"] != "x" {
				t.Fatalf("round trip with malicious runID %q produced wrong data: %+v", id, got.Rows)
			}
		})
	}
}

func TestLocalDiskArtifactStore_EmptyKeysRejected(t *testing.T) {
	store := NewLocalDiskArtifactStore(t.TempDir())
	ds := &common.DataSet{Columns: []string{"v"}, Rows: []common.DataRow{{"v": "x"}}}

	if err := store.WriteArtifact("", "node", "", ds); err == nil {
		t.Fatal("expected error for empty runID")
	}
	if err := store.WriteArtifact("run", "", "", ds); err == nil {
		t.Fatal("expected error for empty nodeID")
	}
	if _, err := store.ReadArtifact("", "node", ""); err == nil {
		t.Fatal("expected error reading with empty runID")
	}
}

func TestLocalDiskArtifactStore_DeleteRunArtifacts_RemovesAllNodesForRun(t *testing.T) {
	store := NewLocalDiskArtifactStore(t.TempDir())
	ds := &common.DataSet{Columns: []string{"v"}, Rows: []common.DataRow{{"v": "x"}}}

	if err := store.WriteArtifact("run-1", "node-a", "", ds); err != nil {
		t.Fatalf("write run-1/node-a: %v", err)
	}
	if err := store.WriteArtifact("run-1", "node-b", "", ds); err != nil {
		t.Fatalf("write run-1/node-b: %v", err)
	}
	if err := store.WriteArtifact("run-2", "node-a", "", ds); err != nil {
		t.Fatalf("write run-2/node-a: %v", err)
	}

	if err := store.DeleteRunArtifacts("run-1"); err != nil {
		t.Fatalf("delete run-1 artifacts: %v", err)
	}

	if _, err := store.ReadArtifact("run-1", "node-a", ""); !errors.Is(err, ErrArtifactNotFound) {
		t.Errorf("run-1/node-a: got %v, want ErrArtifactNotFound", err)
	}
	if _, err := store.ReadArtifact("run-1", "node-b", ""); !errors.Is(err, ErrArtifactNotFound) {
		t.Errorf("run-1/node-b: got %v, want ErrArtifactNotFound", err)
	}
	// A different run's artifact must survive untouched.
	if _, err := store.ReadArtifact("run-2", "node-a", ""); err != nil {
		t.Errorf("run-2/node-a should be unaffected by deleting run-1, got: %v", err)
	}
}

func TestLocalDiskArtifactStore_DeleteRunArtifacts_MissingRunIsNotAnError(t *testing.T) {
	store := NewLocalDiskArtifactStore(t.TempDir())
	if err := store.DeleteRunArtifacts("run-never-existed"); err != nil {
		t.Fatalf("deleting artifacts for a run that never wrote any should be a no-op, got: %v", err)
	}
}
