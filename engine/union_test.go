package engine

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Tnsor-Labs/brokoli/models"
	"github.com/Tnsor-Labs/brokoli/pkg/common"
	"github.com/Tnsor-Labs/brokoli/store"
)

func TestUnionDatasets_MatchingColumns(t *testing.T) {
	a := &common.DataSet{
		Columns: []string{"id", "name"},
		Rows: []common.DataRow{
			{"id": "1", "name": "Alice"},
			{"id": "2", "name": "Bob"},
		},
	}
	b := &common.DataSet{
		Columns: []string{"id", "name"},
		Rows: []common.DataRow{
			{"id": "3", "name": "Carol"},
		},
	}

	result, err := UnionDatasets([]*common.DataSet{a, b})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result.Rows) != 3 {
		t.Fatalf("expected 3 rows, got %d", len(result.Rows))
	}
	if len(result.Columns) != 2 {
		t.Fatalf("expected 2 columns, got %d: %v", len(result.Columns), result.Columns)
	}
	if result.Rows[0]["name"] != "Alice" || result.Rows[2]["name"] != "Carol" {
		t.Fatalf("unexpected row contents: %+v", result.Rows)
	}
}

func TestUnionDatasets_ThreeInputs(t *testing.T) {
	mk := func(id string) *common.DataSet {
		return &common.DataSet{Columns: []string{"id"}, Rows: []common.DataRow{{"id": id}}}
	}
	result, err := UnionDatasets([]*common.DataSet{mk("a"), mk("b"), mk("c")})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result.Rows) != 3 {
		t.Fatalf("expected 3 rows, got %d", len(result.Rows))
	}
}

// TestUnionDatasets_MismatchedColumns documents and locks in the decided
// behavior for union of differing schemas: the output column set is the
// union of all input columns, and rows from an input missing a given
// column get that column filled with nil (not an error, and not
// restricted to the intersection of columns). See UnionDatasets's doc
// comment in engine/union.go for the full rationale.
func TestUnionDatasets_MismatchedColumns(t *testing.T) {
	a := &common.DataSet{
		Columns: []string{"id", "name"},
		Rows: []common.DataRow{
			{"id": "1", "name": "Alice"},
		},
	}
	b := &common.DataSet{
		Columns: []string{"id", "email"},
		Rows: []common.DataRow{
			{"id": "2", "email": "bob@example.com"},
		},
	}

	result, err := UnionDatasets([]*common.DataSet{a, b})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	wantCols := map[string]bool{"id": true, "name": true, "email": true}
	if len(result.Columns) != len(wantCols) {
		t.Fatalf("expected columns %v, got %v", wantCols, result.Columns)
	}
	for _, c := range result.Columns {
		if !wantCols[c] {
			t.Fatalf("unexpected column %q in result: %v", c, result.Columns)
		}
	}

	if len(result.Rows) != 2 {
		t.Fatalf("expected 2 rows, got %d", len(result.Rows))
	}
	// Row from `a` has no "email" column -> filled with nil, not omitted or erroring.
	if v, ok := result.Rows[0]["email"]; !ok || v != nil {
		t.Errorf("expected row 0 'email' to be present and nil, got %v (present=%v)", v, ok)
	}
	if result.Rows[0]["name"] != "Alice" {
		t.Errorf("expected row 0 'name' = Alice, got %v", result.Rows[0]["name"])
	}
	// Row from `b` has no "name" column -> filled with nil.
	if v, ok := result.Rows[1]["name"]; !ok || v != nil {
		t.Errorf("expected row 1 'name' to be present and nil, got %v (present=%v)", v, ok)
	}
	if result.Rows[1]["email"] != "bob@example.com" {
		t.Errorf("expected row 1 'email' = bob@example.com, got %v", result.Rows[1]["email"])
	}
}

func TestUnionDatasets_FewerThanTwoInputsFails(t *testing.T) {
	a := &common.DataSet{Columns: []string{"id"}, Rows: []common.DataRow{{"id": "1"}}}

	if _, err := UnionDatasets([]*common.DataSet{a}); err == nil {
		t.Fatal("expected error for a single input, got nil")
	}
	if _, err := UnionDatasets(nil); err == nil {
		t.Fatal("expected error for zero inputs, got nil")
	}
	// A nil entry doesn't count toward the minimum.
	if _, err := UnionDatasets([]*common.DataSet{a, nil}); err == nil {
		t.Fatal("expected error when only 1 of 2 slice entries is non-nil, got nil")
	}
}

// TestPipeline_UnionEndToEnd exercises the union node type through the
// real engine dispatch (runner.go's runNodeLogic switch), not just the
// pure UnionDatasets helper — proving that two source_file nodes feeding
// a union node, feeding a sink_file, actually combine both inputs when
// run through a real pipeline. This is also a regression guard: before
// this change, `union` hit runNodeLogic's `default: return input, nil`
// case and would have silently passed through only ONE upstream input.
func TestPipeline_UnionEndToEnd(t *testing.T) {
	dir := t.TempDir()
	real, err := store.NewSQLiteStore(filepath.Join(dir, "union-e2e.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer real.Close()

	csvA := filepath.Join(dir, "a.csv")
	csvB := filepath.Join(dir, "b.csv")
	if err := os.WriteFile(csvA, []byte("id,name\n1,Alice\n2,Bob\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(csvB, []byte("id,name\n3,Carol\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	outPath := filepath.Join(dir, "out.csv")

	pipeline := &models.Pipeline{
		ID:   "union-e2e-pipeline",
		Name: "Union e2e",
		Nodes: []models.Node{
			{ID: "srcA", Type: models.NodeTypeSourceFile, Name: "A", Config: map[string]interface{}{"path": csvA, "format": "csv"}},
			{ID: "srcB", Type: models.NodeTypeSourceFile, Name: "B", Config: map[string]interface{}{"path": csvB, "format": "csv"}},
			{ID: "union1", Type: models.NodeTypeUnion, Name: "Combine", Config: map[string]interface{}{"mode": "union"}},
			{ID: "sink", Type: models.NodeTypeSinkFile, Name: "Out", Config: map[string]interface{}{"path": outPath, "format": "csv"}},
		},
		Edges: []models.Edge{
			{From: "srcA", To: "union1"},
			{From: "srcB", To: "union1"},
			{From: "union1", To: "sink"},
		},
		CreatedAt: time.Now().UTC(),
		UpdatedAt: time.Now().UTC(),
	}
	if err := real.CreatePipeline(pipeline); err != nil {
		t.Fatal(err)
	}

	eng := drainEngineOnCleanup(t, NewEngine(real))
	run, err := eng.RunPipeline(pipeline.ID)
	if err != nil {
		t.Fatalf("RunPipeline failed: %v", err)
	}
	if run.Status != models.RunStatusSuccess {
		t.Fatalf("run status = %s, want success", run.Status)
	}

	outBytes, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatalf("read sink output: %v", err)
	}
	out := string(outBytes)
	for _, want := range []string{"Alice", "Bob", "Carol"} {
		if !strings.Contains(out, want) {
			t.Errorf("expected union output to contain %q, got:\n%s", want, out)
		}
	}
}
