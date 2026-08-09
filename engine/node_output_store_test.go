package engine

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"

	"github.com/Tnsor-Labs/brokoli/models"
	"github.com/Tnsor-Labs/brokoli/pkg/artifact"
	"github.com/Tnsor-Labs/brokoli/pkg/common"
)

func rowsOfSize(n int, cellBytes int) []common.DataRow {
	cell := strings.Repeat("x", cellBytes)
	rows := make([]common.DataRow, n)
	for i := range rows {
		rows[i] = common.DataRow{"id": i, "payload": cell}
	}
	return rows
}

// Below the threshold nothing changes: the dataset stays in memory and the
// very same value comes back out.
func TestNodeOutputs_SmallOutputStaysInline(t *testing.T) {
	store := artifact.NewLocalDiskStore(t.TempDir())
	out := newNodeOutputs(store, "run-1", 1<<20)

	ds := &common.DataSet{Columns: []string{"id", "payload"}, Rows: rowsOfSize(10, 8)}
	if err := out.Put("n1", ds); err != nil {
		t.Fatal(err)
	}
	if out.spilledCount() != 0 {
		t.Error("a small output was spilled")
	}
	got, ok, err := out.Get("n1")
	if err != nil || !ok {
		t.Fatalf("Get: ok=%v err=%v", ok, err)
	}
	if got != ds {
		t.Error("an inline output should come back as the same value, not a copy")
	}
}

// Above the threshold the output leaves memory and comes back byte-identical.
func TestNodeOutputs_LargeOutputSpillsAndRestores(t *testing.T) {
	store := artifact.NewLocalDiskStore(t.TempDir())
	out := newNodeOutputs(store, "run-1", 4096)

	ds := &common.DataSet{Columns: []string{"id", "payload"}, Rows: rowsOfSize(500, 64)}
	if err := out.Put("n1", ds); err != nil {
		t.Fatal(err)
	}
	if out.spilledCount() != 1 {
		t.Fatalf("spilled %d outputs, want 1", out.spilledCount())
	}

	got, ok, err := out.Get("n1")
	if err != nil || !ok {
		t.Fatalf("Get: ok=%v err=%v", ok, err)
	}
	if len(got.Rows) != len(ds.Rows) {
		t.Fatalf("rows = %d, want %d", len(got.Rows), len(ds.Rows))
	}
	// Column order survives the trip, because the reference records it.
	if strings.Join(got.Columns, ",") != "id,payload" {
		t.Errorf("columns = %v, want [id payload]", got.Columns)
	}
	if got.Rows[0]["payload"] != ds.Rows[0]["payload"] {
		t.Error("payload changed across the spill")
	}
	if got.Rows[499]["payload"] != ds.Rows[499]["payload"] {
		t.Error("last row changed across the spill")
	}
}

// A fan-out reads the same spilled output more than once, and every consumer
// must see the full dataset.
func TestNodeOutputs_SpilledOutputReadableRepeatedly(t *testing.T) {
	store := artifact.NewLocalDiskStore(t.TempDir())
	out := newNodeOutputs(store, "run-1", 1024)
	ds := &common.DataSet{Columns: []string{"id", "payload"}, Rows: rowsOfSize(200, 32)}
	if err := out.Put("n1", ds); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		got, ok, err := out.Get("n1")
		if err != nil || !ok {
			t.Fatalf("read %d: ok=%v err=%v", i, ok, err)
		}
		if len(got.Rows) != 200 {
			t.Fatalf("read %d: rows = %d, want 200", i, len(got.Rows))
		}
	}
}

// With no store, or a threshold that disables spilling, behaviour is exactly
// what it was before this existed.
func TestNodeOutputs_SpillDisabled(t *testing.T) {
	big := &common.DataSet{Columns: []string{"id"}, Rows: rowsOfSize(1000, 128)}

	cases := map[string]*nodeOutputs{
		"no store":           newNodeOutputs(nil, "run-1", 1024),
		"no namespace":       newNodeOutputs(artifact.NewLocalDiskStore(t.TempDir()), "", 1024),
		"threshold zero":     newNodeOutputs(artifact.NewLocalDiskStore(t.TempDir()), "run-1", 0),
		"threshold negative": newNodeOutputs(artifact.NewLocalDiskStore(t.TempDir()), "run-1", -1),
	}
	for name, out := range cases {
		t.Run(name, func(t *testing.T) {
			if err := out.Put("n1", big); err != nil {
				t.Fatal(err)
			}
			if out.spilledCount() != 0 {
				t.Error("spilled despite spilling being disabled")
			}
			got, ok, _ := out.Get("n1")
			if !ok || got != big {
				t.Error("output should have stayed in memory unchanged")
			}
		})
	}
}

// A store that cannot accept the spill must not fail the pipeline: the
// dataset is already correct and in hand, so it stays in memory and the
// caller is told.
func TestNodeOutputs_SpillFailureKeepsDataInMemory(t *testing.T) {
	out := newNodeOutputs(&failingStore{}, "run-1", 1024)
	ds := &common.DataSet{Columns: []string{"id"}, Rows: rowsOfSize(500, 64)}

	err := out.Put("n1", ds)
	if err == nil {
		t.Fatal("Put should report that the spill failed")
	}
	if !strings.Contains(err.Error(), "spill node n1 output") {
		t.Errorf("error should name the node, got: %v", err)
	}
	got, ok, getErr := out.Get("n1")
	if getErr != nil || !ok {
		t.Fatalf("output was lost after a failed spill: ok=%v err=%v", ok, getErr)
	}
	if got != ds {
		t.Error("output should still be the in-memory dataset")
	}
	if out.spilledCount() != 0 {
		t.Error("a failed spill should not be recorded as spilled")
	}
}

