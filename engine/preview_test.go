package engine

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Tnsor-Labs/brokoli/models"
	"github.com/Tnsor-Labs/brokoli/store"
)

// previewBed is a pipeline that reads a file, computes a column, writes a
// file, and has every hook pointed at a server counting calls.
func previewBed(t *testing.T, srcPath string) (*Engine, *store.SQLiteStore, *models.Pipeline, string, *int32) {
	t.Helper()
	dir := t.TempDir()
	s, err := store.NewSQLiteStore(filepath.Join(dir, "real.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	var hooks int32
	hs := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { atomic.AddInt32(&hooks, 1) }))
	t.Cleanup(hs.Close)

	out := filepath.Join(dir, "out.csv")
	hook := models.Hook{Type: "webhook", URL: hs.URL, Enabled: true}
	p := &models.Pipeline{
		ID: "p", Name: "p", Enabled: true,
		Hooks: map[string]models.Hook{"on_start": hook, "on_success": hook, "on_failure": hook},
		Nodes: []models.Node{
			{ID: "src", Type: models.NodeTypeSourceFile, Name: "orders", Config: map[string]interface{}{"path": srcPath, "format": "csv"}},
			{ID: "tf", Type: models.NodeTypeTransform, Name: "total", Config: map[string]interface{}{"rules": []interface{}{
				map[string]interface{}{"type": "add_column", "name": "total", "expression": "price * qty"},
			}}},
			{ID: "dst", Type: models.NodeTypeSinkFile, Name: "out", Config: map[string]interface{}{"path": out, "format": "csv"}},
			{ID: "after", Type: models.NodeTypeFilter, Name: "after the sink", Config: map[string]interface{}{}},
		},
		Edges:     []models.Edge{{From: "src", To: "tf"}, {From: "tf", To: "dst"}, {From: "dst", To: "after"}},
		CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}
	if err := s.CreatePipeline(p); err != nil {
		t.Fatal(err)
	}
	return drainEngineOnCleanup(t, NewEngine(s)), s, p, out, &hooks
}

// A preview shows what the nodes produce and leaves nothing behind: the
// sink is held and writes nothing, no run is recorded, no hook is called.
func TestPreviewWritesNothingAndShowsEveryNode(t *testing.T) {
	in := filepath.Join(t.TempDir(), "orders.csv")
	if err := os.WriteFile(in, []byte("id,price,qty\n1,10,2\n2,20,3\n3,1,1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	e, s, p, out, hooks := previewBed(t, in)

	pv, err := e.PreviewPipeline(context.Background(), p, 2)
	if err != nil {
		t.Fatal(err)
	}
	if pv.Error != "" {
		t.Fatalf("preview error: %s", pv.Error)
	}
	got := map[string]PreviewNode{}
	for _, n := range pv.Nodes {
		got[n.NodeID] = n
	}
	if len(pv.Nodes) != 4 || pv.Nodes[0].NodeID != "src" || pv.Nodes[3].NodeID != "after" {
		t.Fatalf("nodes = %+v, want all four in the pipeline's order", pv.Nodes)
	}
	if tf := got["tf"]; tf.Status != string(models.RunStatusSuccess) || len(tf.Rows) != 2 || tf.Rows[0]["total"] == nil {
		t.Fatalf("transform = %+v, want success with two rows carrying total", tf)
	}
	if d := got["dst"]; d.Status != PreviewHeld || d.Reason == "" {
		t.Fatalf("sink = %+v, want held with a reason", d)
	}
	if a := got["after"]; a.Status != PreviewNotRun {
		t.Fatalf("node after the sink = %+v, want not_run", a)
	}
	if _, err := os.Stat(out); !os.IsNotExist(err) {
		t.Fatalf("the sink wrote %s during a preview", out)
	}
	if runs, _ := s.ListRunsByPipeline(p.ID, 10); len(runs) != 0 {
		t.Fatalf("a preview left %d runs in the pipeline's history", len(runs))
	}
	if n := atomic.LoadInt32(hooks); n != 0 {
		t.Fatalf("a preview called the pipeline's hooks %d times", n)
	}

	// DryRun, behind the editor's Preview, is the same preview.
	res, err := e.DryRun(p, 10)
	if err != nil || res["dst"] == nil || res["dst"].Status != PreviewHeld {
		t.Fatalf("DryRun = %v, %v", res, err)
	}
	if _, err := os.Stat(out); !os.IsNotExist(err) {
		t.Fatal("DryRun wrote the sink")
	}
	if runs, _ := s.ListRunsByPipeline(p.ID, 10); len(runs) != 0 {
		t.Fatal("DryRun left a run")
	}
}

// A failing preview reports the error and raises no alert.
func TestAFailingPreviewRaisesNothing(t *testing.T) {
	e, s, p, _, hooks := previewBed(t, filepath.Join(t.TempDir(), "missing.csv"))
	pv, err := e.PreviewPipeline(context.Background(), p, 10)
	if err != nil {
		t.Fatal(err)
	}
	if pv.Error == "" || pv.Nodes[0].Status != string(models.RunStatusFailed) {
		t.Fatalf("preview = %+v, want the source failed and the error reported", pv)
	}
	if alerts, _ := s.ListAlerts("", false, 10); len(alerts) != 0 {
		t.Fatalf("a failing preview raised %d alerts", len(alerts))
	}
	if n := atomic.LoadInt32(hooks); n != 0 {
		t.Fatalf("a failing preview called hooks %d times", n)
	}
}

// What a preview runs and holds, by node.
func TestPreviewHoldsWhatMayWriteOutside(t *testing.T) {
	for _, c := range []struct {
		n    models.Node
		runs bool
	}{
		{models.Node{Type: models.NodeTypeSourceDB}, true},
		{models.Node{Type: models.NodeTypeSourceAPI}, true},
		{models.Node{Type: models.NodeTypeCode}, true},
		{models.Node{Type: models.NodeTypeJoin}, true},
		{models.Node{Type: models.NodeTypeCode, Capabilities: []string{models.CapabilitySink}}, false},
		{models.Node{Type: models.NodeTypeSinkDB}, false},
		{models.Node{Type: models.NodeTypeSinkAPI}, false},
		{models.Node{Type: models.NodeTypeNotify}, false},
		{models.Node{Type: models.NodeTypeMigrate}, false},
		{models.Node{Type: models.NodeTypeBash}, false},
		{models.Node{Type: models.NodeTypeDBT}, false},
		{models.Node{Type: models.NodeTypeTask}, false},
		{models.Node{Type: models.NodeTypeWait}, false},
		{models.Node{Type: "something_new"}, false},
	} {
		if runs, _ := previewRuns(c.n); runs != c.runs {
			t.Errorf("previewRuns(%s %v) = %v, want %v", c.n.Type, c.n.Capabilities, runs, c.runs)
		}
	}
}
