package engine

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Tnsor-Labs/brokoli/models"
	"github.com/Tnsor-Labs/brokoli/pkg/common"
	"github.com/Tnsor-Labs/brokoli/store"
)

// ── parseExpansionConfig / nodeHasExpansion ──────────────────────────────

func TestNodeHasExpansion(t *testing.T) {
	code := models.Node{Type: models.NodeTypeCode, Config: map[string]interface{}{"expansion": map[string]interface{}{}}}
	if !nodeHasExpansion(code) {
		t.Error("expected code node with expansion config to be recognized")
	}

	plainCode := models.Node{Type: models.NodeTypeCode, Config: map[string]interface{}{"script": "pass"}}
	if nodeHasExpansion(plainCode) {
		t.Error("ordinary code node without expansion config should not be recognized as expansion")
	}

	other := models.Node{Type: models.NodeTypeTransform, Config: map[string]interface{}{"expansion": map[string]interface{}{}}}
	if nodeHasExpansion(other) {
		t.Error("non-code node type should never be recognized as expansion, even with an expansion key present")
	}
}

func TestParseExpansionConfig_Valid(t *testing.T) {
	node := models.Node{
		ID: "n1", Name: "Parse",
		Config: map[string]interface{}{
			"expansion": map[string]interface{}{
				"over": map[string]interface{}{"file": "files_node"},
			},
		},
	}
	cfg, err := parseExpansionConfig(node)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Over["file"] != "files_node" {
		t.Errorf("expected over[file]=files_node, got %v", cfg.Over)
	}
	if cfg.MaxInstances != defaultMaxExpansionInstances {
		t.Errorf("expected default max instances %d, got %d", defaultMaxExpansionInstances, cfg.MaxInstances)
	}
	if cfg.Key != nil {
		t.Errorf("expected no key, got %v", cfg.Key)
	}
}

func TestParseExpansionConfig_WithKeyAndMaxInstances(t *testing.T) {
	node := models.Node{
		ID: "n1", Name: "Parse",
		Config: map[string]interface{}{
			"expansion": map[string]interface{}{
				"over":          map[string]interface{}{"file": "files_node"},
				"key":           map[string]interface{}{"name": "file_key", "doc": "keys by filename"},
				"max_instances": float64(50),
			},
		},
	}
	cfg, err := parseExpansionConfig(node)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Key == nil || cfg.Key.Name != "file_key" {
		t.Errorf("expected key.name=file_key, got %+v", cfg.Key)
	}
	if cfg.MaxInstances != 50 {
		t.Errorf("expected max instances 50, got %d", cfg.MaxInstances)
	}
}

func TestParseExpansionConfig_Malformed(t *testing.T) {
	cases := []struct {
		name string
		cfg  map[string]interface{}
	}{
		{"missing over", map[string]interface{}{"expansion": map[string]interface{}{}}},
		{"over not object", map[string]interface{}{"expansion": map[string]interface{}{"over": "nope"}}},
		{"over empty", map[string]interface{}{"expansion": map[string]interface{}{"over": map[string]interface{}{}}}},
		{"over value not string", map[string]interface{}{"expansion": map[string]interface{}{"over": map[string]interface{}{"file": 5}}}},
		{"expansion not object", map[string]interface{}{"expansion": "nope"}},
		{"max_instances not positive", map[string]interface{}{"expansion": map[string]interface{}{
			"over": map[string]interface{}{"file": "n1"}, "max_instances": float64(0),
		}}},
		{"key not object", map[string]interface{}{"expansion": map[string]interface{}{
			"over": map[string]interface{}{"file": "n1"}, "key": "nope",
		}}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			node := models.Node{ID: "n1", Name: "Parse", Config: c.cfg}
			if _, err := parseExpansionConfig(node); err == nil {
				t.Errorf("expected error for %s", c.name)
			}
		})
	}
}

// ── deriveInstanceKey ─────────────────────────────────────────────────────

func TestDeriveInstanceKey_DeterministicAndDistinct(t *testing.T) {
	if deriveInstanceKey(0) == deriveInstanceKey(1) {
		t.Error("expected distinct keys for distinct indices")
	}
	if deriveInstanceKey(3) != deriveInstanceKey(3) {
		t.Error("expected deterministic key for same index")
	}
}

