package engine

import (
	"errors"
	"os"
	"testing"

	"github.com/Tnsor-Labs/brokoli/pkg/fetchers"
)

func TestLocalDiskPaginationCheckpointStore_SaveLoadRoundTrip(t *testing.T) {
	store := NewLocalDiskPaginationCheckpointStore(t.TempDir())
	cp := fetchers.PaginationCheckpoint{Strategy: "offset", Offset: 40, PagesFetched: 20, RecordCount: 2}
	records := []map[string]interface{}{
		{"id": float64(1)},
		{"id": float64(2)},
	}

	if err := store.SaveCheckpoint("run-1", "node-a", cp, records); err != nil {
		t.Fatalf("save checkpoint: %v", err)
	}

	gotCP, gotRecords, err := store.LoadCheckpoint("run-1", "node-a")
	if err != nil {
		t.Fatalf("load checkpoint: %v", err)
	}
	if gotCP.Strategy != "offset" || gotCP.Offset != 40 || gotCP.PagesFetched != 20 {
		t.Errorf("checkpoint = %+v, want Strategy=offset Offset=40 PagesFetched=20", gotCP)
	}
	if len(gotRecords.Rows) != 2 {
		t.Fatalf("got %d records, want 2", len(gotRecords.Rows))
	}
}

func TestLocalDiskPaginationCheckpointStore_LoadMissingReturnsErrCheckpointNotFound(t *testing.T) {
	store := NewLocalDiskPaginationCheckpointStore(t.TempDir())
	_, _, err := store.LoadCheckpoint("run-missing", "node-missing")
	if err == nil {
		t.Fatal("expected an error for a missing checkpoint")
	}
	if !errors.Is(err, ErrCheckpointNotFound) {
		t.Fatalf("error = %v, want it to wrap ErrCheckpointNotFound", err)
	}
}

// TestLocalDiskPaginationCheckpointStore_RepeatedSaveAppendsRecords is the
// core fix for issue #52: a second SaveCheckpoint call with a *delta* must
// end up with all records from both calls, not just the latest one (which
// is what would happen if SaveCheckpoint still rewrote the file each time).
func TestLocalDiskPaginationCheckpointStore_RepeatedSaveAppendsRecords(t *testing.T) {
	store := NewLocalDiskPaginationCheckpointStore(t.TempDir())
	early := fetchers.PaginationCheckpoint{Strategy: "offset", Offset: 10, PagesFetched: 5, RecordCount: 1}
	later := fetchers.PaginationCheckpoint{Strategy: "offset", Offset: 30, PagesFetched: 15, RecordCount: 3}

	if err := store.SaveCheckpoint("run-1", "node-a", early, []map[string]interface{}{{"id": float64(1)}}); err != nil {
		t.Fatalf("save early: %v", err)
	}
	if err := store.SaveCheckpoint("run-1", "node-a", later, []map[string]interface{}{{"id": float64(2)}, {"id": float64(3)}}); err != nil {
		t.Fatalf("save later: %v", err)
	}

	got, records, err := store.LoadCheckpoint("run-1", "node-a")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if got.Offset != 30 || got.PagesFetched != 15 {
		t.Errorf("checkpoint = %+v, want the later save's position (Offset=30 PagesFetched=15)", got)
	}
	if len(records.Rows) != 3 {
		t.Fatalf("got %d records, want 3 (1 from the early save + 2 from the later save — must accumulate, not overwrite)", len(records.Rows))
	}
}

// TestLocalDiskPaginationCheckpointStore_LoadTruncatesToRecordCount covers
// the defensive read-side check: if the records file somehow has more rows
// than the position's RecordCount claims (e.g. an append that succeeded
// right before a crash, before the position file could be committed), the
// extra rows must be dropped, not trusted.
func TestLocalDiskPaginationCheckpointStore_LoadTruncatesToRecordCount(t *testing.T) {
	store := NewLocalDiskPaginationCheckpointStore(t.TempDir())
	cp := fetchers.PaginationCheckpoint{Strategy: "offset", Offset: 10, PagesFetched: 5, RecordCount: 2}

	if err := store.SaveCheckpoint("run-1", "node-a", cp, []map[string]interface{}{{"id": float64(1)}, {"id": float64(2)}}); err != nil {
		t.Fatalf("save: %v", err)
	}

	// Simulate an append that landed on disk without its matching position
	// update ever being committed (e.g. a crash between the two writes).
	_, recordsPath := store.checkpointPaths("run-1", "node-a")
	f, err := os.OpenFile(recordsPath, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatalf("open records file: %v", err)
	}
	if _, err := f.WriteString(`{"id":3}` + "\n"); err != nil {
		t.Fatalf("append extra row: %v", err)
	}
	f.Close()

	_, records, err := store.LoadCheckpoint("run-1", "node-a")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(records.Rows) != 2 {
		t.Fatalf("got %d records, want exactly 2 (RecordCount) — the uncommitted 3rd row must be dropped, not trusted", len(records.Rows))
	}
}

