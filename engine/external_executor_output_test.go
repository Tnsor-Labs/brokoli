package engine

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Tnsor-Labs/brokoli/extensions"
	"github.com/Tnsor-Labs/brokoli/models"
	"github.com/Tnsor-Labs/brokoli/pkg/common"
	"github.com/Tnsor-Labs/brokoli/store"
)

/*
 * An external executor that accepts a node and returns nothing used to be
 * indistinguishable from one whose node legitimately produced no rows:
 * both became `nodeExecutionResult{}, nil`, a success carrying an empty
 * dataset, which the next node then read as real data.
 *
 * That is how an executor which could not execute anything at all went
 * unnoticed. It claimed code, migrate, source_db and sink_db; it had no
 * way to return data at all; and nothing in the engine objected.
 */

// stubExecutor answers for one node type with whatever it is told to.
type stubExecutor struct {
	handles string
	output  interface{}
	logs    []string
}

func (e *stubExecutor) Name() string { return "stub" }

func (e *stubExecutor) CanHandle(nodeType string) bool { return nodeType == e.handles }

func (e *stubExecutor) Execute(extensions.ExecutionContext) (*extensions.ExecutionResult, error) {
	return &extensions.ExecutionResult{OutputData: e.output, Logs: e.logs}, nil
}

func runWithExecutor(t *testing.T, pipe *models.Pipeline, exec extensions.NodeExecutor) (*models.Run, error) {
	t.Helper()
	s, err := store.NewSQLiteStore(filepath.Join(t.TempDir(), "exec.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	now := time.Now().UTC()
	pipe.CreatedAt, pipe.UpdatedAt = now, now
	if err := s.CreatePipeline(pipe); err != nil {
		t.Fatalf("create pipeline: %v", err)
	}
	runner := NewRunner(s, nil, pipe, nil, nil, []extensions.NodeExecutor{exec}, nil, "", nil)
	run, err := runner.Execute()
	return run, err
}

func onePipeline(nodeID string, nodeType models.NodeType, caps ...string) *models.Pipeline {
	return &models.Pipeline{
		ID:         "external-executor",
		PipelineID: "external-executor",
		Name:       "External Executor",
		Nodes: []models.Node{{
			ID:           nodeID,
			Type:         nodeType,
			Name:         nodeID,
			Config:       map[string]interface{}{},
			Capabilities: caps,
		}},
	}
}

// A compute node handed to an executor that returns nothing must fail,
// naming the executor. Silence here becomes an empty dataset downstream.
func TestExternalExecutorReturningNoDataFailsTheNode(t *testing.T) {
	run, err := runWithExecutor(t,
		onePipeline("normalize", models.NodeTypeCode),
		&stubExecutor{handles: string(models.NodeTypeCode), output: nil})

	if err == nil {
		t.Fatal("run succeeded; an executor that produced no data for a code node must fail it")
	}
	for _, want := range []string{"stub", "normalize", "produced nothing"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err.Error(), want)
		}
	}
	if run != nil && run.Status != models.RunStatusFailed {
		t.Errorf("run status = %q, want failed", run.Status)
	}
}

// Wrong type was silently dropped, which is the same wrong answer by a
// different route.
func TestExternalExecutorReturningWrongTypeFailsTheNode(t *testing.T) {
	_, err := runWithExecutor(t,
		onePipeline("normalize", models.NodeTypeCode),
		&stubExecutor{handles: string(models.NodeTypeCode), output: map[string]any{"rows": 1}})

	if err == nil {
		t.Fatal("run succeeded; an executor returning a non-DataSet must fail the node")
	}
	if !strings.Contains(err.Error(), "expected *common.DataSet") {
		t.Errorf("error %q does not name the expected type", err.Error())
	}
}

// The allow direction: a sink writes outward and returns nothing, exactly
// as the built-in handlers do (runSinkFile returns nil, nil). Refusing
// that would break every dispatched sink.
func TestExternalExecutorSinkMayReturnNoData(t *testing.T) {
	for _, nodeType := range []models.NodeType{
		models.NodeTypeSinkFile,
		models.NodeTypeSinkDB,
		models.NodeTypeSinkAPI,
		models.NodeTypeNotify,
	} {
		t.Run(string(nodeType), func(t *testing.T) {
			run, err := runWithExecutor(t,
				onePipeline("writer", nodeType),
				&stubExecutor{handles: string(nodeType), output: nil})
			if err != nil {
				t.Fatalf("sink %s returning no data should succeed, got %v", nodeType, err)
			}
			if run.Status != models.RunStatusSuccess {
				t.Errorf("run status = %q, want success", run.Status)
			}
		})
	}
}

// A declared sink capability outranks the type name, matching
// nodeCannotWriteExternally: the SDK can tag a code node as a sink.
func TestDeclaredSinkCapabilityExemptsANodeFromRequiringData(t *testing.T) {
	run, err := runWithExecutor(t,
		onePipeline("publish", models.NodeTypeCode, models.CapabilitySink),
		&stubExecutor{handles: string(models.NodeTypeCode), output: nil})
	if err != nil {
		t.Fatalf("a code node declared as a sink should be allowed to return nothing, got %v", err)
	}
	if run.Status != models.RunStatusSuccess {
		t.Errorf("run status = %q, want success", run.Status)
	}
}

// A dataset still passes through untouched.
func TestExternalExecutorDataSetPassesThrough(t *testing.T) {
	ds := &common.DataSet{
		Columns: []string{"id"},
		Rows:    []common.DataRow{{"id": 1}, {"id": 2}},
	}
	run, err := runWithExecutor(t,
		onePipeline("normalize", models.NodeTypeCode),
		&stubExecutor{handles: string(models.NodeTypeCode), output: ds})
	if err != nil {
		t.Fatalf("run failed: %v", err)
	}
	if run.Status != models.RunStatusSuccess {
		t.Errorf("run status = %q, want success", run.Status)
	}
}

// The table and the capability override, checked directly so the rule is
// readable without running a pipeline.
func TestExternalExecutorMustReturnData(t *testing.T) {
	cases := []struct {
		name string
		node models.Node
		want bool
	}{
		{"code must", models.Node{Type: models.NodeTypeCode}, true},
		{"source_db must", models.Node{Type: models.NodeTypeSourceDB}, true},
		{"migrate must, it returns a summary", models.Node{Type: models.NodeTypeMigrate}, true},
		{"sink_file need not", models.Node{Type: models.NodeTypeSinkFile}, false},
		{"sink_db need not", models.Node{Type: models.NodeTypeSinkDB}, false},
		{"declared sink need not", models.Node{Type: models.NodeTypeCode, Capabilities: []string{models.CapabilitySink}}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := externalExecutorMustReturnData(tc.node); got != tc.want {
				t.Errorf("externalExecutorMustReturnData = %v, want %v", got, tc.want)
			}
		})
	}
}
