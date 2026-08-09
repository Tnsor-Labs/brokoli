package engine

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Tnsor-Labs/brokoli/pkg/artifact"
	"github.com/Tnsor-Labs/brokoli/pkg/common"
)

// Column order is not recoverable from NDJSON rows — the old read path
// rebuilt it by iterating a map. Recording it in the manifest is what makes
// a restored dataset identical to the one that was written, not merely
// equivalent.
func TestLocalDiskArtifactStore_PreservesColumnOrder(t *testing.T) {
	store := NewLocalDiskArtifactStore(t.TempDir())
	want := []string{"zeta", "alpha", "middle", "beta"}
	ds := &common.DataSet{
		Columns: want,
		Rows: []common.DataRow{
			{"zeta": 1, "alpha": 2, "middle": 3, "beta": 4},
			{"zeta": 5, "alpha": 6, "middle": 7, "beta": 8},
		},
	}
	if err := store.WriteArtifact("run-1", "node-1", ds); err != nil {
		t.Fatal(err)
	}
	got, err := store.ReadArtifact("run-1", "node-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Columns) != len(want) {
		t.Fatalf("columns = %v, want %v", got.Columns, want)
	}
	for i := range want {
		if got.Columns[i] != want[i] {
			t.Fatalf("column order not preserved:\n got %v\nwant %v", got.Columns, want)
		}
	}
	if len(got.Rows) != 2 {
		t.Errorf("rows = %d, want 2", len(got.Rows))
	}
}

