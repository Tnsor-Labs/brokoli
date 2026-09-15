package engine

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Tnsor-Labs/brokoli/models"
	"github.com/Tnsor-Labs/brokoli/store"
)

// The symptom that was reported: a JSON source feeding a CSV sink wrote
// its columns in a different order from one run to the next. Every run
// now writes the file's own order.
func TestAJSONSourceWritesTheSameCSVHeaderEveryRun(t *testing.T) {
	dir := t.TempDir()
	st, err := store.NewSQLiteStore(filepath.Join(dir, "meta.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	eng := drainEngineOnCleanup(t, NewEngine(st))
	in := filepath.Join(dir, "in.json")
	if err := os.WriteFile(in, []byte(`[{"zeta": 1, "alpha": 2, "mid": 3, "beta": 4}]`), 0o644); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(dir, "out.csv")
	p := &models.Pipeline{ID: "json-order", Name: "json-order", Enabled: true,
		Nodes: []models.Node{
			{ID: "src", Name: "src", Type: models.NodeTypeSourceFile, Config: map[string]interface{}{"path": in}},
			{ID: "out", Name: "out", Type: models.NodeTypeSinkFile, Config: map[string]interface{}{"path": out}},
		},
		Edges: []models.Edge{{From: "src", To: "out"}}}
	if err := st.CreatePipeline(p); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 10; i++ {
		run, err := eng.RunPipeline(p.ID)
		if err != nil || run.Status != models.RunStatusSuccess {
			t.Fatalf("run %d: %v %v", i, run, err)
		}
		got, _ := os.ReadFile(out)
		if header := strings.SplitN(string(got), "\n", 2)[0]; header != "zeta,alpha,mid,beta" {
			t.Fatalf("run %d wrote header %q, want the file's order zeta,alpha,mid,beta", i, header)
		}
	}
}
