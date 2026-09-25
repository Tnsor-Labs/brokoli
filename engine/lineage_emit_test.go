package engine

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/Tnsor-Labs/brokoli/extensions"
	"github.com/Tnsor-Labs/brokoli/models"
	"github.com/Tnsor-Labs/brokoli/store"
)

// Emitting lineage for a run.
//
// The emitter existed and was called from nowhere: no code in this
// repository read the registry field. So the first thing worth asserting
// is not the shape of an event but that one is sent at all.

type capturedEvent struct {
	kind    string
	runID   string
	inputs  []extensions.LineageDataset
	outputs []extensions.LineageDataset
	errMsg  string
}

type capturingLineage struct {
	mu     sync.Mutex
	events []capturedEvent
	fail   bool
}

func (c *capturingLineage) record(e capturedEvent) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.events = append(c.events, e)
	if c.fail {
		return context.DeadlineExceeded
	}
	return nil
}

func (c *capturingLineage) EmitRunStart(_, _, runID string, in, out []extensions.LineageDataset) error {
	return c.record(capturedEvent{kind: "START", runID: runID, inputs: in, outputs: out})
}

func (c *capturingLineage) EmitRunComplete(_, _, runID string, _ int64, in, out []extensions.LineageDataset) error {
	return c.record(capturedEvent{kind: "COMPLETE", runID: runID, inputs: in, outputs: out})
}

func (c *capturingLineage) EmitRunFail(_, _, runID string, errMsg string, in, out []extensions.LineageDataset) error {
	return c.record(capturedEvent{kind: "FAIL", runID: runID, inputs: in, outputs: out, errMsg: errMsg})
}

func (c *capturingLineage) kinds() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]string, len(c.events))
	for i, e := range c.events {
		out[i] = e.kind
	}
	return out
}

func (c *capturingLineage) find(kind string) (capturedEvent, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, e := range c.events {
		if e.kind == kind {
			return e, true
		}
	}
	return capturedEvent{}, false
}

// emitTestPipeline is a real file-to-file pipeline, so the assets it
// reports are the ones a catalogue would show.
func emitTestPipeline(t *testing.T, failing bool) (*store.SQLiteStore, *models.Pipeline, string, string) {
	t.Helper()
	dir := t.TempDir()
	in := filepath.Join(dir, "input.csv")
	out := filepath.Join(dir, "output.csv")
	if err := os.WriteFile(in, []byte("id,name\n1,brokoli\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if failing {
		// A source pointing at a file that is not there fails the run
		// without needing a broken node type.
		in = filepath.Join(dir, "missing.csv")
	}

	s, err := store.NewSQLiteStore(filepath.Join(dir, "emit.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })

	p := &models.Pipeline{
		ID: "emit-1", Name: "emitting pipeline",
		Nodes: []models.Node{
			{ID: "src", Type: models.NodeTypeSourceFile, Name: "src",
				Config: map[string]interface{}{"path": in, "format": "csv"}},
			{ID: "dst", Type: models.NodeTypeSinkFile, Name: "dst",
				Config: map[string]interface{}{"path": out, "format": "csv"}},
		},
		Edges:     []models.Edge{{From: "src", To: "dst"}},
		CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}
	if err := s.CreatePipeline(p); err != nil {
		t.Fatal(err)
	}
	return s, p, in, out
}

func runWithLineage(t *testing.T, s *store.SQLiteStore, pipelineID string, cap *capturingLineage) {
	t.Helper()
	eng := NewEngine(s)
	eng.Lineage = cap
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = eng.Close(ctx)
	})
	_, _ = eng.RunPipeline(pipelineID)
}

// The bug: nothing called the emitter. A successful run must produce a
// START and a COMPLETE.
func TestASuccessfulRunEmitsStartAndComplete(t *testing.T) {
	s, p, _, _ := emitTestPipeline(t, false)
	cap := &capturingLineage{}
	runWithLineage(t, s, p.ID, cap)

	kinds := cap.kinds()
	if len(kinds) != 2 || kinds[0] != "START" || kinds[1] != "COMPLETE" {
		t.Fatalf("events = %v, want START then COMPLETE", kinds)
	}
	start, _ := cap.find("START")
	done, _ := cap.find("COMPLETE")
	if start.runID == "" || start.runID != done.runID {
		t.Errorf("run ids differ or are empty: start=%q complete=%q", start.runID, done.runID)
	}
}

// The datasets are the point of the format. An event without them tells
// a catalogue that a job ran and nothing about lineage.
func TestEmittedEventsCarryTheDatasets(t *testing.T) {
	s, p, in, out := emitTestPipeline(t, false)
	cap := &capturingLineage{}
	runWithLineage(t, s, p.ID, cap)

	event, ok := cap.find("START")
	if !ok {
		t.Fatal("no START event")
	}
	if len(event.inputs) != 1 || event.inputs[0].ID != "file:"+in {
		t.Fatalf("inputs = %+v, want the source file %q", event.inputs, in)
	}
	if len(event.outputs) != 1 || event.outputs[0].ID != "file:"+out {
		t.Fatalf("outputs = %+v, want the sink file %q", event.outputs, out)
	}
	if event.inputs[0].Type != "file" || event.inputs[0].Name == "" {
		t.Errorf("input dataset is unnamed or untyped: %+v", event.inputs[0])
	}
}

// A catalogue that only hears about successes shows a pipeline as
// healthy while it is broken, and the failed run is the one somebody is
// looking for.
func TestAFailedRunEmitsFailWithItsDatasets(t *testing.T) {
	s, p, _, _ := emitTestPipeline(t, true)
	cap := &capturingLineage{}
	runWithLineage(t, s, p.ID, cap)

	kinds := cap.kinds()
	if len(kinds) == 0 || kinds[0] != "START" {
		t.Fatalf("events = %v, want a START first", kinds)
	}
	event, ok := cap.find("FAIL")
	if !ok {
		t.Fatalf("events = %v, want a FAIL", kinds)
	}
	if event.errMsg == "" {
		t.Error("the failure event carries no message")
	}
	if len(event.outputs) != 1 {
		t.Errorf("outputs = %+v, want the sink reported even though the run failed", event.outputs)
	}
}

// A catalogue being down must not fail a run. The run is the product;
// lineage is a report about it.
func TestACatalogueFailureDoesNotFailTheRun(t *testing.T) {
	s, p, _, out := emitTestPipeline(t, false)
	cap := &capturingLineage{fail: true}
	runWithLineage(t, s, p.ID, cap)

	if len(cap.kinds()) != 2 {
		t.Fatalf("events = %v, want the run to keep emitting after a failure", cap.kinds())
	}
	// The run really produced its output despite every emit erroring.
	if _, err := os.Stat(out); err != nil {
		t.Fatalf("the run did not write its output: %v", err)
	}
}

// A deployment with no catalogue is the normal case and must cost
// nothing but a nil check.
func TestNoEmitterIsFine(t *testing.T) {
	s, p, _, out := emitTestPipeline(t, false)
	eng := NewEngine(s)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = eng.Close(ctx)
	})
	if _, err := eng.RunPipeline(p.ID); err != nil {
		t.Fatalf("run with no emitter: %v", err)
	}
	if _, err := os.Stat(out); err != nil {
		t.Fatalf("the run did not write its output: %v", err)
	}
}