// TestLocalDiskPaginationCheckpointStore_LoadRejectsShortRecordsFile covers
// the other direction: a records file with *fewer* complete rows than the
// committed position claims indicates real corruption, not an ordinary
// mid-append crash — LoadCheckpoint must refuse to resume from it.
func TestLocalDiskPaginationCheckpointStore_LoadRejectsShortRecordsFile(t *testing.T) {
	store := NewLocalDiskPaginationCheckpointStore(t.TempDir())
	cp := fetchers.PaginationCheckpoint{Strategy: "offset", Offset: 10, PagesFetched: 5, RecordCount: 5}

	if err := store.SaveCheckpoint("run-1", "node-a", cp, []map[string]interface{}{{"id": float64(1)}, {"id": float64(2)}}); err != nil {
		t.Fatalf("save: %v", err)
	}

	_, _, err := store.LoadCheckpoint("run-1", "node-a")
	if err == nil {
		t.Fatal("expected an error: records file has 2 rows but the position claims 5")
	}
}

func TestLocalDiskPaginationCheckpointStore_DeleteThenLoadIsNotFound(t *testing.T) {
	store := NewLocalDiskPaginationCheckpointStore(t.TempDir())
	cp := fetchers.PaginationCheckpoint{Strategy: "numbered", Page: 3, PagesFetched: 2, RecordCount: 1}

	if err := store.SaveCheckpoint("run-1", "node-a", cp, []map[string]interface{}{{"id": float64(1)}}); err != nil {
		t.Fatalf("save: %v", err)
	}
	if err := store.DeleteCheckpoint("run-1", "node-a"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	_, _, err := store.LoadCheckpoint("run-1", "node-a")
	if !errors.Is(err, ErrCheckpointNotFound) {
		t.Fatalf("expected ErrCheckpointNotFound after delete, got %v", err)
	}
}

func TestLocalDiskPaginationCheckpointStore_DeleteMissingIsNotAnError(t *testing.T) {
	store := NewLocalDiskPaginationCheckpointStore(t.TempDir())
	if err := store.DeleteCheckpoint("run-never-existed", "node-never-existed"); err != nil {
		t.Fatalf("deleting a checkpoint that was never saved should be a no-op, got: %v", err)
	}
}

func TestLocalDiskPaginationCheckpointStore_DeleteRunCheckpoints_RemovesAllNodesForRun(t *testing.T) {
	store := NewLocalDiskPaginationCheckpointStore(t.TempDir())
	cp := fetchers.PaginationCheckpoint{Strategy: "offset", Offset: 10, PagesFetched: 5, RecordCount: 1}
	records := []map[string]interface{}{{"id": float64(1)}}

	if err := store.SaveCheckpoint("run-1", "node-a", cp, records); err != nil {
		t.Fatalf("save run-1/node-a: %v", err)
	}
	if err := store.SaveCheckpoint("run-1", "node-b", cp, records); err != nil {
		t.Fatalf("save run-1/node-b: %v", err)
	}
	if err := store.SaveCheckpoint("run-2", "node-a", cp, records); err != nil {
		t.Fatalf("save run-2/node-a: %v", err)
	}

	if err := store.DeleteRunCheckpoints("run-1"); err != nil {
		t.Fatalf("delete run-1 checkpoints: %v", err)
	}

	if _, _, err := store.LoadCheckpoint("run-1", "node-a"); !errors.Is(err, ErrCheckpointNotFound) {
		t.Errorf("run-1/node-a: got %v, want ErrCheckpointNotFound", err)
	}
	if _, _, err := store.LoadCheckpoint("run-1", "node-b"); !errors.Is(err, ErrCheckpointNotFound) {
		t.Errorf("run-1/node-b: got %v, want ErrCheckpointNotFound", err)
	}
	// A different run's checkpoint must survive untouched.
	if _, _, err := store.LoadCheckpoint("run-2", "node-a"); err != nil {
		t.Errorf("run-2/node-a should be unaffected by deleting run-1, got: %v", err)
	}
}

func TestLocalDiskPaginationCheckpointStore_DeleteRunCheckpoints_MissingRunIsNotAnError(t *testing.T) {
	store := NewLocalDiskPaginationCheckpointStore(t.TempDir())
	if err := store.DeleteRunCheckpoints("run-never-existed"); err != nil {
		t.Fatalf("deleting checkpoints for a run that never wrote any should be a no-op, got: %v", err)
	}
}

func TestLocalDiskPaginationCheckpointStore_DifferentKeysDoNotCollide(t *testing.T) {
	store := NewLocalDiskPaginationCheckpointStore(t.TempDir())
	cpA := fetchers.PaginationCheckpoint{Strategy: "offset", Offset: 10, PagesFetched: 5, RecordCount: 1}
	cpB := fetchers.PaginationCheckpoint{Strategy: "offset", Offset: 20, PagesFetched: 10, RecordCount: 1}
	records := []map[string]interface{}{{"id": float64(1)}}

	if err := store.SaveCheckpoint("run-1", "node-a", cpA, records); err != nil {
		t.Fatalf("save A: %v", err)
	}
	if err := store.SaveCheckpoint("run-1", "node-b", cpB, records); err != nil {
		t.Fatalf("save B: %v", err)
	}

	gotA, _, err := store.LoadCheckpoint("run-1", "node-a")
	if err != nil {
		t.Fatalf("load A: %v", err)
	}
	gotB, _, err := store.LoadCheckpoint("run-1", "node-b")
	if err != nil {
		t.Fatalf("load B: %v", err)
	}
	if gotA.Offset != 10 || gotB.Offset != 20 {
		t.Fatalf("checkpoints collided: A=%+v B=%+v", gotA, gotB)
	}
}
