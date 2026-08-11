package engine

// Tests for ProjectRunInstances (#90 M2, ADR-015 §8): the physical
// instances a run executed, projected from its node runs and expansion
// instances into the deterministic-key model.

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/Tnsor-Labs/brokoli/models"
	"github.com/Tnsor-Labs/brokoli/store"
)

func projStore(t *testing.T) *store.SQLiteStore {
	t.Helper()
	s, err := store.NewSQLiteStore(filepath.Join(t.TempDir(), "proj.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func seedRun(t *testing.T, s *store.SQLiteStore, runID string) {
	t.Helper()
	if err := s.CreatePipeline(&models.Pipeline{
		ID: "p-" + runID, Name: "p",
		Nodes:     []models.Node{{ID: "n", Type: models.NodeTypeSourceFile, Name: "n"}},
		CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	if err := s.CreateRun(&models.Run{ID: runID, PipelineID: "p-" + runID, Status: models.RunStatusSuccess, StartedAt: &now}); err != nil {
		t.Fatal(err)
	}
}

func byKey(insts []models.PhysicalInstance) map[string]models.PhysicalInstance {
	m := map[string]models.PhysicalInstance{}
	for _, i := range insts {
		m[i.InstanceKey] = i
	}
	return m
}

func TestProjectRunInstancesPlainNodes(t *testing.T) {
	s := projStore(t)
	seedRun(t, s, "run-plain")
	for _, id := range []string{"src", "sink"} {
		if err := s.CreateNodeRun(&models.NodeRun{
			ID: id + "-nr", RunID: "run-plain", NodeID: id,
			Status: models.RunStatusSuccess, RowCount: 5,
		}); err != nil {
			t.Fatal(err)
		}
	}
	insts, err := ProjectRunInstances(s, "run-plain")
	if err != nil {
		t.Fatal(err)
	}
	if len(insts) != 2 {
		t.Fatalf("got %d instances, want 2", len(insts))
	}
	m := byKey(insts)
	for _, id := range []string{"src", "sink"} {
		u, ok := m[id]
		if !ok || u.Kind != models.WorkUnitSingle || u.LogicalNodeID != id || u.RowCount != 5 {
			t.Fatalf("bad single instance for %q: %+v", id, u)
		}
	}
}

func TestProjectRunInstancesExpansionReplacesNodeSummary(t *testing.T) {
	s := projStore(t)
	seedRun(t, s, "run-exp")
	// The expansion node's own NodeRun (the aggregate summary) plus a
	// plain downstream sink.
	if err := s.CreateNodeRun(&models.NodeRun{ID: "fan-nr", RunID: "run-exp", NodeID: "fan", Status: models.RunStatusSuccess}); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateNodeRun(&models.NodeRun{ID: "sink-nr", RunID: "run-exp", NodeID: "sink", Status: models.RunStatusSuccess}); err != nil {
		t.Fatal(err)
	}
	// Two resolved expansion items under "fan".
	for i, key := range []string{"file-a", "file-b"} {
		start := time.Now().UTC()
		if err := s.CreateExpansionInstance(&models.ExpansionInstance{
			ID: key, RunID: "run-exp", NodeID: "fan", InstanceIndex: i,
			InstanceKey: key, Status: models.RunStatusSuccess, RowCount: 3, StartedAt: &start,
		}); err != nil {
			t.Fatal(err)
		}
	}
	insts, err := ProjectRunInstances(s, "run-exp")
	if err != nil {
		t.Fatal(err)
	}
	// Expected: sink (single) + 2 expansion items. The fan NodeRun is the
	// aggregate summary and must NOT appear as an instance.
	if len(insts) != 3 {
		t.Fatalf("got %d instances, want 3 (sink + 2 items): %+v", len(insts), insts)
	}
	m := byKey(insts)
	if _, ok := m["fan"]; ok {
		t.Fatal("expansion node's aggregate NodeRun leaked in as a single instance")
	}
	if u := m["file-a"]; u.Kind != models.WorkUnitExpansion || u.LogicalNodeID != "fan" || u.Index != 0 || u.RowCount != 3 {
		t.Fatalf("bad expansion instance file-a: %+v", u)
	}
	if u := m["file-b"]; u.Kind != models.WorkUnitExpansion || u.Index != 1 {
		t.Fatalf("bad expansion instance file-b: %+v", u)
	}
	if _, ok := m["sink"]; !ok {
		t.Fatal("plain downstream sink missing from projection")
	}
}

func TestProjectRunInstancesEmptyRun(t *testing.T) {
	s := projStore(t)
	seedRun(t, s, "run-empty")
	insts, err := ProjectRunInstances(s, "run-empty")
	if err != nil {
		t.Fatal(err)
	}
	if len(insts) != 0 {
		t.Fatalf("got %d, want 0 for a run with no node runs", len(insts))
	}
}
