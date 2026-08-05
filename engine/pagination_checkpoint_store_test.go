package engine

import (
	"errors"
	"testing"

	"github.com/Tnsor-Labs/brokoli/pkg/common"
	"github.com/Tnsor-Labs/brokoli/pkg/fetchers"
)

func TestLocalDiskPaginationCheckpointStore_SaveLoadRoundTrip(t *testing.T) {
	store := NewLocalDiskPaginationCheckpointStore(t.TempDir())
	cp := fetchers.PaginationCheckpoint{Strategy: "offset", Offset: 40, PagesFetched: 20}
	records := &common.DataSet{
		Columns: []string{"id"},
		Rows: []common.DataRow{
			{"id": float64(1)},
			{"id": float64(2)},
		},
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

func TestLocalDiskPaginationCheckpointStore_OverwritesOnRepeatedSave(t *testing.T) {
	store := NewLocalDiskPaginationCheckpointStore(t.TempDir())
	early := fetchers.PaginationCheckpoint{Strategy: "offset", Offset: 10, PagesFetched: 5}
	later := fetchers.PaginationCheckpoint{Strategy: "offset", Offset: 30, PagesFetched: 15}
	ds := &common.DataSet{Columns: []string{"id"}, Rows: []common.DataRow{{"id": float64(1)}}}

	if err := store.SaveCheckpoint("run-1", "node-a", early, ds); err != nil {
		t.Fatalf("save early: %v", err)
	}
	if err := store.SaveCheckpoint("run-1", "node-a", later, ds); err != nil {
		t.Fatalf("save later: %v", err)
	}

	got, _, err := store.LoadCheckpoint("run-1", "node-a")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if got.Offset != 30 || got.PagesFetched != 15 {
		t.Errorf("checkpoint = %+v, want the later save (Offset=30 PagesFetched=15) to have overwritten the earlier one", got)
	}
}

func TestLocalDiskPaginationCheckpointStore_DeleteThenLoadIsNotFound(t *testing.T) {
	store := NewLocalDiskPaginationCheckpointStore(t.TempDir())
	cp := fetchers.PaginationCheckpoint{Strategy: "numbered", Page: 3, PagesFetched: 2}
	ds := &common.DataSet{Columns: []string{"id"}, Rows: []common.DataRow{{"id": float64(1)}}}

	if err := store.SaveCheckpoint("run-1", "node-a", cp, ds); err != nil {
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
	cp := fetchers.PaginationCheckpoint{Strategy: "offset", Offset: 10, PagesFetched: 5}
	ds := &common.DataSet{Columns: []string{"id"}, Rows: []common.DataRow{{"id": float64(1)}}}

	if err := store.SaveCheckpoint("run-1", "node-a", cp, ds); err != nil {
		t.Fatalf("save run-1/node-a: %v", err)
	}
	if err := store.SaveCheckpoint("run-1", "node-b", cp, ds); err != nil {
		t.Fatalf("save run-1/node-b: %v", err)
	}
	if err := store.SaveCheckpoint("run-2", "node-a", cp, ds); err != nil {
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
	cpA := fetchers.PaginationCheckpoint{Strategy: "offset", Offset: 10, PagesFetched: 5}
	cpB := fetchers.PaginationCheckpoint{Strategy: "offset", Offset: 20, PagesFetched: 10}
	ds := &common.DataSet{Columns: []string{"id"}, Rows: []common.DataRow{{"id": float64(1)}}}

	if err := store.SaveCheckpoint("run-1", "node-a", cpA, ds); err != nil {
		t.Fatalf("save A: %v", err)
	}
	if err := store.SaveCheckpoint("run-1", "node-b", cpB, ds); err != nil {
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
