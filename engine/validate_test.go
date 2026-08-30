package engine

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Tnsor-Labs/brokoli/models"
	"github.com/Tnsor-Labs/brokoli/pkg/plugins"
)

func validPipeline() *models.Pipeline {
	return &models.Pipeline{
		Name: "test",
		Nodes: []models.Node{
			{ID: "n1", Type: models.NodeTypeSourceFile, Name: "Load", Config: map[string]interface{}{"path": "/data/test.csv"}},
			{ID: "n2", Type: models.NodeTypeSinkFile, Name: "Output", Config: map[string]interface{}{"path": "/out.sql"}},
		},
		Edges: []models.Edge{{From: "n1", To: "n2"}},
	}
}

func TestValidate_ValidPipeline(t *testing.T) {
	ve := ValidatePipeline(validPipeline())
	if ve.HasErrors() {
		t.Errorf("expected no errors, got: %v", ve.Errors)
	}
}

func TestValidate_EmptyName(t *testing.T) {
	p := validPipeline()
	p.Name = ""
	ve := ValidatePipeline(p)
	if !ve.HasErrors() {
		t.Error("expected error for empty name")
	}
}

func TestValidate_NoNodes(t *testing.T) {
	p := &models.Pipeline{Name: "test", Nodes: []models.Node{}}
	ve := ValidatePipeline(p)
	if !ve.HasErrors() {
		t.Error("expected error for no nodes")
	}
}

func TestValidate_DuplicateNodeID(t *testing.T) {
	p := validPipeline()
	p.Nodes[1].ID = "n1" // duplicate
	ve := ValidatePipeline(p)
	if !ve.HasErrors() {
		t.Error("expected error for duplicate ID")
	}
}

func TestValidate_InvalidEdgeRef(t *testing.T) {
	p := validPipeline()
	p.Edges = append(p.Edges, models.Edge{From: "n1", To: "nonexistent"})
	ve := ValidatePipeline(p)
	if !ve.HasErrors() {
		t.Error("expected error for invalid edge ref")
	}
}

func TestValidate_Cycle(t *testing.T) {
	p := &models.Pipeline{
		Name: "test",
		Nodes: []models.Node{
			{ID: "a", Type: models.NodeTypeSourceFile, Name: "A", Config: map[string]interface{}{"path": "/a"}},
			{ID: "b", Type: models.NodeTypeTransform, Name: "B", Config: map[string]interface{}{}},
		},
		Edges: []models.Edge{{From: "a", To: "b"}, {From: "b", To: "a"}},
	}
	ve := ValidatePipeline(p)
	if !ve.HasErrors() {
		t.Error("expected error for cycle")
	}
}

func TestValidate_NoSource(t *testing.T) {
	p := &models.Pipeline{
		Name: "test",
		Nodes: []models.Node{
			{ID: "n1", Type: models.NodeTypeSinkFile, Name: "Out", Config: map[string]interface{}{"path": "/out"}},
		},
	}
	ve := ValidatePipeline(p)
	if !ve.HasErrors() {
		t.Error("expected error for no source node")
	}
}

func TestValidate_DisconnectedNode(t *testing.T) {
	p := validPipeline()
	p.Nodes = append(p.Nodes, models.Node{
		ID: "n3", Type: models.NodeTypeTransform, Name: "Orphan", Config: map[string]interface{}{},
	})
	ve := ValidatePipeline(p)
	if !ve.HasErrors() {
		t.Error("expected error for disconnected node")
	}
}

func TestValidate_MissingRequiredConfig(t *testing.T) {
	p := &models.Pipeline{
		Name: "test",
		Nodes: []models.Node{
			{ID: "n1", Type: models.NodeTypeSourceFile, Name: "Load", Config: map[string]interface{}{}}, // missing path
		},
	}
	ve := ValidatePipeline(p)
	if !ve.HasErrors() {
		t.Error("expected error for missing path config")
	}
}

