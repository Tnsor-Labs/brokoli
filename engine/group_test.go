package engine

// Tests for a mapped `group` node (ADR-050 phase 2, #968): a real run of a
// group whose subgraph transforms each item, both collect modes, and the
// validator accepting a sound group while refusing each shape PR 1 does not
// run yet -- both directions, as CONTRIBUTING requires.

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Tnsor-Labs/brokoli/models"
	"github.com/Tnsor-Labs/brokoli/store"
)

// A subgraph entry node: doubles n and passes path through.
const groupDoubleScript = `
output_data = {"columns": ["path", "n"], "rows": [{"path": rows[0]["path"], "n": int(rows[0]["n"]) * 2}]}
`

// A subgraph terminal node: marks the row it received, proving the entry's
// output threaded into it along the inner edge.
const groupMarkScript = `
output_data = {"columns": ["path", "n", "seen"], "rows": [{"path": rows[0]["path"], "n": rows[0]["n"], "seen": 1}]}
`

// groupPipeline builds: a CSV source -> a mapped group (two-node subgraph:
// double then mark) -> an optional CSV sink. ir_version is 2.3.
func groupPipeline(t *testing.T, id, filesCSV, outPath, collect string) *models.Pipeline {
	t.Helper()
	inner := []models.Node{
		{ID: "dbl", Type: models.NodeTypeCode, Name: "Double", Config: map[string]interface{}{"script": groupDoubleScript, "timeout": float64(60)}},
		{ID: "mark", Type: models.NodeTypeCode, Name: "Mark", Config: map[string]interface{}{"script": groupMarkScript, "timeout": float64(60)}},
	}
	innerEdges := []models.Edge{{From: "dbl", To: "mark"}}
	nodes := []models.Node{
		{ID: "files", Type: models.NodeTypeSourceFile, Name: "Files", Config: map[string]interface{}{"path": filesCSV, "format": "csv"}},
		{
			ID: "grp", Type: models.NodeTypeGroup, Name: "Per row",
			Config:  map[string]interface{}{"map": map[string]interface{}{"over": "files", "key": "path"}},
			Collect: collect,
			Nodes:   inner,
			Edges:   innerEdges,
		},
	}
	edges := []models.Edge{{From: "files", To: "grp"}}
	if outPath != "" {
		nodes = append(nodes, models.Node{ID: "sink", Type: models.NodeTypeSinkFile, Name: "Out", Config: map[string]interface{}{"path": outPath, "format": "csv"}})
		edges = append(edges, models.Edge{From: "grp", To: "sink"})
	}
	return &models.Pipeline{ID: id, Name: id, IRVersion: models.IRGroupsVersion, Nodes: nodes, Edges: edges, CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC()}
}

