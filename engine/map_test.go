package engine

// Tests for the `map` block (ADR-050 phase 1, #967): parsing, keys taken
// from an item column, parallel local execution, the three failure
// policies, and the refusal of `map` on node types that cannot fan out.

import (
	"context"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Tnsor-Labs/brokoli/models"
	"github.com/Tnsor-Labs/brokoli/pkg/common"
	"github.com/Tnsor-Labs/brokoli/store"
)

func mapNode(block map[string]interface{}) models.Node {
	return models.Node{ID: "fan", Name: "Fan", Type: models.NodeTypeCode, Config: map[string]interface{}{"script": "x", "map": block}}
}

func TestParseMapConfigForms(t *testing.T) {
	cfg, err := parseExpansionConfig(mapNode(map[string]interface{}{"over": "src"}))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Over["item"] != "src" || cfg.MaxInstances != defaultMaxExpansionInstances || cfg.OnItemFailure != onItemFailureFail || cfg.KeyColumn != "" {
		t.Fatalf("defaults wrong: %+v", cfg)
	}
	if got := cfg.parallelism(false); got != defaultMapMaxParallel {
		t.Fatalf("local parallelism = %d, want %d", got, defaultMapMaxParallel)
	}

	cfg, err = parseExpansionConfig(mapNode(map[string]interface{}{
		"over": map[string]interface{}{"file": "files"}, "key": "path",
		"max_items": float64(10), "max_parallel": float64(64), "on_item_failure": "continue",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Over["file"] != "files" || cfg.KeyColumn != "path" || cfg.MaxInstances != 10 || cfg.OnItemFailure != onItemFailureContinue {
		t.Fatalf("explicit settings wrong: %+v", cfg)
	}
	if cfg.parallelism(false) != 64 || cfg.parallelism(true) != maxConcurrentRemoteExpansionDispatch {
		t.Fatalf("parallelism local=%d remote=%d", cfg.parallelism(false), cfg.parallelism(true))
	}

	legacy := models.Node{ID: "fan", Name: "Fan", Type: models.NodeTypeCode, Config: map[string]interface{}{
		"script": "x", "expansion": map[string]interface{}{"over": map[string]interface{}{"f": "src"}},
	}}
	cfg, err = parseExpansionConfig(legacy)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.parallelism(false) != 1 || cfg.OnItemFailure != onItemFailureFail {
		t.Fatalf("expansion must keep sequential local items and fail-fast: %+v", cfg)
	}
}

func TestParseMapConfigRefusesWhatItCannotHonour(t *testing.T) {
	for name, tc := range map[string]struct {
		node models.Node
		want string
	}{
		"not an object":   {models.Node{ID: "fan", Name: "Fan", Type: models.NodeTypeCode, Config: map[string]interface{}{"map": "src"}}, "'map' must be an object"},
		"no over":         {mapNode(map[string]interface{}{}), "'map.over' is required"},
		"empty over":      {mapNode(map[string]interface{}{"over": ""}), "must name an upstream node"},
		"unknown setting": {mapNode(map[string]interface{}{"over": "src", "parallel": float64(2)}), "'map.parallel' is not a map setting"},
		"zero items":      {mapNode(map[string]interface{}{"over": "src", "max_items": float64(0)}), "'map.max_items' must be a whole number"},
		"fractional":      {mapNode(map[string]interface{}{"over": "src", "max_parallel": 1.5}), "'map.max_parallel' must be a whole number"},
		"bad policy":      {mapNode(map[string]interface{}{"over": "src", "on_item_failure": "ignore"}), "must be fail, continue or skip"},
		"blank key":       {mapNode(map[string]interface{}{"over": "src", "key": " "}), "'map.key' must be the name"},
		"both blocks": {models.Node{ID: "fan", Name: "Fan", Type: models.NodeTypeCode, Config: map[string]interface{}{
			"map": map[string]interface{}{"over": "src"}, "expansion": map[string]interface{}{"over": map[string]interface{}{"f": "src"}},
		}}, "cannot both be set"},
	} {
		if _, err := parseExpansionConfig(tc.node); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: err = %v, want it to contain %q", name, err, tc.want)
		}
	}
}

func TestInstanceKeysFromAKeyColumn(t *testing.T) {
	node := mapNode(map[string]interface{}{"over": "src", "key": "table"})
	cfg, _ := parseExpansionConfig(node)
	items := []common.DataRow{{"table": "orders"}, {"table": int64(7)}}
	keys, err := instanceKeys(node, cfg, items)
	if err != nil {
		t.Fatal(err)
	}
	if keys[0] != "key:orders" || keys[1] != "key:7" {
		t.Fatalf("keys = %v", keys)
	}
	for name, tc := range map[string]struct {
		items []common.DataRow
		want  string
	}{
		"duplicate": {[]common.DataRow{{"table": "a"}, {"table": "b"}, {"table": "a"}}, `items 1 and 3 have the same key "a"`},
		"missing":   {[]common.DataRow{{"table": "a"}, {"other": "b"}}, "item 2 has no value in key column"},
		"null":      {[]common.DataRow{{"table": nil}}, "item 1 has no value in key column"},
		"too long":  {[]common.DataRow{{"table": strings.Repeat("x", maxKeyLength+1)}}, "over the 256 a key may have"},
	} {
		if _, err := instanceKeys(node, cfg, tc.items); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: err = %v, want it to contain %q", name, err, tc.want)
		}
	}
	plain, _ := parseExpansionConfig(mapNode(map[string]interface{}{"over": "src"}))
	keys, _ = instanceKeys(node, plain, items)
	if keys[0] != "idx:0" || keys[1] != "idx:1" {
		t.Fatalf("index keys = %v", keys)
	}
}

// `map` on a type that cannot fan out is refused at validation, so the
// node never runs once over its whole input as if the block were absent.
func TestMapIsRefusedOnTypesThatCannotFanOut(t *testing.T) {
	n := models.Node{ID: "src", Name: "Src", Type: models.NodeTypeSourceDB, Config: map[string]interface{}{
		"uri": "sqlite://x.db", "query": "SELECT 1", "map": map[string]interface{}{"over": "up"},
	}}
	if err := mapUnsupportedError(n); err == nil || !strings.Contains(err.Error(), "only code nodes can map") {
		t.Fatalf("mapUnsupportedError = %v", err)
	}
	ve := &ValidationError{}
	validateNodeConfig(n, ve)
	if !strings.Contains(ve.Error(), "'map' is not supported on source_db nodes") {
		t.Fatalf("validation did not refuse map on source_db: %v", ve.Error())
	}
	r := &NodeValidationResult{}
	validateNodeConfigDetailed(n, r)
	if !strings.Contains(strings.Join(r.Errors, "\n"), "'map' is not supported on source_db nodes") {
		t.Fatalf("detailed validation did not refuse map on source_db: %v", r.Errors)
	}
	if mapUnsupportedError(mapNode(map[string]interface{}{"over": "up"})) != nil {
		t.Fatal("map on a code node refused")
	}
}

// mapPipeline is files -> fan (code with the given map block) -> sink.
func mapPipeline(t *testing.T, id, filesCSV, outPath, script string, block map[string]interface{}) *models.Pipeline {
	t.Helper()
	nodes := []models.Node{
		{ID: "files", Type: models.NodeTypeSourceFile, Name: "Files", Config: map[string]interface{}{"path": filesCSV, "format": "csv"}},
		{ID: "fan", Type: models.NodeTypeCode, Name: "Fan", Config: map[string]interface{}{"script": script, "timeout": float64(60), "map": block}},
	}
	edges := []models.Edge{{From: "files", To: "fan"}}
	if outPath != "" {
		nodes = append(nodes, models.Node{ID: "sink", Type: models.NodeTypeSinkFile, Name: "Out", Config: map[string]interface{}{"path": outPath, "format": "csv"}})
		edges = append(edges, models.Edge{From: "fan", To: "sink"})
	}
	return &models.Pipeline{ID: id, Name: id, Nodes: nodes, Edges: edges, CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC()}
}

// timingScript records when each item started and finished, so a test can
// count how many ran at the same time.
const timingScript = `
import time
start = time.time()
time.sleep(0.6)
output_data = {"columns": ["path", "start", "end"], "rows": [{"path": rows[0]["path"], "start": start, "end": time.time()}]}
`

// maxOverlap is the largest number of [start, end] intervals open at once.
func maxOverlap(rows []common.DataRow) int {
	type ev struct {
		at    float64
		delta int
	}
	var evs []ev
	for _, r := range rows {
		evs = append(evs, ev{mapTestFloat(r["start"]), 1}, ev{mapTestFloat(r["end"]), -1})
	}
	sort.Slice(evs, func(i, j int) bool {
		if evs[i].at == evs[j].at {
			return evs[i].delta < evs[j].delta
		}
		return evs[i].at < evs[j].at
	})
	open, most := 0, 0
	for _, e := range evs {
		open += e.delta
		if open > most {
			most = open
		}
	}
	return most
}

func mapTestFloat(v interface{}) float64 {
	switch x := v.(type) {
	case float64:
		return x
	case int64:
		return float64(x)
	case string:
		f, _ := strconv.ParseFloat(x, 64)
		return f
	}
	return 0
}

func runMapPipeline(t *testing.T, name string, block map[string]interface{}, csv, script string) (*models.Run, *store.SQLiteStore, []common.DataRow, error) {
	t.Helper()
	real := newExpansionTestStore(t, name).(*store.SQLiteStore)
	dir := t.TempDir()
	filesCSV := writeCSV(t, dir, "files.csv", csv)
	outPath := filepath.Join(dir, "out.csv")
	p := mapPipeline(t, name, filesCSV, outPath, script, block)
	if err := real.CreatePipeline(p); err != nil {
		t.Fatal(err)
	}
	eng := drainEngineOnCleanup(t, NewEngine(real))
	run, err := eng.RunPipeline(p.ID)
	var rows []common.DataRow
	if ds, readErr := readCSVFile(outPath); readErr == nil {
		rows = ds.Rows
	}
	return run, real, rows, err
}

// Six items of 0.6s each with max_parallel 3 run three at a time: never
// more than three, and more than one, so the node takes about 1.2s, not
// 3.6s. The `expansion` form of the same node still runs one at a time.
func TestMapRunsLocalItemsInParallelUpToMaxParallel(t *testing.T) {
	csv := "path\na\nb\nc\nd\ne\nf\n"
	run, _, rows, err := runMapPipeline(t, "map-parallel", map[string]interface{}{"over": "files", "key": "path", "max_parallel": float64(3)}, csv, timingScript)
	if err != nil || run.Status != models.RunStatusSuccess {
		t.Fatalf("run = %v, err = %v", run, err)
	}
	if len(rows) != 6 {
		t.Fatalf("output rows = %d, want 6", len(rows))
	}
	if got := maxOverlap(rows); got < 2 || got > 3 {
		t.Fatalf("items running at once = %d, want 2 or 3 (max_parallel 3)", got)
	}
}

func TestExpansionBlockStillRunsLocalItemsOneAtATime(t *testing.T) {
	real := newExpansionTestStore(t, "expansion-sequential").(*store.SQLiteStore)
	dir := t.TempDir()
	filesCSV := writeCSV(t, dir, "files.csv", "path\na\nb\nc\n")
	outPath := filepath.Join(dir, "out.csv")
	p := mapPipeline(t, "expansion-sequential", filesCSV, outPath, timingScript, nil)
	delete(p.Nodes[1].Config, "map")
	p.Nodes[1].Config["expansion"] = map[string]interface{}{"over": map[string]interface{}{"file": "files"}}
	if err := real.CreatePipeline(p); err != nil {
		t.Fatal(err)
	}
	eng := drainEngineOnCleanup(t, NewEngine(real))
	if run, err := eng.RunPipeline(p.ID); err != nil || run.Status != models.RunStatusSuccess {
		t.Fatalf("run = %v, err = %v", run, err)
	}
	ds, err := readCSVFile(outPath)
	if err != nil {
		t.Fatal(err)
	}
	if got := maxOverlap(ds.Rows); got != 1 {
		t.Fatalf("expansion items running at once = %d, want 1", got)
	}
}

// failingScript fails the item whose path starts with "bad".
const failingScript = `
import sys
if rows[0]["path"].startswith("bad"):
    print("boom", file=sys.stderr)
    sys.exit(1)
output_data = {"columns": ["path"], "rows": [{"path": rows[0]["path"]}]}
`

func instanceStatuses(t *testing.T, real *store.SQLiteStore, runID string) map[string]models.RunStatus {
	t.Helper()
	instances, err := real.ListExpansionInstancesByRun(runID)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]models.RunStatus{}
	for _, inst := range instances {
		out[inst.InstanceKey] = inst.Status
	}
	return out
}