func TestValidate_SelfLoop(t *testing.T) {
	p := validPipeline()
	p.Edges = append(p.Edges, models.Edge{From: "n1", To: "n1"})
	ve := ValidatePipeline(p)
	if !ve.HasErrors() {
		t.Error("expected error for self-loop")
	}
}

func TestValidate_CodeNodeExecutionKeys(t *testing.T) {
	t.Setenv("BROKOLI_CODE_POOL", "1")
	nodePath, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is not installed")
	}
	makeCode := func(config map[string]interface{}) *models.Pipeline {
		config["script"] = "output_data = {}"
		return &models.Pipeline{
			Name: "test",
			Nodes: []models.Node{
				{ID: "src", Type: models.NodeTypeSourceFile, Name: "Load", Config: map[string]interface{}{"path": "/tmp/in.csv"}},
				{ID: "code", Type: models.NodeTypeCode, Name: "Code", Config: config},
			},
			Edges: []models.Edge{{From: "src", To: "code"}},
		}
	}

	// Valid shapes: JSON floats, YAML ints, runtime paths, and both languages.
	ve := ValidatePipeline(makeCode(map[string]interface{}{
		"timeout": float64(60), "max_memory_mb": 512, "max_cpu_seconds": float64(30), "language": "typescript", "node_path": nodePath,
	}))
	if ve.HasErrors() {
		t.Fatalf("valid execution keys rejected: %v", ve.Errors)
	}

	for name, config := range map[string]map[string]interface{}{
		"zero timeout":        {"timeout": float64(0)},
		"negative memory":     {"max_memory_mb": float64(-1)},
		"string cpu":          {"max_cpu_seconds": "fast"},
		"empty python_path":   {"python_path": "  "},
		"numeric python_path": {"python_path": 3},
		"empty node_path":     {"node_path": "  "},
		"numeric node_path":   {"node_path": 3},
		"unknown language":    {"language": "ruby"},
		"numeric language":    {"language": 3},
		"null language":       {"language": nil},
	} {
		if ve := ValidatePipeline(makeCode(config)); !ve.HasErrors() {
			t.Errorf("%s: expected a validation error", name)
		}
	}

	results := ValidateNodes(makeCode(map[string]interface{}{"language": "ruby"}).Nodes)
	if len(results) == 0 || !strings.Contains(strings.Join(results[0].Errors, " "), "language") {
		t.Fatalf("per-node validation did not reject language: %+v", results)
	}
}

func TestValidate_TypeScriptRefusesLegacyCodePath(t *testing.T) {
	t.Setenv("BROKOLI_CODE_POOL", "0")
	errs := codeExecutionKeyErrors(map[string]interface{}{"language": "typescript"})
	if !strings.Contains(strings.Join(errs, " "), "BROKOLI_CODE_POOL=0") {
		t.Fatalf("legacy-path refusal does not name the constraint: %v", errs)
	}
}

// ── ValidateNodes (per-node) tests ──

func TestValidateNodes_AllValid(t *testing.T) {
	nodes := []models.Node{
		{ID: "n1", Type: models.NodeTypeSourceFile, Name: "In", Config: map[string]interface{}{"path": "/data.csv"}},
		{ID: "n2", Type: models.NodeTypeSinkFile, Name: "Out", Config: map[string]interface{}{"path": "/out.csv"}},
	}
	results := ValidateNodes(nodes)
	if len(results) != 0 {
		t.Errorf("expected no issues, got %d", len(results))
	}
}

func TestValidateNodes_MissingSourcePath(t *testing.T) {
	nodes := []models.Node{
		{ID: "n1", Type: models.NodeTypeSourceFile, Name: "In", Config: map[string]interface{}{}},
	}
	results := ValidateNodes(nodes)
	if len(results) != 1 {
		t.Fatalf("expected 1 issue, got %d", len(results))
	}
	if results[0].NodeID != "n1" {
		t.Errorf("expected issue on n1, got %s", results[0].NodeID)
	}
	if len(results[0].Errors) != 1 {
		t.Errorf("expected 1 error, got %d", len(results[0].Errors))
	}
}

