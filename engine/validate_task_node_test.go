package engine

// ADR-033 rollout: "task" is a recognized models.NodeType that
// round-trips through JSON/model parsing. Phase 0 hard-refused every
// task node outright (no execution path existed); phase 2b (#439 step
// 5) replaced that blanket refusal with the same "task_bundle must be
// well-formed" check the code-node task_bundle config already gets --
// a task node with a valid reference now validates cleanly, one without
// (or with a malformed one) is still refused, but with a message naming
// the actual problem instead of "not yet executable".

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/Tnsor-Labs/brokoli/models"
	"github.com/Tnsor-Labs/brokoli/pkg/taskbundlev2"
)

func TestValidate_TaskNodeRoundTripsThroughJSON(t *testing.T) {
	p := validPipeline()
	p.Nodes = append(p.Nodes, models.Node{
		ID: "n3", Type: models.NodeTypeTask, Name: "Score", Config: map[string]interface{}{},
	})

	data, err := json.Marshal(p)
	if err != nil {
		t.Fatalf("marshal pipeline with a task node: %v", err)
	}
	var roundTripped models.Pipeline
	if err := json.Unmarshal(data, &roundTripped); err != nil {
		t.Fatalf("unmarshal pipeline with a task node: %v", err)
	}
	if roundTripped.Nodes[2].Type != models.NodeTypeTask {
		t.Fatalf("task node type did not round-trip: got %q, want %q", roundTripped.Nodes[2].Type, models.NodeTypeTask)
	}
}

// taskNodePipeline builds a two-node pipeline with the task node as its
// own root feeding the sink -- not fed BY the existing source, since a
// task node is source-capable (ADR-033: its inputs are run-parameter
// kwargs, not edges) and, like dbt/migrate, cannot receive incoming
// edges either.
func taskNodePipeline(config map[string]interface{}) *models.Pipeline {
	return &models.Pipeline{
		Name: "test",
		Nodes: []models.Node{
			{ID: "n3", Type: models.NodeTypeTask, Name: "Score", Config: config},
			{ID: "n2", Type: models.NodeTypeSinkFile, Name: "Output", Config: map[string]interface{}{"path": "/out.sql"}},
		},
		Edges: []models.Edge{{From: "n3", To: "n2"}},
	}
}

func TestValidate_TaskNodeWithoutTaskBundleIsRefused(t *testing.T) {
	ve := ValidatePipeline(taskNodePipeline(map[string]interface{}{}))
	if !ve.HasErrors() {
		t.Fatal("expected a task node with no task_bundle to be refused")
	}
	found := false
	for _, e := range ve.Errors {
		if strings.Contains(e, "requires 'task_bundle'") {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected an error naming the missing task_bundle, got: %v", ve.Errors)
	}
}

func TestValidate_TaskNodeWithMalformedDigestIsRefused(t *testing.T) {
	ve := ValidatePipeline(taskNodePipeline(map[string]interface{}{
		"task_bundle": map[string]interface{}{"digest": "not-a-digest", "format": taskbundlev2.Format},
	}))
	if !ve.HasErrors() {
		t.Fatal("expected a malformed digest to be refused")
	}
	found := false
	for _, e := range ve.Errors {
		if strings.Contains(e, "content address") {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected an error naming the malformed digest, got: %v", ve.Errors)
	}
}

func TestValidate_TaskNodeWithWrongFormatIsRefused(t *testing.T) {
	digest := "sha256:" + strings.Repeat("0", 64)
	ve := ValidatePipeline(taskNodePipeline(map[string]interface{}{
		"task_bundle": map[string]interface{}{"digest": digest, "format": "brokoli.task-bundle/v99"},
	}))
	if !ve.HasErrors() {
		t.Fatal("expected an unsupported format to be refused")
	}
	found := false
	for _, e := range ve.Errors {
		if strings.Contains(e, "format") {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected an error naming the unsupported format, got: %v", ve.Errors)
	}
}

func TestValidate_TaskNodeWithWellFormedTaskBundleValidatesCleanly(t *testing.T) {
	digest := "sha256:" + strings.Repeat("0", 64)
	ve := ValidatePipeline(taskNodePipeline(map[string]interface{}{
		"task_bundle": map[string]interface{}{"digest": digest, "format": taskbundlev2.Format},
	}))
	if ve.HasErrors() {
		t.Fatalf("expected a well-formed task_bundle reference to validate cleanly, got: %v", ve.Errors)
	}
}