// PipelineAssets is what names the datasets, and it must agree with the
// lineage graph rather than inventing a second naming scheme.
func TestPipelineAssetsMatchTheLineageGraph(t *testing.T) {
	_, p, in, out := emitTestPipeline(t, false)

	inputs, outputs := PipelineAssets(p)
	if len(inputs) != 1 || len(outputs) != 1 {
		t.Fatalf("assets = %+v / %+v, want one of each", inputs, outputs)
	}

	graph := BuildLineageGraph([]models.Pipeline{*p})
	ids := map[string]bool{}
	for _, n := range graph.Nodes {
		ids[n.ID] = true
	}
	for _, a := range append(inputs, outputs...) {
		if !ids[a.ID] {
			t.Errorf("asset %q is not a node in the lineage graph; the two name assets differently", a.ID)
		}
	}
	_ = in
	_ = out
}

// A node the extractors cannot name is skipped, not emitted with an
// empty id, which would merge every unnamed asset in a catalogue into
// one dataset.
func TestAnUnnameableAssetIsSkipped(t *testing.T) {
	p := &models.Pipeline{
		ID: "p", Name: "p",
		Nodes: []models.Node{
			{ID: "src", Type: models.NodeTypeSourceFile, Name: "src",
				Config: map[string]interface{}{"format": "csv"}}, // no path
		},
	}
	inputs, outputs := PipelineAssets(p)
	for _, a := range append(inputs, outputs...) {
		if a.ID == "" || a.ID == "file:" {
			t.Fatalf("an unnameable asset was emitted: %+v", a)
		}
	}
}

// Reading the same file twice is one dataset, not two reads a catalogue
// records separately.
func TestRepeatedAssetsAreDeduplicated(t *testing.T) {
	dir := t.TempDir()
	same := filepath.Join(dir, "shared.csv")
	p := &models.Pipeline{
		ID: "p", Name: "p",
		Nodes: []models.Node{
			{ID: "a", Type: models.NodeTypeSourceFile, Name: "a",
				Config: map[string]interface{}{"path": same, "format": "csv"}},
			{ID: "b", Type: models.NodeTypeSourceFile, Name: "b",
				Config: map[string]interface{}{"path": same, "format": "csv"}},
		},
	}
	inputs, _ := PipelineAssets(p)
	if len(inputs) != 1 {
		t.Fatalf("inputs = %+v, want the same file reported once", inputs)
	}
}