func TestValidateNodes_MissingCodeScript(t *testing.T) {
	nodes := []models.Node{
		{ID: "c1", Type: models.NodeTypeCode, Name: "Code", Config: map[string]interface{}{}},
	}
	results := ValidateNodes(nodes)
	if len(results) != 1 {
		t.Fatalf("expected 1 issue, got %d", len(results))
	}
	if len(results[0].Errors) == 0 {
		t.Error("expected error for missing script")
	}
}

func TestValidateNodes_SourceDBMissingBoth(t *testing.T) {
	nodes := []models.Node{
		{ID: "db1", Type: models.NodeTypeSourceDB, Name: "DB", Config: map[string]interface{}{}},
	}
	results := ValidateNodes(nodes)
	if len(results) != 1 {
		t.Fatalf("expected 1 issue, got %d", len(results))
	}
	if len(results[0].Errors) != 2 {
		t.Errorf("expected 2 errors (uri + query), got %d", len(results[0].Errors))
	}
}

func TestValidateNodes_TransformWarning(t *testing.T) {
	nodes := []models.Node{
		{ID: "t1", Type: models.NodeTypeTransform, Name: "Transform", Config: map[string]interface{}{}},
	}
	results := ValidateNodes(nodes)
	if len(results) != 1 {
		t.Fatalf("expected 1 issue, got %d", len(results))
	}
	if len(results[0].Warnings) == 0 {
		t.Error("expected warning for empty transform rules")
	}
	if len(results[0].Errors) != 0 {
		t.Error("transform with no rules should be warning, not error")
	}
}

func TestValidateNodes_SQLGenerateMissingTable(t *testing.T) {
	nodes := []models.Node{
		{ID: "g1", Type: models.NodeTypeSQLGenerate, Name: "SQL", Config: map[string]interface{}{}},
	}
	results := ValidateNodes(nodes)
	if len(results) != 1 {
		t.Fatalf("expected 1 issue, got %d", len(results))
	}
	if len(results[0].Errors) == 0 {
		t.Error("expected error for missing table in sql_generate")
	}
}

func TestValidateNodes_MultipleIssues(t *testing.T) {
	nodes := []models.Node{
		{ID: "s1", Type: models.NodeTypeSourceFile, Name: "Good", Config: map[string]interface{}{"path": "/ok.csv"}},
		{ID: "s2", Type: models.NodeTypeSourceAPI, Name: "Bad API", Config: map[string]interface{}{}},
		{ID: "c1", Type: models.NodeTypeCode, Name: "Bad Code", Config: map[string]interface{}{}},
	}
	results := ValidateNodes(nodes)
	if len(results) != 2 {
		t.Errorf("expected 2 nodes with issues, got %d", len(results))
	}
}

// ── Phase 0: capability-based source detection (IR v2) ──

// TestValidate_CapabilitySourceNode_CodeType is the core regression test
// for the Phase 0 SDK protocol change: a decorator-based node whose Type
// is "code" but which declares capabilities ["source", "dataset-output"]
// must satisfy the "pipeline must have a source" rule, even though
// "code" was never in the hardcoded source-type switch.
func TestValidate_CapabilitySourceNode_CodeType(t *testing.T) {
	p := &models.Pipeline{
		Name: "test",
		Nodes: []models.Node{
			{
				ID:           "c1",
				Type:         models.NodeTypeCode,
				Name:         "My Source",
				Config:       map[string]interface{}{"script": "return [{'a': 1}]"},
				Capabilities: []string{models.CapabilitySource, models.CapabilityDatasetOutput},
			},
			{ID: "s1", Type: models.NodeTypeSinkFile, Name: "Out", Config: map[string]interface{}{"path": "/out.csv"}},
		},
		Edges: []models.Edge{{From: "c1", To: "s1"}},
	}
	ve := ValidatePipeline(p)
	if ve.HasErrors() {
		t.Errorf("expected no errors for capability-declared source node, got: %v", ve.Errors)
	}
}