func TestMapOnItemFailureContinueRunsEveryItemThenFails(t *testing.T) {
	run, real, _, err := runMapPipeline(t, "map-continue", map[string]interface{}{"over": "files", "key": "path", "max_parallel": float64(1), "on_item_failure": "continue", "max_items": float64(10)},
		"path\nbad1\na\nbad2\nb\n", failingScript)
	if err == nil || run == nil || run.Status != models.RunStatusFailed {
		t.Fatalf("run = %v, err = %v; want a failed run", run, err)
	}
	if !strings.Contains(err.Error(), "2 of 4 expansion instances failed (key:bad1, key:bad2)") {
		t.Fatalf("error does not name the failed items: %v", err)
	}
	got := instanceStatuses(t, real, run.ID)
	want := map[string]models.RunStatus{"key:bad1": models.RunStatusFailed, "key:a": models.RunStatusSuccess, "key:bad2": models.RunStatusFailed, "key:b": models.RunStatusSuccess}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("instance %s status = %q, want %q (all: %v)", k, got[k], v, got)
		}
	}
}

func TestMapOnItemFailureFailStopsStartingItems(t *testing.T) {
	run, real, _, err := runMapPipeline(t, "map-fail", map[string]interface{}{"over": "files", "key": "path", "max_parallel": float64(1)},
		"path\nbad1\na\nb\n", failingScript)
	if err == nil || run == nil || run.Status != models.RunStatusFailed {
		t.Fatalf("run = %v, err = %v; want a failed run", run, err)
	}
	got := instanceStatuses(t, real, run.ID)
	if got["key:bad1"] != models.RunStatusFailed {
		t.Fatalf("bad1 status = %q (all: %v)", got["key:bad1"], got)
	}
	if _, ran := got["key:a"]; ran {
		t.Fatalf("item a started after the first failure under on_item_failure=fail: %v", got)
	}
}

