package engine

// ADR-033 rollout phase 0 (issue #439 step 5): "task" is a recognized
// models.NodeType that round-trips through JSON/model parsing, but
// engine.ValidatePipeline must hard-refuse any pipeline containing one --
// there is no brokoli.task-runtime/v1 adapter anywhere yet. This pins
// both halves of that deliberate policy so a future change can't
// silently reopen or accidentally tighten it.

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/Tnsor-Labs/brokoli/models"
)

const taskNodeRefusalSubstring = `"task", which this server recognizes but cannot yet execute`

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

func TestValidate_TaskNodeIsRefusedWithASpecificMessage(t *testing.T) {
	p := validPipeline()
	// Otherwise well-formed (connected, has a source) so the task-node
	// refusal is the only validation error -- a clean assertion, not one
	// mixed in with unrelated disconnected-node noise.
	p.Nodes = append(p.Nodes, models.Node{
		ID: "n3", Type: models.NodeTypeTask, Name: "Score", Config: map[string]interface{}{},
	})
	p.Edges = []models.Edge{{From: "n1", To: "n3"}, {From: "n3", To: "n2"}}

	ve := ValidatePipeline(p)
	if !ve.HasErrors() {
		t.Fatal("expected a task-node pipeline to be refused")
	}
	found := false
	for _, e := range ve.Errors {
		if strings.Contains(e, taskNodeRefusalSubstring) {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected an error containing %q, got: %v", taskNodeRefusalSubstring, ve.Errors)
	}
}