// TestValidate_NoCapabilitySource_Rejected covers both old- and new-style
// pipelines that have no source-capable node at all: a plain compute-only
// "code" node explicitly tagged only "compute" (new style) must still be
// rejected for lacking a source.
func TestValidate_NoCapabilitySource_Rejected(t *testing.T) {
	p := &models.Pipeline{
		Name: "test",
		Nodes: []models.Node{
			{
				ID:           "c1",
				Type:         models.NodeTypeCode,
				Name:         "Compute",
				Config:       map[string]interface{}{"script": "x"},
				Capabilities: []string{models.CapabilityCompute},
			},
			{ID: "s1", Type: models.NodeTypeSinkFile, Name: "Out", Config: map[string]interface{}{"path": "/out.csv"}},
		},
		Edges: []models.Edge{{From: "c1", To: "s1"}},
	}
	ve := ValidatePipeline(p)
	if !ve.HasErrors() {
		t.Error("expected error: no source-capable node (new-style, capabilities set but no 'source')")
	}
}

// TestValidate_OldStyleSourceTypes_NoCapabilities_Regression pins that
// pipelines built entirely from the legacy source_api/source_db/etc.
// types with no "capabilities" field at all (pre-Phase-0 SDK clients,
// hand-written JSON) validate exactly as before — the capability check
// must fall back to type-based inference when Capabilities is empty.
func TestValidate_OldStyleSourceTypes_NoCapabilities_Regression(t *testing.T) {
	legacySourceTypes := []models.NodeType{
		models.NodeTypeSourceFile, models.NodeTypeSourceAPI, models.NodeTypeSourceDB,
		models.NodeTypeDBT, models.NodeTypeMigrate,
	}
	for _, nt := range legacySourceTypes {
		t.Run(string(nt), func(t *testing.T) {
			cfg := map[string]interface{}{"path": "/x", "url": "http://x", "uri": "postgres://x", "query": "select 1"}
			p := &models.Pipeline{
				Name: "test",
				Nodes: []models.Node{
					{ID: "src", Type: nt, Name: "Source", Config: cfg},
					{ID: "s1", Type: models.NodeTypeSinkFile, Name: "Out", Config: map[string]interface{}{"path": "/out.csv"}},
				},
				Edges: []models.Edge{{From: "src", To: "s1"}},
			}
			if nt == models.NodeTypeMigrate {
				// migrate is intentionally standalone with no edges.
				p.Nodes = []models.Node{{ID: "src", Type: nt, Name: "Source", Config: cfg}}
				p.Edges = nil
			}
			ve := ValidatePipeline(p)
			if ve.HasErrors() {
				t.Errorf("expected no errors for legacy source type %s (no capabilities), got: %v", nt, ve.Errors)
			}
		})
	}

	// And the negative case: no legacy source type present, no
	// capabilities anywhere -> still rejected, same as pre-Phase-0.
	p := &models.Pipeline{
		Name: "test",
		Nodes: []models.Node{
			{ID: "t1", Type: models.NodeTypeTransform, Name: "T", Config: map[string]interface{}{}},
			{ID: "s1", Type: models.NodeTypeSinkFile, Name: "Out", Config: map[string]interface{}{"path": "/out.csv"}},
		},
		Edges: []models.Edge{{From: "t1", To: "s1"}},
	}
	ve := ValidatePipeline(p)
	if !ve.HasErrors() {
		t.Error("expected error: no source-capable node (old-style, no capabilities at all)")
	}
}