func TestMapOnItemFailureSkipLeavesFailedItemsOut(t *testing.T) {
	run, _, rows, err := runMapPipeline(t, "map-skip", map[string]interface{}{"over": "files", "key": "path", "on_item_failure": "skip"},
		"path\na\nbad1\nb\n", failingScript)
	if err != nil || run.Status != models.RunStatusSuccess {
		t.Fatalf("run = %v, err = %v; want success", run, err)
	}
	var paths []string
	for _, r := range rows {
		paths = append(paths, r["path"].(string))
	}
	sort.Strings(paths)
	if strings.Join(paths, ",") != "a,b" {
		t.Fatalf("output paths = %v, want a,b", paths)
	}
}

// Skipping every item would turn a broken node into an empty success.
func TestMapOnItemFailureSkipFailsWhenNoItemSucceeds(t *testing.T) {
	run, _, _, err := runMapPipeline(t, "map-skip-all", map[string]interface{}{"over": "files", "on_item_failure": "skip"},
		"path\nbad1\nbad2\n", failingScript)
	if err == nil || run == nil || run.Status != models.RunStatusFailed {
		t.Fatalf("run = %v, err = %v; want a failed run when every item failed", run, err)
	}
}

// A stored pipeline can predate the validation check. Running one whose
// non-code node carries `map` fails naming the problem instead of running
// the node once over its whole input.
func TestRunRefusesMapOnTypesThatCannotFanOut(t *testing.T) {
	real := newExpansionTestStore(t, "map-refused-at-run").(*store.SQLiteStore)
	dir := t.TempDir()
	filesCSV := writeCSV(t, dir, "files.csv", "path\na\n")
	p := &models.Pipeline{ID: "map-refused", Name: "map-refused", Nodes: []models.Node{
		{ID: "files", Type: models.NodeTypeSourceFile, Name: "Files", Config: map[string]interface{}{
			"path": filesCSV, "format": "csv", "map": map[string]interface{}{"over": "elsewhere"},
		}},
	}, CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC()}
	if err := real.CreatePipeline(p); err != nil {
		t.Fatal(err)
	}
	eng := drainEngineOnCleanup(t, NewEngine(real))
	run, err := eng.RunPipeline(p.ID)
	if err == nil || (run != nil && run.Status != models.RunStatusFailed) {
		t.Fatalf("run = %v, err = %v; want the run refused", run, err)
	}
	if !strings.Contains(err.Error(), "'map' is not supported on source_file nodes") {
		t.Fatalf("error does not name the refused map: %v", err)
	}
}

// A worker runs nodes it was dispatched without validating the pipeline
// again, so the node handler refuses the block itself, before the
// source_file handler would read the file once over.
func TestRunNodeLogicRefusesMapOnTypesThatCannotFanOut(t *testing.T) {
	dir := t.TempDir()
	filesCSV := writeCSV(t, dir, "files.csv", "path\na\n")
	r := &Runner{pipe: &models.Pipeline{}}
	node := models.Node{ID: "files", Name: "Files", Type: models.NodeTypeSourceFile, Config: map[string]interface{}{
		"path": filesCSV, "format": "csv", "map": map[string]interface{}{"over": "elsewhere"},
	}}
	_, err := r.runNodeLogic(node, nil, nil, nil, nil, nil, 0, "", context.Background(), 0)
	if err == nil || !strings.Contains(err.Error(), "'map' is not supported on source_file nodes") {
		t.Fatalf("runNodeLogic error = %v, want the map refused", err)
	}
}