// A spilled output that cannot be read back is a hard error. It was the only
// copy; substituting an empty dataset is the data-loss shape #8 fixed.
func TestNodeOutputs_UnreadableSpillIsAnError(t *testing.T) {
	store := artifact.NewLocalDiskStore(t.TempDir())
	out := newNodeOutputs(store, "run-1", 1024)
	if err := out.Put("n1", &common.DataSet{Columns: []string{"id"}, Rows: rowsOfSize(500, 64)}); err != nil {
		t.Fatal(err)
	}
	// Drop the bytes behind the reference.
	if err := store.DeleteNamespace(context.Background(), "run-1"); err != nil {
		t.Fatal(err)
	}

	ds, ok, err := out.Get("n1")
	if err == nil {
		t.Fatal("a missing spilled output was reported as success")
	}
	if !ok {
		t.Error("the node should still be known to have had an output")
	}
	if ds != nil {
		t.Error("no dataset should be returned when the spill cannot be read")
	}
}

func TestNodeOutputs_MissingNodeIsNotAnError(t *testing.T) {
	out := newNodeOutputs(artifact.NewLocalDiskStore(t.TempDir()), "run-1", 1024)
	ds, ok, err := out.Get("never-ran")
	if err != nil || ok || ds != nil {
		t.Errorf("want (nil, false, nil), got (%v, %v, %v)", ds, ok, err)
	}
}

// Re-recording an output must not leave the previous state behind — a node
// that spilled and is then re-run smaller should be inline, and vice versa.
func TestNodeOutputs_PutReplacesPreviousState(t *testing.T) {
	store := artifact.NewLocalDiskStore(t.TempDir())
	out := newNodeOutputs(store, "run-1", 2048)

	big := &common.DataSet{Columns: []string{"id"}, Rows: rowsOfSize(500, 64)}
	small := &common.DataSet{Columns: []string{"id"}, Rows: rowsOfSize(2, 4)}

	if err := out.Put("n1", big); err != nil {
		t.Fatal(err)
	}
	if out.spilledCount() != 1 {
		t.Fatal("expected the large output to spill")
	}
	if err := out.Put("n1", small); err != nil {
		t.Fatal(err)
	}
	if out.spilledCount() != 0 {
		t.Error("the stale spilled reference was not cleared")
	}
	got, _, _ := out.Get("n1")
	if got != small {
		t.Error("Get returned the stale output")
	}
}

func TestNodeOutputs_NilOutputIsIgnored(t *testing.T) {
	out := newNodeOutputs(artifact.NewLocalDiskStore(t.TempDir()), "run-1", 1024)
	if err := out.Put("n1", nil); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := out.Get("n1"); ok {
		t.Error("a nil output should not be recorded")
	}
}

func TestEstimateEncodedSize(t *testing.T) {
	if got := estimateEncodedSize(nil); got != 0 {
		t.Errorf("nil = %d, want 0", got)
	}
	if got := estimateEncodedSize(&common.DataSet{}); got != 0 {
		t.Errorf("empty = %d, want 0", got)
	}

	// Under the sample size the estimate is the exact encoded length.
	small := &common.DataSet{Rows: rowsOfSize(4, 10)}
	if got := estimateEncodedSize(small); got < 40 {
		t.Errorf("small estimate = %d, implausibly low", got)
	}

	// Beyond the sample it extrapolates, and should land near the truth for
	// uniform rows. Compared against the real encoding rather than a
	// hardcoded number.
	big := &common.DataSet{Rows: rowsOfSize(1000, 50)}
	var buf strings.Builder
	if err := EncodeArrowJSON(&buf, big); err != nil {
		t.Fatal(err)
	}
	actual := int64(buf.Len())
	est := estimateEncodedSize(big)
	ratio := float64(est) / float64(actual)
	if ratio < 0.9 || ratio > 1.1 {
		t.Errorf("estimate %d vs actual %d (ratio %.2f) — outside 10%% for uniform rows", est, actual, ratio)
	}
}

// failingStore rejects everything, standing in for a full or unavailable disk.
type failingStore struct{}

func (f *failingStore) Put(ctx context.Context, namespace string, r io.Reader, opts artifact.PutOptions) (*artifact.ArtifactRef, error) {
	return nil, errors.New("no space left on device")
}
func (f *failingStore) Open(ctx context.Context, ref *artifact.ArtifactRef) (io.ReadCloser, error) {
	return nil, fmt.Errorf("%w: unavailable", artifact.ErrNotFound)
}
func (f *failingStore) DeleteNamespace(ctx context.Context, namespace string) error { return nil }

// A dry run must never spill, even with an artifact store wired. Nothing
// persists a dry run, so nothing would ever purge its namespace — every
// spilled preview would be an orphaned blob. Today Engine.DryRun happens
// not to wire a store; this pins the guarantee rather than the accident.
func TestNewOutputs_DryRunNeverSpills(t *testing.T) {
	r := &Runner{
		dryRun:        true,
		artifactStore: NewLocalDiskArtifactStore(t.TempDir()),
		run:           &models.Run{ID: "dry-run-id"},
	}
	if out := r.newOutputs(); out.spillEnabled() {
		t.Fatal("a dry run with a store wired would spill, orphaning blobs nothing purges")
	}

	// The same runner not in dry-run mode spills, proving the guard is the
	// dry-run flag and not a broken fixture.
	r.dryRun = false
	if out := r.newOutputs(); !out.spillEnabled() {
		t.Fatal("fixture broken: the non-dry runner should be able to spill")
	}
}