// TestValidate_EdgeIntoCapabilitySourceNode_Rejected mirrors the
// type-based "source nodes can't receive incoming edges" rule for
// capability-declared source nodes.
func TestValidate_EdgeIntoCapabilitySourceNode_Rejected(t *testing.T) {
	p := &models.Pipeline{
		Name: "test",
		Nodes: []models.Node{
			{ID: "c1", Type: models.NodeTypeCode, Name: "Src", Config: map[string]interface{}{"script": "x"}, Capabilities: []string{models.CapabilitySource}},
			{ID: "c2", Type: models.NodeTypeCode, Name: "Other", Config: map[string]interface{}{"script": "y"}, Capabilities: []string{models.CapabilityCompute}},
			{ID: "s1", Type: models.NodeTypeSinkFile, Name: "Out", Config: map[string]interface{}{"path": "/out.csv"}},
		},
		Edges: []models.Edge{{From: "c2", To: "c1"}, {From: "c1", To: "s1"}},
	}
	ve := ValidatePipeline(p)
	if !ve.HasErrors() {
		t.Error("expected error: capability-source node cannot receive incoming edges")
	}
}

// ── Phase 0: ir_version validation ──

func TestValidate_UnsupportedIRVersion(t *testing.T) {
	p := validPipeline()
	p.IRVersion = "99.0"
	ve := ValidatePipeline(p)
	if !ve.HasErrors() {
		t.Error("expected error for unsupported ir_version")
	}
}

func TestValidate_SupportedIRVersion(t *testing.T) {
	p := validPipeline()
	p.IRVersion = "2.0"
	ve := ValidatePipeline(p)
	if ve.HasErrors() {
		t.Errorf("expected no errors for supported ir_version, got: %v", ve.Errors)
	}
}

func TestValidate_SupportedConditionalIRVersion(t *testing.T) {
	selected := true
	p := &models.Pipeline{
		Name:      "conditional",
		IRVersion: models.ConditionalEdgesIRVersion,
		Nodes: []models.Node{
			{ID: "src", Type: models.NodeTypeSourceFile, Name: "Source", Config: map[string]interface{}{"path": "/input.csv"}},
			{ID: "check", Type: models.NodeTypeCondition, Name: "Check", Config: map[string]interface{}{"expression": "always_true"}},
			{ID: "sink", Type: models.NodeTypeSinkFile, Name: "Sink", Config: map[string]interface{}{"path": "/output.csv"}},
		},
		Edges: []models.Edge{
			{From: "src", To: "check"},
			{From: "check", To: "sink", Condition: &selected},
		},
	}

	if ve := ValidatePipeline(p); ve.HasErrors() {
		t.Errorf("expected no errors for supported conditional IR, got: %v", ve.Errors)
	}
}

func TestValidate_EmptyIRVersion_BackwardCompatible(t *testing.T) {
	p := validPipeline()
	p.IRVersion = ""
	ve := ValidatePipeline(p)
	if ve.HasErrors() {
		t.Errorf("expected no errors for empty ir_version (pre-versioned pipeline), got: %v", ve.Errors)
	}
}

func TestValidate_ConditionalEdgeRequiresIR21AndConditionSource(t *testing.T) {
	p := validPipeline()
	p.IRVersion = "2.0"
	selected := true
	p.Edges[0].Condition = &selected

	ve := ValidatePipeline(p)
	joined := strings.Join(ve.Errors, "; ")
	if !strings.Contains(joined, "requires pipeline IR 2.1") {
		t.Errorf("expected IR requirement error, got: %v", ve.Errors)
	}
	if !strings.Contains(joined, "must originate from a condition node") {
		t.Errorf("expected condition-source error, got: %v", ve.Errors)
	}
}

// ── union / dataset_map / dataset_filter validation (Tnsor-Labs/brokoli#32) ──

func unionPipeline(edgesToUnion int) *models.Pipeline {
	p := &models.Pipeline{
		Name: "union test",
		Nodes: []models.Node{
			{ID: "u1", Type: models.NodeTypeUnion, Name: "Combine", Config: map[string]interface{}{"mode": "union"}},
			{ID: "sink", Type: models.NodeTypeSinkFile, Name: "Out", Config: map[string]interface{}{"path": "/out.csv"}},
		},
		Edges: []models.Edge{{From: "u1", To: "sink"}},
	}
	for i := 0; i < edgesToUnion; i++ {
		id := fmt.Sprintf("src%d", i)
		p.Nodes = append(p.Nodes, models.Node{
			ID: id, Type: models.NodeTypeSourceFile, Name: id, Config: map[string]interface{}{"path": "/" + id + ".csv"},
		})
		p.Edges = append(p.Edges, models.Edge{From: id, To: "u1"})
	}
	return p
}