// ── resolveExpansionItems ─────────────────────────────────────────────────

func TestResolveExpansionItems_SingleCollection(t *testing.T) {
	node := models.Node{ID: "expand1", Name: "Parse"}
	cfg := &expansionConfig{Over: map[string]string{"file": "files"}}
	edgeInputs := map[string]*common.DataSet{
		"files": {
			Columns: []string{"path"},
			Rows: []common.DataRow{
				{"path": "a.csv"},
				{"path": "b.csv"},
				{"path": "c.csv"},
			},
		},
	}
	items, _, err := resolveExpansionItems(node, cfg, edgeInputs)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(items) != 3 {
		t.Fatalf("expected 3 items, got %d", len(items))
	}
	if items[1]["path"] != "b.csv" {
		t.Errorf("expected item 1 path=b.csv, got %v", items[1]["path"])
	}
}

func TestResolveExpansionItems_MissingUpstream(t *testing.T) {
	node := models.Node{ID: "expand1", Name: "Parse"}
	cfg := &expansionConfig{Over: map[string]string{"file": "files"}}
	if _, _, err := resolveExpansionItems(node, cfg, map[string]*common.DataSet{}); err == nil {
		t.Fatal("expected error when the named upstream collection has no resolved input")
	}
}

func TestResolveExpansionItems_MismatchedCounts(t *testing.T) {
	node := models.Node{ID: "expand1", Name: "Parse"}
	cfg := &expansionConfig{Over: map[string]string{"a": "colA", "b": "colB"}}
	edgeInputs := map[string]*common.DataSet{
		"colA": {Columns: []string{"x"}, Rows: []common.DataRow{{"x": 1}, {"x": 2}}},
		"colB": {Columns: []string{"y"}, Rows: []common.DataRow{{"y": 1}}},
	}
	if _, _, err := resolveExpansionItems(node, cfg, edgeInputs); err == nil {
		t.Fatal("expected error for mismatched item counts across named collections")
	}
}

// ── combineExpansionResults ───────────────────────────────────────────────

func TestCombineExpansionResults_Empty(t *testing.T) {
	result, err := combineExpansionResults(nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result.Rows) != 0 {
		t.Errorf("expected 0 rows, got %d", len(result.Rows))
	}
}

func TestCombineExpansionResults_Single(t *testing.T) {
	ds := &common.DataSet{Columns: []string{"id"}, Rows: []common.DataRow{{"id": "1"}}}
	result, err := combineExpansionResults([]*common.DataSet{ds})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != ds {
		t.Error("expected single result returned as-is")
	}
}

func TestCombineExpansionResults_Multiple(t *testing.T) {
	a := &common.DataSet{Columns: []string{"id"}, Rows: []common.DataRow{{"id": "1"}}}
	b := &common.DataSet{Columns: []string{"id"}, Rows: []common.DataRow{{"id": "2"}}}
	result, err := combineExpansionResults([]*common.DataSet{a, b})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result.Rows) != 2 {
		t.Errorf("expected 2 rows, got %d", len(result.Rows))
	}
}

// ── validate.go integration ───────────────────────────────────────────────

func TestValidate_ExpansionNode_Valid(t *testing.T) {
	p := &models.Pipeline{
		Name: "expand-valid",
		Nodes: []models.Node{
			{ID: "files", Type: models.NodeTypeSourceFile, Name: "Files", Config: map[string]interface{}{"path": "/data/x.csv"}},
			{ID: "expand1", Type: models.NodeTypeCode, Name: "Parse", Config: map[string]interface{}{
				"script": "pass",
				"expansion": map[string]interface{}{
					"over": map[string]interface{}{"file": "files"},
				},
			}},
			{ID: "sink", Type: models.NodeTypeSinkFile, Name: "Out", Config: map[string]interface{}{"path": "/out.csv"}},
		},
		Edges: []models.Edge{{From: "files", To: "expand1"}, {From: "expand1", To: "sink"}},
	}
	ve := ValidatePipeline(p)
	if ve.HasErrors() {
		t.Errorf("expected no errors, got: %v", ve.Errors)
	}
}