// A run already in flight when the binary is upgraded must still resume, so
// artifacts in the pre-manifest layout stay readable.
func TestLocalDiskArtifactStore_ReadsLegacyLayout(t *testing.T) {
	base := t.TempDir()
	store := NewLocalDiskArtifactStore(base)

	// Write in the old shape: NDJSON rows straight to the per-node path,
	// with no manifest beside them.
	legacy := store.artifactPath("run-legacy", "node-1")
	if err := os.MkdirAll(filepath.Dir(legacy), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := WriteArrowJSON(legacy, &common.DataSet{
		Columns: []string{"id"},
		Rows:    []common.DataRow{{"id": 1}, {"id": 2}},
	}); err != nil {
		t.Fatal(err)
	}

	got, err := store.ReadArtifact("run-legacy", "node-1")
	if err != nil {
		t.Fatalf("a pre-manifest artifact should still be readable: %v", err)
	}
	if len(got.Rows) != 2 {
		t.Errorf("rows = %d, want 2", len(got.Rows))
	}
}

// An empty result is a real result. It must survive the round trip as an
// empty dataset rather than come back as "nothing was written" — the
// distinction Tnsor-Labs/brokoli#8 exists to protect.
func TestLocalDiskArtifactStore_EmptyDatasetIsNotAbsence(t *testing.T) {
	store := NewLocalDiskArtifactStore(t.TempDir())
	if err := store.WriteArtifact("run-1", "node-1", &common.DataSet{
		Columns: []string{"id"},
		Rows:    []common.DataRow{},
	}); err != nil {
		t.Fatal(err)
	}
	got, err := store.ReadArtifact("run-1", "node-1")
	if err != nil {
		t.Fatalf("an empty dataset should read back, got %v", err)
	}
	if got == nil || len(got.Rows) != 0 {
		t.Fatalf("got %+v, want an empty dataset", got)
	}
}

// Corruption must not be reported as absence. restoreSkippedNodeOutput
// treats ErrArtifactNotFound as a recoverable miss, so altered output has to
// take a different path — otherwise a tampered artifact would be quietly
// re-derived instead of failing.
func TestLocalDiskArtifactStore_CorruptionIsNotReportedAsMissing(t *testing.T) {
	base := t.TempDir()
	store := NewLocalDiskArtifactStore(base)
	if err := store.WriteArtifact("run-1", "node-1", &common.DataSet{
		Columns: []string{"id"},
		Rows:    []common.DataRow{{"id": 1}},
	}); err != nil {
		t.Fatal(err)
	}

	var blob string
	_ = filepath.Walk(base, func(p string, info os.FileInfo, err error) error {
		if err == nil && !info.IsDir() && strings.HasSuffix(p, ".blob") {
			blob = p
		}
		return nil
	})
	if blob == "" {
		t.Fatal("no blob was written")
	}
	if err := os.WriteFile(blob, []byte(`{"id":999}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	_, err := store.ReadArtifact("run-1", "node-1")
	if err == nil {
		t.Fatal("tampered artifact was read as if it were valid")
	}
	if errors.Is(err, ErrArtifactNotFound) {
		t.Error("corruption was reported as ErrArtifactNotFound, which resume treats as a recoverable miss")
	}
	if !errors.Is(err, artifact.ErrChecksumMismatch) {
		t.Errorf("want a checksum mismatch, got %v", err)
	}
}

// A manifest from a future build must fail loudly rather than be read with
// this build's assumptions about the fields.
func TestLocalDiskArtifactStore_RejectsUnknownManifestVersion(t *testing.T) {
	base := t.TempDir()
	store := NewLocalDiskArtifactStore(base)
	if err := store.WriteArtifact("run-1", "node-1", &common.DataSet{
		Columns: []string{"id"}, Rows: []common.DataRow{{"id": 1}},
	}); err != nil {
		t.Fatal(err)
	}

	path := store.manifestPath("run-1", "node-1")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	bumped := strings.Replace(string(data), `"version":1`, `"version":999`, 1)
	if bumped == string(data) {
		t.Fatalf("could not find the version field in %s", data)
	}
	if err := os.WriteFile(path, []byte(bumped), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := store.ReadArtifact("run-1", "node-1"); err == nil {
		t.Fatal("read a manifest written by a newer schema")
	}
}

// Purging a run has to reclaim the bytes, not just the manifests pointing at
// them — otherwise retention (Tnsor-Labs/brokoli#49) would free nothing.
func TestLocalDiskArtifactStore_DeleteRunArtifactsReclaimsBlobs(t *testing.T) {
	base := t.TempDir()
	store := NewLocalDiskArtifactStore(base)
	for _, node := range []string{"n1", "n2", "n3"} {
		if err := store.WriteArtifact("run-1", node, &common.DataSet{
			Columns: []string{"id"}, Rows: []common.DataRow{{"id": node}},
		}); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.WriteArtifact("run-2", "n1", &common.DataSet{
		Columns: []string{"id"}, Rows: []common.DataRow{{"id": "keep"}},
	}); err != nil {
		t.Fatal(err)
	}

	if err := store.DeleteRunArtifacts("run-1"); err != nil {
		t.Fatal(err)
	}

	var leftover []string
	_ = filepath.Walk(base, func(p string, info os.FileInfo, err error) error {
		if err == nil && !info.IsDir() {
			leftover = append(leftover, p)
		}
		return nil
	})
	// run-2's manifest and blob should be all that remains: two files.
	if len(leftover) != 2 {
		t.Errorf("after purging run-1, %d files remain (want run-2's manifest and blob only):\n  %s",
			len(leftover), strings.Join(leftover, "\n  "))
	}
	if _, err := store.ReadArtifact("run-2", "n1"); err != nil {
		t.Errorf("purging run-1 damaged run-2: %v", err)
	}
}

// Two nodes producing identical output within one run share a blob, but each
// keeps its own manifest — so removing one node's output cannot strand the
// other's.
func TestLocalDiskArtifactStore_IdenticalOutputsShareOneBlob(t *testing.T) {
	base := t.TempDir()
	store := NewLocalDiskArtifactStore(base)
	ds := &common.DataSet{Columns: []string{"id"}, Rows: []common.DataRow{{"id": 1}}}
	for _, node := range []string{"n1", "n2"} {
		if err := store.WriteArtifact("run-1", node, ds); err != nil {
			t.Fatal(err)
		}
	}

	blobs, manifests := 0, 0
	_ = filepath.Walk(base, func(p string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil
		}
		switch {
		case strings.HasSuffix(p, ".blob"):
			blobs++
		case strings.HasSuffix(p, ".manifest.json"):
			manifests++
		}
		return nil
	})
	if blobs != 1 {
		t.Errorf("identical output stored %d times, want 1", blobs)
	}
	if manifests != 2 {
		t.Errorf("%d manifests, want one per node", manifests)
	}
	for _, node := range []string{"n1", "n2"} {
		if _, err := store.ReadArtifact("run-1", node); err != nil {
			t.Errorf("node %s: %v", node, err)
		}
	}
}