func TestValidate_Union_TwoOrMoreInputsOK(t *testing.T) {
	ve := ValidatePipeline(unionPipeline(2))
	if ve.HasErrors() {
		t.Errorf("expected no errors for union with 2 inputs, got: %v", ve.Errors)
	}

	ve = ValidatePipeline(unionPipeline(3))
	if ve.HasErrors() {
		t.Errorf("expected no errors for union with 3 inputs, got: %v", ve.Errors)
	}
}

// TestValidate_Union_FewerThanTwoInputsFails is the direct acceptance-
// criteria test: "union with fewer than 2 incoming edges fails validation
// with a clear error, not silent pass-through."
func TestValidate_Union_FewerThanTwoInputsFails(t *testing.T) {
	ve := ValidatePipeline(unionPipeline(1))
	if !ve.HasErrors() {
		t.Fatal("expected error for union with only 1 incoming edge")
	}
	found := false
	for _, e := range ve.Errors {
		if strings.Contains(e, "union") && strings.Contains(e, "2") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected a clear union-input-count error, got: %v", ve.Errors)
	}
}

func TestValidate_Union_ZeroInputsFails(t *testing.T) {
	ve := ValidatePipeline(unionPipeline(0))
	if !ve.HasErrors() {
		t.Fatal("expected error for union with 0 incoming edges")
	}
}

func TestValidate_Union_UnsupportedModeRejected(t *testing.T) {
	p := unionPipeline(2)
	p.Nodes[0].Config["mode"] = "intersect"
	ve := ValidatePipeline(p)
	if !ve.HasErrors() {
		t.Fatal("expected error for unsupported union mode")
	}
}

func TestValidate_DatasetMap_MissingFunctionFails(t *testing.T) {
	nodes := []models.Node{
		{ID: "m1", Type: models.NodeTypeDatasetMap, Name: "Map", Config: map[string]interface{}{}},
	}
	results := ValidateNodes(nodes)
	if len(results) != 1 {
		t.Fatalf("expected 1 issue, got %d", len(results))
	}
	if len(results[0].Errors) == 0 {
		t.Error("expected error for missing function reference on dataset_map")
	}
}

func TestValidate_DatasetFilter_MissingFunctionNameFails(t *testing.T) {
	nodes := []models.Node{
		{
			ID: "f1", Type: models.NodeTypeDatasetFilter, Name: "Filter",
			Config: map[string]interface{}{"function": map[string]interface{}{"doc": "no name here"}},
		},
	}
	results := ValidateNodes(nodes)
	if len(results) != 1 {
		t.Fatalf("expected 1 issue, got %d", len(results))
	}
	if len(results[0].Errors) == 0 {
		t.Error("expected error for function reference missing 'name'")
	}
}

func TestValidate_DatasetMap_ValidFunctionReferencePasses(t *testing.T) {
	// Matches exactly what brokoli-sdk's DatasetRef.map()/.filter() emit
	// today (name + doc, no script) — this must pass deploy-time
	// validation even though it will fail loudly at *execution* time
	// (see TestExecutePartitionTransform_NameOnlyReference) until the SDK
	// serializes runnable source. See functionRefConfigError's doc
	// comment for why that split is intentional.
	nodes := []models.Node{
		{
			ID: "m1", Type: models.NodeTypeDatasetMap, Name: "Map",
			Config: map[string]interface{}{"function": map[string]interface{}{"name": "normalize", "doc": "Normalizes a row."}},
		},
	}
	results := ValidateNodes(nodes)
	if len(results) != 0 {
		t.Errorf("expected no validation issues for a well-formed (SDK-shaped) function reference, got: %+v", results)
	}
}

