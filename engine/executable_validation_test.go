package engine

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Tnsor-Labs/brokoli/extensions"
	"github.com/Tnsor-Labs/brokoli/models"
	"github.com/Tnsor-Labs/brokoli/store"
)

type validationExecutor struct {
	nodeType string
}

func (e *validationExecutor) Name() string { return "validation" }
func (e *validationExecutor) CanHandle(nodeType string) bool {
	return nodeType == e.nodeType
}
func (e *validationExecutor) Execute(extensions.ExecutionContext) (*extensions.ExecutionResult, error) {
	return &extensions.ExecutionResult{}, nil
}

type declaringOnlyExecutor struct{}

func (*declaringOnlyExecutor) Name() string          { return "declarer" }
func (*declaringOnlyExecutor) CanHandle(string) bool { return false }
func (*declaringOnlyExecutor) Execute(extensions.ExecutionContext) (*extensions.ExecutionResult, error) {
	return &extensions.ExecutionResult{}, nil
}
func (*declaringOnlyExecutor) DeclaredCapabilities(string) ([]string, bool) {
	return []string{models.CapabilitySource}, true
}

func executableValidationPipeline(node models.Node) *models.Pipeline {
	return &models.Pipeline{
		Name: "executable validation",
		Nodes: []models.Node{
			{ID: "source", Type: models.NodeTypeSourceFile, Name: "Source", Config: map[string]interface{}{"path": "/tmp/input.csv"}},
			node,
			{ID: "sink", Type: models.NodeTypeSinkFile, Name: "Sink", Config: map[string]interface{}{"path": "/tmp/output.csv"}},
		},
		Edges: []models.Edge{{From: "source", To: node.ID}, {From: node.ID, To: "sink"}},
	}
}

func TestValidatePipelineRejectsUnexecutableNodeTypes(t *testing.T) {
	tests := []struct {
		name string
		node models.Node
		exec extensions.NodeExecutor
	}{
		{name: "unknown intermediate", node: models.Node{ID: "work", Type: "unknown", Name: "Unknown", Config: map[string]interface{}{}}},
		{name: "manual source capability", node: models.Node{ID: "work", Type: "unknown", Name: "Unknown", Config: map[string]interface{}{}, Capabilities: []string{models.CapabilitySource}}},
		{name: "manual compute capability", node: models.Node{ID: "work", Type: "unknown", Name: "Unknown", Config: map[string]interface{}{}, Capabilities: []string{models.CapabilityCompute}}},
		{name: "empty type", node: models.Node{ID: "work", Name: "Empty", Config: map[string]interface{}{}}},
		{name: "nil executor", node: models.Node{ID: "work", Type: "unknown", Name: "Unknown", Config: map[string]interface{}{}}, exec: nil},
		{name: "declarer alone", node: models.Node{ID: "work", Type: "unknown", Name: "Unknown", Config: map[string]interface{}{}}, exec: &declaringOnlyExecutor{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ve := ValidatePipeline(executableValidationPipeline(tt.node), tt.exec)
			if !strings.Contains(ve.Error(), "unsupported type") {
				t.Fatalf("errors = %v, want unsupported type", ve.Errors)
			}
		})
	}
}

func TestValidatePipelineAcceptsCanHandleExecutorWithoutDeclarer(t *testing.T) {
	node := models.Node{ID: "work", Type: "custom", Name: "Custom", Config: map[string]interface{}{}}
	if ve := ValidatePipeline(executableValidationPipeline(node), &validationExecutor{nodeType: "custom"}); ve.HasErrors() {
		t.Fatalf("custom executor-owned type rejected: %v", ve.Errors)
	}
}

func TestIsBuiltInNodeTypeRecognizesEveryImplementedBuiltIn(t *testing.T) {
	builtIns := []models.NodeType{
		models.NodeTypeSourceFile, models.NodeTypeSourceAPI, models.NodeTypeSourceDB,
		models.NodeTypeTransform, models.NodeTypeQualityCheck, models.NodeTypeSQLGenerate,
		models.NodeTypeCode, models.NodeTypeJoin, models.NodeTypeSinkFile,
		models.NodeTypeSinkDB, models.NodeTypeSinkAPI, models.NodeTypeMigrate,
		models.NodeTypeCondition, models.NodeTypeDBT, models.NodeTypeNotify,
		models.NodeTypeUnion, models.NodeTypeDatasetMap, models.NodeTypeDatasetFilter,
	}
	for _, nodeType := range builtIns {
		if !IsBuiltInNodeType(nodeType) {
			t.Errorf("built-in %q is not recognized", nodeType)
		}
	}
	if IsBuiltInNodeType("") || IsBuiltInNodeType("unknown") {
		t.Fatal("empty or unknown node type recognized as built-in")
	}
}

func TestValidatePipelineDatabaseConnectorsAcceptConnID(t *testing.T) {
	p := &models.Pipeline{
		Name: "connector IDs",
		Nodes: []models.Node{
			{ID: "source", Type: models.NodeTypeSourceDB, Name: "Source", Config: map[string]interface{}{"conn_id": "source-connection", "query": "select 1"}},
			{ID: "sql", Type: models.NodeTypeSQLGenerate, Name: "SQL", Config: map[string]interface{}{"table": "output"}},
			{ID: "sink", Type: models.NodeTypeSinkDB, Name: "Sink", Config: map[string]interface{}{"conn_id": "sink-connection"}},
		},
		Edges: []models.Edge{{From: "source", To: "sql"}, {From: "sql", To: "sink"}},
	}
	if ve := ValidatePipeline(p); ve.HasErrors() {
		t.Fatalf("conn_id connector config rejected: %v", ve.Errors)
	}
}

func TestRunnerRunNodeLogicRejectsUnsupportedType(t *testing.T) {
	r := &Runner{pipe: &models.Pipeline{}, executors: []extensions.NodeExecutor{nil}}
	_, err := r.runNodeLogic(models.Node{Type: "unknown"}, nil, nil, nil, 0, "", context.Background())
	if err == nil || !strings.Contains(err.Error(), `unsupported node type "unknown"`) {
		t.Fatalf("error = %v, want unsupported node type", err)
	}
}

func TestRunnerRecognizedBuiltInReachesRuntimeHandler(t *testing.T) {
	r := &Runner{pipe: &models.Pipeline{}}
	_, err := r.runNodeLogic(models.Node{Type: models.NodeTypeSourceFile, Config: map[string]interface{}{}}, nil, nil, nil, 0, "", context.Background())
	if err == nil {
		t.Fatal("source_file without a path unexpectedly succeeded")
	}
	if strings.Contains(err.Error(), "unsupported node type") || strings.Contains(err.Error(), "has no runtime handler") {
		t.Fatalf("recognized built-in reached fallback instead of source_file handler: %v", err)
	}
}

func TestDryRunReturnsRunnerError(t *testing.T) {
	s, err := store.NewSQLiteStore(filepath.Join(t.TempDir(), "dry-run.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	e := drainEngineOnCleanup(t, NewEngine(s))
	p := &models.Pipeline{
		ID:   "dry-run-error",
		Name: "dry run error",
		Nodes: []models.Node{{
			ID: "source", Type: models.NodeTypeSourceFile, Name: "Source",
			Config: map[string]interface{}{"path": filepath.Join(t.TempDir(), "missing.csv")},
		}},
	}
	_, err = e.DryRun(p, 10)
	if err == nil {
		t.Fatal("DryRun discarded runner error")
	}
}
