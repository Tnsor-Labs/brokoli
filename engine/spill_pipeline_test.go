package engine

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Tnsor-Labs/brokoli/models"
	"github.com/Tnsor-Labs/brokoli/store"
)

// bigRowServer serves n rows, each padded to roughly cellBytes, so a
// pipeline can produce an output large enough to cross a spill threshold.
func bigRowServer(t *testing.T, n, cellBytes int) *httptest.Server {
	t.Helper()
	pad := strings.Repeat("y", cellBytes)
	items := make([]map[string]interface{}, n)
	for i := range items {
		items[i] = map[string]interface{}{"id": i, "payload": pad}
	}
	body, err := json.Marshal(items)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(body)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// newSpillTestEngine is newResumeTestEngine with directories that are not
// t.TempDir().
//
// Engine has no Stop/Close, so its background work — dependency trigger
// mode in particular — keeps running after a test returns and can write
// into the run's artifact directory while t.TempDir()'s cleanup is removing
// it, failing the test with "directory not empty" for reasons unrelated to
// what it asserts. These tests finish runs with downstream edges, which is
// what makes them reach that path more reliably than the existing ones.
// Cleaning up manually and ignoring the error keeps the flakiness out of
// the assertions. Giving Engine a shutdown is the real fix and is not in
// scope here.
func newSpillTestEngine(t *testing.T) (*Engine, *store.SQLiteStore) {
	t.Helper()
	dir, err := os.MkdirTemp("", "brokoli-spill-*")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })

	s, err := store.NewSQLiteStore(filepath.Join(dir, "spill.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	eng := NewEngine(s)
	eng.ArtifactStore = NewLocalDiskArtifactStore(filepath.Join(dir, "artifacts"))
	eng.PaginationCheckpointStore = NewLocalDiskPaginationCheckpointStore(filepath.Join(dir, "pagination-checkpoints"))
	return eng, s
}

// A pipeline whose source output crosses the threshold spills it, and the
// downstream node still receives every row. This is the property that
// matters: spilling must be invisible to the data.
func TestPipeline_LargeOutputSpillsWithoutChangingResults(t *testing.T) {
	const rows = 400
	srv := bigRowServer(t, rows, 200)

	eng, s := newSpillTestEngine(t)
	eng.SpillThresholdBytes = 4096 // well below the source's output

	pipeline := &models.Pipeline{
		ID: "p-spill", Name: "Spill", Enabled: true,
		Nodes: []models.Node{
			{ID: "source", Type: models.NodeTypeSourceAPI, Name: "Big Source",
				Config: map[string]interface{}{"url": srv.URL}},
			{ID: "keep", Type: models.NodeTypeTransform, Name: "Passthrough",
				Config: map[string]interface{}{"rules": []interface{}{
					map[string]interface{}{"type": "add_column", "name": "tag", "expression": "'seen'"},
				}}},
		},
		Edges: []models.Edge{{From: "source", To: "keep"}},
	}
	if err := s.CreatePipeline(pipeline); err != nil {
		t.Fatal(err)
	}

	run, err := eng.RunPipeline(pipeline.ID)
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != models.RunStatusSuccess {
		t.Fatalf("run status = %s, want success (error: %s)", run.Status, run.Error)
	}

	// The downstream node saw every row, so nothing was lost across the
	// spill and reload.
	nodeRuns, err := s.ListNodeRunsByRun(run.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, nr := range nodeRuns {
		if nr.NodeID == "keep" && nr.Status == models.RunStatusSuccess {
			if nr.RowCount != rows {
				t.Errorf("downstream node saw %d rows, want %d", nr.RowCount, rows)
			}
			return
		}
	}
	t.Fatalf("no successful run for the downstream node among %+v", nodeRuns)
}

// The mirror of the test above with spilling disabled. Both assert the same
// row count against the same pipeline shape, so a divergence between them
// would show up as one failing — without needing two engines in one test,
// whose background work races the other's store cleanup.
func TestPipeline_SpillDisabledProducesSameResult(t *testing.T) {
	const rows = 300
	srv := bigRowServer(t, rows, 200)

	eng, s := newSpillTestEngine(t)
	eng.SpillThresholdBytes = -1 // disabled: previous always-in-memory behaviour

	pipeline := &models.Pipeline{
		ID: "p-spill-disabled", Name: "Spill Disabled", Enabled: true,
		Nodes: []models.Node{
			{ID: "source", Type: models.NodeTypeSourceAPI, Name: "Source",
				Config: map[string]interface{}{"url": srv.URL}},
			{ID: "keep", Type: models.NodeTypeTransform, Name: "Passthrough",
				Config: map[string]interface{}{"rules": []interface{}{
					map[string]interface{}{"type": "add_column", "name": "tag", "expression": "'seen'"},
				}}},
		},
		Edges: []models.Edge{{From: "source", To: "keep"}},
	}
	if err := s.CreatePipeline(pipeline); err != nil {
		t.Fatal(err)
	}
	run, err := eng.RunPipeline(pipeline.ID)
	if err != nil || run.Status != models.RunStatusSuccess {
		t.Fatalf("run failed: %v %s", err, run.Error)
	}

	nodeRuns, _ := s.ListNodeRunsByRun(run.ID)
	for _, nr := range nodeRuns {
		if nr.NodeID == "keep" && nr.Status == models.RunStatusSuccess {
			if nr.RowCount != rows {
				t.Errorf("with spilling disabled the downstream node saw %d rows, want %d", nr.RowCount, rows)
			}
			return
		}
	}
	t.Fatal("no successful run for the downstream node")
}

// A fan-out reads one spilled output from several downstream nodes. Every
// consumer must get the whole thing.
func TestPipeline_SpilledOutputFeedsMultipleConsumers(t *testing.T) {
	const rows = 250
	srv := bigRowServer(t, rows, 200)

	eng, s := newSpillTestEngine(t)
	eng.SpillThresholdBytes = 2048

	pipeline := &models.Pipeline{
		ID: "p-spill-fanout", Name: "Spill Fan-out", Enabled: true,
		Nodes: []models.Node{
			{ID: "source", Type: models.NodeTypeSourceAPI, Name: "Source",
				Config: map[string]interface{}{"url": srv.URL}},
			{ID: "a", Type: models.NodeTypeTransform, Name: "A",
				Config: map[string]interface{}{"rules": []interface{}{
					map[string]interface{}{"type": "add_column", "name": "branch", "expression": "'a'"},
				}}},
			{ID: "b", Type: models.NodeTypeTransform, Name: "B",
				Config: map[string]interface{}{"rules": []interface{}{
					map[string]interface{}{"type": "add_column", "name": "branch", "expression": "'b'"},
				}}},
		},
		Edges: []models.Edge{{From: "source", To: "a"}, {From: "source", To: "b"}},
	}
	if err := s.CreatePipeline(pipeline); err != nil {
		t.Fatal(err)
	}
	run, err := eng.RunPipeline(pipeline.ID)
	if err != nil || run.Status != models.RunStatusSuccess {
		t.Fatalf("run failed: %v %s", err, run.Error)
	}

	nodeRuns, _ := s.ListNodeRunsByRun(run.ID)
	seen := 0
	for _, nr := range nodeRuns {
		if (nr.NodeID == "a" || nr.NodeID == "b") && nr.Status == models.RunStatusSuccess {
			seen++
			if nr.RowCount != rows {
				t.Errorf("node %s saw %d rows, want %d", nr.NodeID, nr.RowCount, rows)
			}
		}
	}
	if seen != 2 {
		t.Errorf("%d of 2 downstream nodes succeeded", seen)
	}
}

// Spilled outputs live in the run's namespace, so purging the run reclaims
// them along with everything else it wrote.
func TestPipeline_SpilledOutputsPurgedWithTheRun(t *testing.T) {
	srv := bigRowServer(t, 300, 200)
	eng, s := newSpillTestEngine(t)
	eng.SpillThresholdBytes = 2048

	pipeline := &models.Pipeline{
		ID: "p-spill-purge", Name: "Spill Purge", Enabled: true,
		Nodes: []models.Node{
			{ID: "source", Type: models.NodeTypeSourceAPI, Name: "Source",
				Config: map[string]interface{}{"url": srv.URL}},
		},
	}
	if err := s.CreatePipeline(pipeline); err != nil {
		t.Fatal(err)
	}
	run, err := eng.RunPipeline(pipeline.ID)
	if err != nil || run.Status != models.RunStatusSuccess {
		t.Fatalf("run failed: %v %s", err, run.Error)
	}

	if err := eng.ArtifactStore.DeleteRunArtifacts(run.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := eng.ArtifactStore.ReadArtifact(run.ID, "source"); err == nil {
		t.Error("the run's artifacts survived DeleteRunArtifacts")
	}
}