func TestValidate_ExpansionNode_MalformedConfigRejected(t *testing.T) {
	p := &models.Pipeline{
		Name: "expand-bad",
		Nodes: []models.Node{
			{ID: "files", Type: models.NodeTypeSourceFile, Name: "Files", Config: map[string]interface{}{"path": "/data/x.csv"}},
			{ID: "expand1", Type: models.NodeTypeCode, Name: "Parse", Config: map[string]interface{}{
				"script":    "pass",
				"expansion": map[string]interface{}{}, // missing 'over'
			}},
			{ID: "sink", Type: models.NodeTypeSinkFile, Name: "Out", Config: map[string]interface{}{"path": "/out.csv"}},
		},
		Edges: []models.Edge{{From: "files", To: "expand1"}, {From: "expand1", To: "sink"}},
	}
	ve := ValidatePipeline(p)
	if !ve.HasErrors() {
		t.Error("expected error for expansion node missing 'over'")
	}
}

func TestValidate_UnionSingleEdge_AllowedWhenFedByExpansion(t *testing.T) {
	p := &models.Pipeline{
		Name: "expand-collect",
		Nodes: []models.Node{
			{ID: "files", Type: models.NodeTypeSourceFile, Name: "Files", Config: map[string]interface{}{"path": "/data/x.csv"}},
			{ID: "expand1", Type: models.NodeTypeCode, Name: "Parse", Config: map[string]interface{}{
				"script": "pass",
				"expansion": map[string]interface{}{
					"over": map[string]interface{}{"file": "files"},
				},
			}},
			{ID: "collect1", Type: models.NodeTypeUnion, Name: "Collected", Config: map[string]interface{}{"mode": "union"}},
			{ID: "sink", Type: models.NodeTypeSinkFile, Name: "Out", Config: map[string]interface{}{"path": "/out.csv"}},
		},
		Edges: []models.Edge{
			{From: "files", To: "expand1"},
			{From: "expand1", To: "collect1"}, // single edge into union — the .collect(mode="union") shape
			{From: "collect1", To: "sink"},
		},
	}
	ve := ValidatePipeline(p)
	if ve.HasErrors() {
		t.Errorf("expected a union node fed by a single expansion node to be valid, got: %v", ve.Errors)
	}
}

func TestValidate_UnionSingleEdge_RejectedWhenNotFedByExpansion(t *testing.T) {
	p := &models.Pipeline{
		Name: "plain-single-union",
		Nodes: []models.Node{
			{ID: "src", Type: models.NodeTypeSourceFile, Name: "Src", Config: map[string]interface{}{"path": "/data/x.csv"}},
			{ID: "union1", Type: models.NodeTypeUnion, Name: "U", Config: map[string]interface{}{"mode": "union"}},
			{ID: "sink", Type: models.NodeTypeSinkFile, Name: "Out", Config: map[string]interface{}{"path": "/out.csv"}},
		},
		Edges: []models.Edge{
			{From: "src", To: "union1"},
			{From: "union1", To: "sink"},
		},
	}
	ve := ValidatePipeline(p)
	if !ve.HasErrors() {
		t.Error("expected the pre-existing >=2-edge union rule to still apply for an ordinary (non-expansion) single upstream")
	}
}

// ── End-to-end pipeline tests ──────────────────────────────────────────────