func TestValidatePipeline_MalformedDatasetMapFailsAtDeployTime(t *testing.T) {
	p := &models.Pipeline{
		Name: "test",
		Nodes: []models.Node{
			{ID: "n1", Type: models.NodeTypeSourceFile, Name: "Load", Config: map[string]interface{}{"path": "/data/test.csv"}},
			{ID: "n2", Type: models.NodeTypeDatasetMap, Name: "Map", Config: map[string]interface{}{}},
		},
		Edges: []models.Edge{{From: "n1", To: "n2"}},
	}
	ve := ValidatePipeline(p)
	if !ve.HasErrors() {
		t.Fatal("expected pipeline-level validation error for dataset_map with no function reference")
	}
}

// TestValidate_PluginSourceType_NoManualCapabilities pins the fix for
// Tnsor-Labs/brokoli#62: a pipeline whose only source is a plugin-
// registered node type must validate cleanly when the plugin manager is
// passed as an executor, without the pipeline author hand-setting
// Node.Capabilities — mirroring how a legacy built-in source type or a
// capability-tagged "code" node already work. Omitting the executor
// (the zero-value/no-plugins case) must keep rejecting it, same as any
// other unrecognized type with no capabilities.
func TestValidate_PluginSourceType_NoManualCapabilities(t *testing.T) {
	dir := t.TempDir()
	pluginDir := filepath.Join(dir, "hello")
	if err := os.MkdirAll(pluginDir, 0o755); err != nil {
		t.Fatalf("mkdir plugin dir: %v", err)
	}
	manifest := `{
		"protocol_version": 1,
		"name": "hello",
		"version": "0.1.0",
		"description": "test fixture",
		"binary": "./bin",
		"node_types": [
			{"type": "source_hello", "kind": "source", "display_name": "Hello Source"},
			{"type": "transform_hello", "kind": "transform", "display_name": "Hello Transform"}
		]
	}`
	if err := os.WriteFile(filepath.Join(pluginDir, "manifest.json"), []byte(manifest), 0o644); err != nil {
		t.Fatalf("write manifest: %v", err)
	}

	mgr, err := plugins.NewManager(dir)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}

	p := &models.Pipeline{
		Name: "test",
		Nodes: []models.Node{
			{ID: "src", Type: "source_hello", Name: "Plugin Source", Config: map[string]interface{}{"stream": "greetings"}},
			{ID: "s1", Type: models.NodeTypeSinkFile, Name: "Out", Config: map[string]interface{}{"path": "/out.csv"}},
		},
		Edges: []models.Edge{{From: "src", To: "s1"}},
	}

	// With the plugin manager as an executor: recognized, validates clean.
	ve := ValidatePipeline(p, mgr)
	if ve.HasErrors() {
		t.Errorf("expected no errors for plugin-declared source (executor passed), got: %v", ve.Errors)
	}

	// Without any executors: unrecognized type, no manual capabilities ->
	// same "must have a source" rejection as before this fix, not a panic
	// or a false positive.
	ve = ValidatePipeline(p)
	if !ve.HasErrors() {
		t.Error("expected 'must have a source' error when no executors are passed for a plugin-only-source pipeline")
	}

	transformPipeline := validPipeline()
	transformPipeline.Nodes = append(transformPipeline.Nodes, models.Node{
		ID: "plugin-transform", Type: "transform_hello", Name: "Plugin Transform", Config: map[string]interface{}{},
	})
	transformPipeline.Edges = []models.Edge{
		{From: "n1", To: "plugin-transform"},
		{From: "plugin-transform", To: "n2"},
	}
	ve = ValidatePipeline(transformPipeline, mgr)
	if !strings.Contains(ve.Error(), "unsupported type") {
		t.Fatalf("plugin transform errors = %v, want unsupported type", ve.Errors)
	}
}