func TestGroupMapsSubgraphOverItemsAndUnions(t *testing.T) {
	real := newExpansionTestStore(t, "group-union").(*store.SQLiteStore)
	dir := t.TempDir()
	filesCSV := writeCSV(t, dir, "files.csv", "path,n\na,1\nb,2\nc,3\n")
	outPath := filepath.Join(dir, "out.csv")
	p := groupPipeline(t, "group-union", filesCSV, outPath, models.CollectUnion)
	if err := real.CreatePipeline(p); err != nil {
		t.Fatal(err)
	}
	eng := drainEngineOnCleanup(t, NewEngine(real))
	run, err := eng.RunPipeline(p.ID)
	if err != nil || run.Status != models.RunStatusSuccess {
		t.Fatalf("run = %v, err = %v", run, err)
	}
	ds, err := readCSVFile(outPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(ds.Rows) != 3 {
		t.Fatalf("collect:union output rows = %d, want 3 (one per item)", len(ds.Rows))
	}
	// Each item ran the whole subgraph: n doubled (Double) and seen set
	// (Mark, fed Double's output), unioned across the three items.
	got := map[string]string{}
	for _, row := range ds.Rows {
		got[toStr(row["path"])] = toStr(row["n"]) + "/" + toStr(row["seen"])
	}
	want := map[string]string{"a": "2/1", "b": "4/1", "c": "6/1"}
	for path, w := range want {
		if got[path] != w {
			t.Errorf("item %q => n/seen %q, want %q (all: %v)", path, got[path], w, got)
		}
	}

	// One durable per-item instance was recorded, keyed by the path column.
	instances, err := real.ListExpansionInstancesByRun(run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(instances) != 3 {
		t.Fatalf("expansion instances = %d, want 3", len(instances))
	}
}

func TestGroupCollectNoneYieldsEmptyButStillRuns(t *testing.T) {
	real := newExpansionTestStore(t, "group-none").(*store.SQLiteStore)
	dir := t.TempDir()
	filesCSV := writeCSV(t, dir, "files.csv", "path,n\na,1\nb,2\n")
	outPath := filepath.Join(dir, "out.csv")
	p := groupPipeline(t, "group-none", filesCSV, outPath, models.CollectNone)
	if err := real.CreatePipeline(p); err != nil {
		t.Fatal(err)
	}
	eng := drainEngineOnCleanup(t, NewEngine(real))
	run, err := eng.RunPipeline(p.ID)
	if err != nil || run.Status != models.RunStatusSuccess {
		t.Fatalf("run = %v, err = %v", run, err)
	}
	// collect:none yields an empty dataset, so the sink writes an empty
	// (headerless) file; readCSVFile reports EOF for that, which is zero
	// rows, not a failure.
	if ds, err := readCSVFile(outPath); err == nil && len(ds.Rows) != 0 {
		t.Fatalf("collect:none output rows = %d, want 0", len(ds.Rows))
	}
	// The subgraph still ran for every item: both instances recorded.
	instances, err := real.ListExpansionInstancesByRun(run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(instances) != 2 {
		t.Fatalf("collect:none instances = %d, want 2 (the subgraph still ran)", len(instances))
	}
}

func TestGroupValidationAcceptsSoundAndRefusesUnsupported(t *testing.T) {
	sound := func() models.Node {
		return models.Node{
			ID: "grp", Type: models.NodeTypeGroup, Name: "G",
			Config:  map[string]interface{}{"map": map[string]interface{}{"over": "src"}},
			Collect: models.CollectUnion,
			Nodes: []models.Node{
				{ID: "a", Type: models.NodeTypeCode, Name: "A", Config: map[string]interface{}{"script": "x"}},
				{ID: "b", Type: models.NodeTypeCode, Name: "B", Config: map[string]interface{}{"script": "x"}},
			},
			Edges: []models.Edge{{From: "a", To: "b"}},
		}
	}

	// Accept: a sound group produces no messages.
	if errs := mappedGroupErrors(sound()); len(errs) != 0 {
		t.Fatalf("sound group rejected: %v", errs)
	}

	// Accept: a group whose subgraph routes on a condition (entry condition,
	// two branches reconverging at one terminal, conditional edges leaving
	// the condition) is sound -- per-item routing is supported now.
	tru, fls := true, false
	conditional := models.Node{
		ID: "grp", Type: models.NodeTypeGroup, Name: "G",
		Config:  map[string]interface{}{"map": map[string]interface{}{"over": "src"}},
		Collect: models.CollectUnion,
		Nodes: []models.Node{
			{ID: "chk", Type: models.NodeTypeCondition, Name: "Chk", Config: map[string]interface{}{"expression": "always_true"}},
			{ID: "hi", Type: models.NodeTypeCode, Name: "Hi", Config: map[string]interface{}{"script": "x"}},
			{ID: "lo", Type: models.NodeTypeCode, Name: "Lo", Config: map[string]interface{}{"script": "x"}},
			{ID: "merge", Type: models.NodeTypeCode, Name: "Merge", Config: map[string]interface{}{"script": "x"}},
		},
		Edges: []models.Edge{
			{From: "chk", To: "hi", Condition: &tru},
			{From: "chk", To: "lo", Condition: &fls},
			{From: "hi", To: "merge"},
			{From: "lo", To: "merge"},
		},
	}
	if errs := mappedGroupErrors(conditional); len(errs) != 0 {
		t.Fatalf("sound conditional group rejected: %v", errs)
	}

	cases := map[string]struct {
		mutate func(n *models.Node)
		want   string
	}{
		"no map block":   {func(n *models.Node) { delete(n.Config, "map") }, "needs a 'map' block"},
		"collect list":   {func(n *models.Node) { n.Collect = models.CollectList }, "collect:\"list\" is not supported"},
		"bad collect":    {func(n *models.Node) { n.Collect = "zip" }, "collect must be"},
		"empty subgraph": {func(n *models.Node) { n.Nodes = nil }, "at least one inner node"},
		"routing label not from condition": {func(n *models.Node) {
			tru := true
			n.Edges = []models.Edge{{From: "a", To: "b", Condition: &tru}}
		}, "does not leave a condition node"},
		"nested group": {func(n *models.Node) { n.Nodes[1].Type = models.NodeTypeGroup }, "group inside a group"},
		"inner maps": {func(n *models.Node) {
			n.Nodes[1].Config = map[string]interface{}{"map": map[string]interface{}{"over": "a"}}
		}, "cannot itself map"},
		"unknown inner edge": {func(n *models.Node) { n.Edges = []models.Edge{{From: "a", To: "ghost"}} }, "unknown node"},
		"two terminals":      {func(n *models.Node) { n.Edges = nil }, "exactly one terminal"},
	}
	for name, tc := range cases {
		n := sound()
		tc.mutate(&n)
		errs := mappedGroupErrors(n)
		if !containsSubstr(errs, tc.want) {
			t.Errorf("%s: errors = %v, want one containing %q", name, errs, tc.want)
		}
	}
}

// A subgraph that tags each item's row with the branch it was routed down.
const groupBranchHi = `
output_data = {"columns": ["path", "n", "branch"], "rows": [{"path": rows[0]["path"], "n": rows[0]["n"], "branch": "hi"}]}
`
const groupBranchLo = `
output_data = {"columns": ["path", "n", "branch"], "rows": [{"path": rows[0]["path"], "n": rows[0]["n"], "branch": "lo"}]}
`
const groupPassThrough = `
output_data = {"columns": ["path", "n", "branch"], "rows": rows}
`

// condGroupPipeline builds a CSV source -> mapped group -> CSV sink, where
// the group's subgraph is the given inner nodes/edges.
func condGroupPipeline(t *testing.T, id, filesCSV, outPath string, inner []models.Node, innerEdges []models.Edge) *models.Pipeline {
	t.Helper()
	nodes := []models.Node{
		{ID: "files", Type: models.NodeTypeSourceFile, Name: "Files", Config: map[string]interface{}{"path": filesCSV, "format": "csv"}},
		{
			ID: "grp", Type: models.NodeTypeGroup, Name: "Route per row",
			Config:  map[string]interface{}{"map": map[string]interface{}{"over": "files", "key": "path"}},
			Collect: models.CollectUnion,
			Nodes:   inner,
			Edges:   innerEdges,
		},
		{ID: "sink", Type: models.NodeTypeSinkFile, Name: "Out", Config: map[string]interface{}{"path": outPath, "format": "csv"}},
	}
	edges := []models.Edge{{From: "files", To: "grp"}, {From: "grp", To: "sink"}}
	return &models.Pipeline{ID: id, Name: id, IRVersion: models.IRGroupsVersion, Nodes: nodes, Edges: edges, CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC()}
}

// A condition inside a group routes each item down one of two branches that
// reconverge at the terminal: the taken branch runs, the other is skipped,
// and the terminal sees exactly the active branch's output. Each item is
// routed on its own value, proving the routing is per item, not per run.
func TestGroupConditionRoutesEachItemAndReconverges(t *testing.T) {
	real := newExpansionTestStore(t, "group-diamond").(*store.SQLiteStore)
	dir := t.TempDir()
	filesCSV := writeCSV(t, dir, "files.csv", "path,n\na,1\nb,2\nc,3\nd,5\n")
	outPath := filepath.Join(dir, "out.csv")
	tru, fls := true, false
	inner := []models.Node{
		{ID: "chk", Type: models.NodeTypeCondition, Name: "n>=3?", Config: map[string]interface{}{"expression": `max("n") >= 3`}},
		{ID: "hi", Type: models.NodeTypeCode, Name: "Hi", Config: map[string]interface{}{"script": groupBranchHi, "timeout": float64(60)}},
		{ID: "lo", Type: models.NodeTypeCode, Name: "Lo", Config: map[string]interface{}{"script": groupBranchLo, "timeout": float64(60)}},
		{ID: "merge", Type: models.NodeTypeCode, Name: "Merge", Config: map[string]interface{}{"script": groupPassThrough, "timeout": float64(60)}},
	}
	innerEdges := []models.Edge{
		{From: "chk", To: "hi", Condition: &tru},
		{From: "chk", To: "lo", Condition: &fls},
		{From: "hi", To: "merge"},
		{From: "lo", To: "merge"},
	}
	p := condGroupPipeline(t, "group-diamond", filesCSV, outPath, inner, innerEdges)
	if err := real.CreatePipeline(p); err != nil {
		t.Fatal(err)
	}
	eng := drainEngineOnCleanup(t, NewEngine(real))
	run, err := eng.RunPipeline(p.ID)
	if err != nil || run.Status != models.RunStatusSuccess {
		t.Fatalf("run = %v, err = %v", run, err)
	}
	ds, err := readCSVFile(outPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(ds.Rows) != 4 {
		t.Fatalf("union output rows = %d, want 4 (every item reconverges at the terminal)", len(ds.Rows))
	}
	got := map[string]string{}
	for _, row := range ds.Rows {
		got[toStr(row["path"])] = toStr(row["branch"])
	}
	// n<3 -> lo, n>=3 -> hi, decided per item.
	want := map[string]string{"a": "lo", "b": "lo", "c": "hi", "d": "hi"}
	for path, w := range want {
		if got[path] != w {
			t.Errorf("item %q routed to %q, want %q (all: %v)", path, got[path], w, got)
		}
	}
}

// A condition with a single branch to the terminal filters items: when it
// routes away, the terminal is skipped and the item contributes nothing to
// the union. So the group is a per-item filter.
func TestGroupConditionFiltersItemsWhenTerminalIsSkipped(t *testing.T) {
	real := newExpansionTestStore(t, "group-filter").(*store.SQLiteStore)
	dir := t.TempDir()
	filesCSV := writeCSV(t, dir, "files.csv", "path,n\na,1\nb,2\nc,3\nd,9\n")
	outPath := filepath.Join(dir, "out.csv")
	tru := true
	inner := []models.Node{
		{ID: "gate", Type: models.NodeTypeCondition, Name: "keep n>=3?", Config: map[string]interface{}{"expression": `max("n") >= 3`}},
		{ID: "keep", Type: models.NodeTypeCode, Name: "Keep", Config: map[string]interface{}{"script": groupBranchHi, "timeout": float64(60)}},
	}
	innerEdges := []models.Edge{{From: "gate", To: "keep", Condition: &tru}}
	p := condGroupPipeline(t, "group-filter", filesCSV, outPath, inner, innerEdges)
	if err := real.CreatePipeline(p); err != nil {
		t.Fatal(err)
	}
	eng := drainEngineOnCleanup(t, NewEngine(real))
	run, err := eng.RunPipeline(p.ID)
	if err != nil || run.Status != models.RunStatusSuccess {
		t.Fatalf("run = %v, err = %v", run, err)
	}
	ds, err := readCSVFile(outPath)
	if err != nil {
		t.Fatal(err)
	}
	// Only c (3) and d (9) pass the gate; a (1) and b (2) are filtered out.
	if len(ds.Rows) != 2 {
		t.Fatalf("filtered union rows = %d, want 2 (only items with n>=3 survive)", len(ds.Rows))
	}
	kept := map[string]bool{}
	for _, row := range ds.Rows {
		kept[toStr(row["path"])] = true
	}
	if !kept["c"] || !kept["d"] || kept["a"] || kept["b"] {
		t.Errorf("kept = %v, want exactly c and d", kept)
	}
	// Every item still ran its gate: four durable instances recorded, even
	// the two that produced nothing.
	instances, err := real.ListExpansionInstancesByRun(run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(instances) != 4 {
		t.Fatalf("instances = %d, want 4 (the gate ran for every item)", len(instances))
	}
}

func TestGroupRefusedBelowIR23(t *testing.T) {
	dir := t.TempDir()
	filesCSV := writeCSV(t, dir, "files.csv", "path,n\na,1\n")
	p := groupPipeline(t, "group-oldir", filesCSV, "", models.CollectUnion)
	p.IRVersion = models.TaskInterfaceIRVersion // 2.2: too old for a group
	ve := ValidatePipeline(p, nil)
	if ve == nil || !strings.Contains(ve.Error(), "requires pipeline IR "+models.IRGroupsVersion) {
		t.Fatalf("a group in a 2.2 pipeline should be refused naming the version; got %v", ve)
	}
}

func toStr(v interface{}) string { return fmt.Sprintf("%v", v) }

func containsSubstr(errs []string, want string) bool {
	for _, e := range errs {
		if strings.Contains(e, want) {
			return true
		}
	}
	return false
}