func newExpansionTestStore(t *testing.T, name string) store.Store {
	t.Helper()
	dir := t.TempDir()
	s, err := store.NewSQLiteStore(filepath.Join(dir, name+".db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

// TestPipeline_ExpandEndToEnd_FansOutPerItem is the primary acceptance-
// criteria test: parse.expand(file=files) must execute the task body once
// per item in a multi-item upstream collection, not once total. Verified
// two ways: (1) the combined output has one row per item (each instance
// tags its own row with its instance index), and (2) the durable
// models.ExpansionInstance rows — one per item, independently tracked —
// match the item count exactly.
func TestPipeline_ExpandEndToEnd_FansOutPerItem(t *testing.T) {
	realStore := newExpansionTestStore(t, "expand-e2e")
	real := realStore.(*store.SQLiteStore)

	dir := t.TempDir()
	filesCSV := writeCSV(t, dir, "files.csv", "path\na.csv\nb.csv\nc.csv\n")
	outPath := filepath.Join(dir, "out.csv")

	pipeline := &models.Pipeline{
		ID:   "expand-e2e-pipeline",
		Name: "Expand e2e",
		Nodes: []models.Node{
			{ID: "files", Type: models.NodeTypeSourceFile, Name: "Files", Config: map[string]interface{}{"path": filesCSV, "format": "csv"}},
			{
				ID: "parse", Type: models.NodeTypeCode, Name: "Parse",
				Config: map[string]interface{}{
					"script": `
output_data = {"columns": columns + ["seen"], "rows": [dict(r, seen=1) for r in rows]}
`,
					"expansion": map[string]interface{}{
						"over": map[string]interface{}{"file": "files"},
					},
				},
			},
			{ID: "sink", Type: models.NodeTypeSinkFile, Name: "Out", Config: map[string]interface{}{"path": outPath, "format": "csv"}},
		},
		Edges: []models.Edge{
			{From: "files", To: "parse"},
			{From: "parse", To: "sink"},
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

	out, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatalf("read sink output: %v", err)
	}
	outStr := string(out)
	for _, want := range []string{"a.csv", "b.csv", "c.csv"} {
		if !strings.Contains(outStr, want) {
			t.Errorf("expected combined output to contain %q, got:\n%s", want, outStr)
		}
	}

	instances, err := real.ListExpansionInstancesByRun(run.ID)
	if err != nil {
		t.Fatalf("ListExpansionInstancesByRun: %v", err)
	}
	if len(instances) != 3 {
		t.Fatalf("expected 3 distinct per-item expansion instances (one per file), got %d: %+v", len(instances), instances)
	}
	seenKeys := map[string]bool{}
	for _, inst := range instances {
		if inst.NodeID != "parse" {
			t.Errorf("expected instance node_id=parse, got %s", inst.NodeID)
		}
		if inst.Status != models.RunStatusSuccess {
			t.Errorf("expected instance %d status=success, got %s", inst.InstanceIndex, inst.Status)
		}
		if inst.InstanceKey == "" {
			t.Error("expected a non-empty instance key")
		}
		seenKeys[inst.InstanceKey] = true
	}
	if len(seenKeys) != 3 {
		t.Errorf("expected 3 distinct instance keys, got %d: %v", len(seenKeys), seenKeys)
	}
}

// TestPipeline_ExpandThenUnion_CollectShape simulates the exact compiled
// shape of `parsed = parse.expand(file=files); parsed.collect(mode="union")`
// per brokoli-sdk's CollectionRef.collect(): the expand node feeds exactly
// one edge into an explicit downstream `union` node. This proves the
// architectural resolution end-to-end: the expand node combines its own
// per-item results internally, and the downstream union node passes that
// single already-combined dataset through (the "identity union" exception)
// rather than erroring on <2 inputs.
func TestPipeline_ExpandThenUnion_CollectShape(t *testing.T) {
	realStore := newExpansionTestStore(t, "expand-union-e2e")
	real := realStore.(*store.SQLiteStore)

	dir := t.TempDir()
	filesCSV := writeCSV(t, dir, "files.csv", "path\na.csv\nb.csv\n")
	outPath := filepath.Join(dir, "out.csv")

	pipeline := &models.Pipeline{
		ID:   "expand-union-e2e-pipeline",
		Name: "Expand + collect(union) e2e",
		Nodes: []models.Node{
			{ID: "files", Type: models.NodeTypeSourceFile, Name: "Files", Config: map[string]interface{}{"path": filesCSV, "format": "csv"}},
			{
				ID: "parse", Type: models.NodeTypeCode, Name: "Parse",
				Config: map[string]interface{}{
					"script": `output_data = {"columns": columns, "rows": rows}`,
					"expansion": map[string]interface{}{
						"over": map[string]interface{}{"file": "files"},
					},
				},
			},
			{ID: "collected", Type: models.NodeTypeUnion, Name: "Collected", Config: map[string]interface{}{"mode": "union"}},
			{ID: "sink", Type: models.NodeTypeSinkFile, Name: "Out", Config: map[string]interface{}{"path": outPath, "format": "csv"}},
		},
		Edges: []models.Edge{
			{From: "files", To: "parse"},
			{From: "parse", To: "collected"},
			{From: "collected", To: "sink"},
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

	out, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatalf("read sink output: %v", err)
	}
	outStr := string(out)
	for _, want := range []string{"a.csv", "b.csv"} {
		if !strings.Contains(outStr, want) {
			t.Errorf("expected union output to contain %q, got:\n%s", want, outStr)
		}
	}
}

// TestPipeline_ExpandMaxInstances_Exceeded proves the bounded-expansion
// limit is enforced loudly (run fails with a clear error) rather than
// silently truncating the collection.
func TestPipeline_ExpandMaxInstances_Exceeded(t *testing.T) {
	realStore := newExpansionTestStore(t, "expand-maxinstances")
	real := realStore.(*store.SQLiteStore)

	dir := t.TempDir()
	filesCSV := writeCSV(t, dir, "files.csv", "path\na.csv\nb.csv\nc.csv\n")

	pipeline := &models.Pipeline{
		ID:   "expand-maxinstances-pipeline",
		Name: "Expand over limit",
		Nodes: []models.Node{
			{ID: "files", Type: models.NodeTypeSourceFile, Name: "Files", Config: map[string]interface{}{"path": filesCSV, "format": "csv"}},
			{
				ID: "parse", Type: models.NodeTypeCode, Name: "Parse",
				Config: map[string]interface{}{
					"script": `output_data = {"columns": columns, "rows": rows}`,
					"expansion": map[string]interface{}{
						"over":          map[string]interface{}{"file": "files"},
						"max_instances": float64(2), // 3 items > limit of 2
					},
				},
			},
		},
		Edges:     []models.Edge{{From: "files", To: "parse"}},
		CreatedAt: time.Now().UTC(),
		UpdatedAt: time.Now().UTC(),
	}
	if err := real.CreatePipeline(pipeline); err != nil {
		t.Fatal(err)
	}

	eng := drainEngineOnCleanup(t, NewEngine(real))
	run, err := eng.RunPipeline(pipeline.ID)
	if err == nil {
		t.Fatal("expected RunPipeline to fail when the collection exceeds expansion.max_instances")
	}
	if run == nil || run.Status != models.RunStatusFailed {
		t.Fatalf("expected a failed run, got %+v", run)
	}
	if !strings.Contains(err.Error(), "max_instances") && !strings.Contains(err.Error(), "exceeding the configured limit") {
		t.Errorf("expected a clear max-instances error, got: %v", err)
	}
}

// TestPipeline_ExpandInstanceFailure_IndependentlyTracked proves that when
// one item's instance fails, its models.ExpansionInstance row is recorded
// as failed independently of the instances that succeeded before it —
// each item's outcome is visible on its own row, not folded into one
// aggregate status.
func TestPipeline_ExpandInstanceFailure_IndependentlyTracked(t *testing.T) {
	realStore := newExpansionTestStore(t, "expand-failure")
	real := realStore.(*store.SQLiteStore)

	dir := t.TempDir()
	// "bad.csv" triggers the script's deliberate failure below; sorted
	// alphabetically it comes after a.csv, so at least one instance
	// succeeds durably before the failing one runs.
	filesCSV := writeCSV(t, dir, "files.csv", "path\na.csv\nbad.csv\n")

	pipeline := &models.Pipeline{
		ID:   "expand-failure-pipeline",
		Name: "Expand with a failing item",
		Nodes: []models.Node{
			{ID: "files", Type: models.NodeTypeSourceFile, Name: "Files", Config: map[string]interface{}{"path": filesCSV, "format": "csv"}},
			{
				ID: "parse", Type: models.NodeTypeCode, Name: "Parse",
				Config: map[string]interface{}{
					"script": `
import sys
if rows and rows[0].get("path") == "bad.csv":
    print("boom", file=sys.stderr)
    sys.exit(1)
output_data = {"columns": columns, "rows": rows}
`,
					"expansion": map[string]interface{}{
						"over": map[string]interface{}{"file": "files"},
					},
				},
			},
		},
		Edges:     []models.Edge{{From: "files", To: "parse"}},
		CreatedAt: time.Now().UTC(),
		UpdatedAt: time.Now().UTC(),
	}
	if err := real.CreatePipeline(pipeline); err != nil {
		t.Fatal(err)
	}

	eng := drainEngineOnCleanup(t, NewEngine(real))
	run, err := eng.RunPipeline(pipeline.ID)
	if err == nil {
		t.Fatal("expected RunPipeline to fail when an expansion instance's script fails")
	}
	if run == nil {
		t.Fatal("expected a run record even on failure")
	}

	instances, lerr := real.ListExpansionInstancesByRun(run.ID)
	if lerr != nil {
		t.Fatalf("ListExpansionInstancesByRun: %v", lerr)
	}
	if len(instances) != 2 {
		t.Fatalf("expected 2 tracked instances (one success, one failure), got %d: %+v", len(instances), instances)
	}
	var sawSuccess, sawFailure bool
	for _, inst := range instances {
		switch inst.Status {
		case models.RunStatusSuccess:
			sawSuccess = true
		case models.RunStatusFailed:
			sawFailure = true
			if inst.Error == "" {
				t.Error("expected a non-empty error on the failed instance")
			}
		}
	}
	if !sawSuccess || !sawFailure {
		t.Errorf("expected one instance to succeed and one to fail independently, got: %+v", instances)
	}
}

// TestPipeline_ExpandRetry_SkipsAlreadySucceededSiblings proves ADR-015
// §6's "retry without rerunning successful siblings" guarantee at the node
// level (Tnsor-Labs/brokoli#90 M3): "a.csv" succeeds on the node's first
// attempt, "bad.csv" fails, forcing the whole node to retry — but "a.csv"'s
// script must not run a second time on that retry. Verified two ways: (1) a
// call-count marker file the "a.csv" branch of the script appends to stays
// at exactly one byte, and (2) all four expected models.ExpansionInstance
// rows exist (two attempts x two items), with attempt 1's "a.csv" row
// showing success despite never re-invoking the subprocess.
func TestPipeline_ExpandRetry_SkipsAlreadySucceededSiblings(t *testing.T) {
	realStore := newExpansionTestStore(t, "expand-retry-skip")
	real := realStore.(*store.SQLiteStore)

	dir := t.TempDir()
	filesCSV := writeCSV(t, dir, "files.csv", "path\na.csv\nbad.csv\n")
	aCallsPath := filepath.Join(dir, "a_calls.txt")
	badMarkerPath := filepath.Join(dir, "bad_failed_once.marker")

	pipeline := &models.Pipeline{
		ID:   "expand-retry-skip-pipeline",
		Name: "Expand retry skips successful siblings",
		Nodes: []models.Node{
			{ID: "files", Type: models.NodeTypeSourceFile, Name: "Files", Config: map[string]interface{}{"path": filesCSV, "format": "csv"}},
			{
				ID: "parse", Type: models.NodeTypeCode, Name: "Parse",
				Config: map[string]interface{}{
					"max_retries": float64(1),
					"retry_delay": float64(1),
					"script": `
import os, sys
path = rows[0]["path"] if rows else None
if path == "a.csv":
    with open(` + goStringLiteral(aCallsPath) + `, "a") as f:
        f.write("x")
    output_data = {"columns": columns, "rows": rows}
elif path == "bad.csv":
    if not os.path.exists(` + goStringLiteral(badMarkerPath) + `):
        open(` + goStringLiteral(badMarkerPath) + `, "w").close()
        print("boom", file=sys.stderr)
        sys.exit(1)
    output_data = {"columns": columns, "rows": rows}
`,
					"expansion": map[string]interface{}{
						"over": map[string]interface{}{"file": "files"},
					},
				},
			},
		},
		Edges:     []models.Edge{{From: "files", To: "parse"}},
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
		t.Fatalf("run status = %s, want success (bad.csv succeeds on the node's retry)", run.Status)
	}

	calls, err := os.ReadFile(aCallsPath)
	if err != nil {
		t.Fatalf("read a.csv call marker: %v", err)
	}
	if len(calls) != 1 {
		t.Fatalf("a.csv's script ran %d time(s), want exactly 1 — it must not be re-executed on the node's retry", len(calls))
	}

	instances, err := real.ListExpansionInstancesByRun(run.ID)
	if err != nil {
		t.Fatalf("ListExpansionInstancesByRun: %v", err)
	}
	if len(instances) != 4 {
		t.Fatalf("expected 4 expansion instance rows (2 attempts x 2 items), got %d: %+v", len(instances), instances)
	}
	byAttemptAndKey := map[[2]interface{}]models.ExpansionInstance{}
	for _, inst := range instances {
		byAttemptAndKey[[2]interface{}{inst.NodeAttempt, inst.InstanceKey}] = inst
	}
	aAttempt0, ok := byAttemptAndKey[[2]interface{}{0, "idx:0"}]
	if !ok || aAttempt0.Status != models.RunStatusSuccess {
		t.Errorf("attempt 0 a.csv (idx:0) = %+v, want a successful row", aAttempt0)
	}
	badAttempt0, ok := byAttemptAndKey[[2]interface{}{0, "idx:1"}]
	if !ok || badAttempt0.Status != models.RunStatusFailed {
		t.Errorf("attempt 0 bad.csv (idx:1) = %+v, want a failed row", badAttempt0)
	}
	aAttempt1, ok := byAttemptAndKey[[2]interface{}{1, "idx:0"}]
	if !ok || aAttempt1.Status != models.RunStatusSuccess {
		t.Errorf("attempt 1 a.csv (idx:0) = %+v, want a successful (reused) row", aAttempt1)
	}
	badAttempt1, ok := byAttemptAndKey[[2]interface{}{1, "idx:1"}]
	if !ok || badAttempt1.Status != models.RunStatusSuccess {
		t.Errorf("attempt 1 bad.csv (idx:1) = %+v, want a successful row (second try)", badAttempt1)
	}

	// The same scenario, from the store.ExecutionAttemptStore side (ADR-017):
	// each (node, instance_key, attempt) combination gets its own durable
	// claim/lease/fencing row, extending #144's node-level wiring down to
	// instance granularity.
	aExecAttempt0, err := real.GetExecutionAttempt(run.ID, "parse", "idx:0", 0)
	if err != nil {
		t.Fatalf("GetExecutionAttempt(idx:0, attempt 0): %v", err)
	}
	if aExecAttempt0.Status != models.AttemptStatusCompleted {
		t.Errorf("execution attempt idx:0 attempt 0 status = %s, want completed", aExecAttempt0.Status)
	}
	if aExecAttempt0.ClaimedBy == "" {
		t.Error("execution attempt idx:0 attempt 0 claimed_by is empty, want the runner's instance ID (it was actually executed)")
	}

	badExecAttempt0, err := real.GetExecutionAttempt(run.ID, "parse", "idx:1", 0)
	if err != nil {
		t.Fatalf("GetExecutionAttempt(idx:1, attempt 0): %v", err)
	}
	if badExecAttempt0.Status != models.AttemptStatusFailed {
		t.Errorf("execution attempt idx:1 attempt 0 status = %s, want failed", badExecAttempt0.Status)
	}

	aExecAttempt1, err := real.GetExecutionAttempt(run.ID, "parse", "idx:0", 1)
	if err != nil {
		t.Fatalf("GetExecutionAttempt(idx:0, attempt 1): %v", err)
	}
	if aExecAttempt1.Status != models.AttemptStatusCompleted {
		t.Errorf("execution attempt idx:0 attempt 1 status = %s, want completed", aExecAttempt1.Status)
	}
	if aExecAttempt1.ClaimedBy != "" {
		t.Errorf("execution attempt idx:0 attempt 1 claimed_by = %q, want empty — it was reused, never actually claimed", aExecAttempt1.ClaimedBy)
	}

	badExecAttempt1, err := real.GetExecutionAttempt(run.ID, "parse", "idx:1", 1)
	if err != nil {
		t.Fatalf("GetExecutionAttempt(idx:1, attempt 1): %v", err)
	}
	if badExecAttempt1.Status != models.AttemptStatusCompleted {
		t.Errorf("execution attempt idx:1 attempt 1 status = %s, want completed", badExecAttempt1.Status)
	}
}

// goStringLiteral renders s as a double-quoted Python string literal safe
// to splice directly into a generated script — reused across this file's
// tests that need to embed a filesystem path (e.g. t.TempDir()'s output,
// which is an absolute path Python must see exactly) inside Python source.
func goStringLiteral(s string) string {
	return fmt.Sprintf("%q", s)
}

// TestPipeline_OrdinaryCodeNode_RegressionNoExpansion is the explicit
// regression guard: an ordinary `code` node with no `expansion` config must
// behave exactly as before — running once against its whole input, with no
// expansion_instances rows created at all.
func TestPipeline_OrdinaryCodeNode_RegressionNoExpansion(t *testing.T) {
	realStore := newExpansionTestStore(t, "expand-regression")
	real := realStore.(*store.SQLiteStore)

	dir := t.TempDir()
	inCSV := writeCSV(t, dir, "in.csv", "id,name\n1,Alice\n2,Bob\n")
	outPath := filepath.Join(dir, "out.csv")

	pipeline := &models.Pipeline{
		ID:   "expand-regression-pipeline",
		Name: "Ordinary code node (no expansion)",
		Nodes: []models.Node{
			{ID: "src", Type: models.NodeTypeSourceFile, Name: "Src", Config: map[string]interface{}{"path": inCSV, "format": "csv"}},
			{
				ID: "code1", Type: models.NodeTypeCode, Name: "Upper",
				Config: map[string]interface{}{
					"script": `
for r in rows:
    r["name"] = r["name"].upper()
output_data = {"columns": columns, "rows": rows}
`,
				},
			},
			{ID: "sink", Type: models.NodeTypeSinkFile, Name: "Out", Config: map[string]interface{}{"path": outPath, "format": "csv"}},
		},
		Edges: []models.Edge{
			{From: "src", To: "code1"},
			{From: "code1", To: "sink"},
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

	out, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatalf("read sink output: %v", err)
	}
	outStr := string(out)
	if !strings.Contains(outStr, "ALICE") || !strings.Contains(outStr, "BOB") {
		t.Errorf("expected the whole-dataset script to run once over both rows, got:\n%s", outStr)
	}

	instances, err := real.ListExpansionInstancesByRun(run.ID)
	if err != nil {
		t.Fatalf("ListExpansionInstancesByRun: %v", err)
	}
	if len(instances) != 0 {
		t.Errorf("expected no expansion_instances rows for a non-expansion code node, got %d: %+v", len(instances), instances)
	}
}

// newUnitTestRunner builds a minimal but safe Runner for directly exercising
// node-handler methods (like runUnion) outside a full pipeline Execute —
// r.log's AppendLog/emit calls need a real store and an existing run row.
func newUnitTestRunner(t *testing.T) *Runner {
	t.Helper()
	s := newExpansionTestStore(t, "runner-unit")
	pipe := &models.Pipeline{ID: "unit-test-pipeline", Name: "unit-test-pipeline", CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC()}
	if err := s.CreatePipeline(pipe); err != nil {
		t.Fatal(err)
	}
	run := &models.Run{ID: common.NewID(), PipelineID: pipe.ID, Status: models.RunStatusRunning}
	if err := s.CreateRun(run); err != nil {
		t.Fatal(err)
	}
	return &Runner{store: s, run: run}
}

// TestRunUnion_SingleInput_IdentityPassThrough is a unit-level check of the
// runUnion pass-through decision itself, isolated from the full pipeline
// wiring above.
func TestRunUnion_SingleInput_IdentityPassThrough(t *testing.T) {
	r := newUnitTestRunner(t)
	ds := &common.DataSet{Columns: []string{"id"}, Rows: []common.DataRow{{"id": "1"}, {"id": "2"}}}
	node := models.Node{ID: "u1", Name: "Collected", Type: models.NodeTypeUnion}

	result, err := r.runUnion(node, []*common.DataSet{ds})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result.Rows) != 2 {
		t.Errorf("expected identity pass-through of 2 rows, got %d", len(result.Rows))
	}
}

func TestRunUnion_StillRequiresTwoForOrdinaryMultiInput(t *testing.T) {
	r := newUnitTestRunner(t)
	node := models.Node{ID: "u1", Name: "U", Type: models.NodeTypeUnion}
	if _, err := r.runUnion(node, nil); err == nil {
		t.Fatal("expected an error for zero inputs")
	}
}
