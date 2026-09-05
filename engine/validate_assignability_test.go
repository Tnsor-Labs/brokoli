package engine

// Tests for validateEdgeAssignability (ADR-032 rollout step 4, #439):
// pkg/taskinterface's assignability engine wired into edge validation.
//
// No SDK populates models.Node.Interface yet, so these tests set it
// explicitly to prove the mechanism is live -- not exercised through any
// real client path today, but genuinely wired and load-bearing the
// moment one exists.

import (
	"strings"
	"testing"

	"github.com/Tnsor-Labs/brokoli/models"
)

func interfaceWithRecordOutput(fields string) map[string]interface{} {
	return map[string]interface{}{
		"contract": "brokoli.task-interface/v1",
		"inputs":   map[string]interface{}{},
		"outputs": map[string]interface{}{
			"result": map[string]interface{}{
				"value": map[string]interface{}{
					"kind": "dataset",
					"row": map[string]interface{}{
						"kind":              "record",
						"additional_fields": false,
						"fields":            mustFieldList(fields),
					},
				},
			},
		},
	}
}

// mustFieldList builds the record 'fields' array for one of two fixed
// shapes this test file uses, keyed by a short label -- avoids a JSON
// literal + unmarshal round trip for two tiny, fixed cases.
func mustFieldList(label string) []interface{} {
	switch label {
	case "score:string":
		return []interface{}{
			map[string]interface{}{"name": "score", "type": map[string]interface{}{"kind": "string"}, "required": true},
		}
	case "score:float64":
		return []interface{}{
			map[string]interface{}{"name": "score", "type": map[string]interface{}{"kind": "float64"}, "required": true},
		}
	default:
		panic("mustFieldList: unknown label " + label)
	}
}

func interfaceWithRecordInput(fields string) map[string]interface{} {
	return map[string]interface{}{
		"contract": "brokoli.task-interface/v1",
		"inputs": map[string]interface{}{
			"input": map[string]interface{}{
				"value": map[string]interface{}{
					"kind": "dataset",
					"row": map[string]interface{}{
						"kind":              "record",
						"additional_fields": false,
						"fields":            mustFieldList(fields),
					},
				},
			},
		},
		"outputs": map[string]interface{}{},
	}
}

func TestValidateEdgeAssignability_IncompatibleRecordFieldType_Rejected(t *testing.T) {
	p := &models.Pipeline{
		Name: "test",
		Nodes: []models.Node{
			{ID: "src", Type: models.NodeTypeSourceFile, Name: "Src", Config: map[string]interface{}{"path": "/x.csv"},
				Interface: interfaceWithRecordOutput("score:string")},
			{ID: "sink", Type: models.NodeTypeSinkFile, Name: "Sink", Config: map[string]interface{}{"path": "/y.csv"},
				Interface: interfaceWithRecordInput("score:float64")},
		},
		Edges: []models.Edge{{From: "src", To: "sink"}},
	}
	ve := ValidatePipeline(p)
	if !ve.HasErrors() {
		t.Fatal("expected an assignability error (producer declares score:string, consumer expects score:float64)")
	}
	found := false
	for _, e := range ve.Errors {
		if strings.Contains(e, "src -> sink") && strings.Contains(e, "score") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected an error naming the src -> sink edge and the score field, got: %v", ve.Errors)
	}
}

func TestValidateEdgeAssignability_CompatibleExplicitInterfaces_NoError(t *testing.T) {
	p := &models.Pipeline{
		Name: "test",
		Nodes: []models.Node{
			{ID: "src", Type: models.NodeTypeSourceFile, Name: "Src", Config: map[string]interface{}{"path": "/x.csv"},
				Interface: interfaceWithRecordOutput("score:float64")},
			{ID: "sink", Type: models.NodeTypeSinkFile, Name: "Sink", Config: map[string]interface{}{"path": "/y.csv"},
				Interface: interfaceWithRecordInput("score:float64")},
		},
		Edges: []models.Edge{{From: "src", To: "sink"}},
	}
	ve := ValidatePipeline(p)
	if ve.HasErrors() {
		t.Errorf("expected no errors for compatible interfaces, got: %v", ve.Errors)
	}
}

// TestValidateEdgeAssignability_KnownReferenceTypes_NoFalsePositive proves
// today's reference-table interfaces (all row: unknown except migrate,
// which cannot have outgoing edges) never produce an assignability error
// between any two known types -- the exact safety property that let this
// wiring land without touching any currently-deployable pipeline. This
// duplicates validPipeline()'s own source_file->sink_file shape but
// widens it to every pairing that can legally connect.
func TestValidateEdgeAssignability_KnownReferenceTypes_NoFalsePositive(t *testing.T) {
	p := &models.Pipeline{
		Name: "test",
		Nodes: []models.Node{
			{ID: "src", Type: models.NodeTypeSourceFile, Name: "Src", Config: map[string]interface{}{"path": "/x.csv"}},
			{ID: "t1", Type: models.NodeTypeTransform, Name: "T1"},
			{ID: "t2", Type: models.NodeTypeQualityCheck, Name: "T2"},
			{ID: "sink", Type: models.NodeTypeSinkFile, Name: "Sink", Config: map[string]interface{}{"path": "/y.csv"}},
		},
		Edges: []models.Edge{
			{From: "src", To: "t1"},
			{From: "t1", To: "t2"},
			{From: "t2", To: "sink"},
		},
	}
	ve := ValidatePipeline(p)
	if ve.HasErrors() {
		t.Errorf("expected no errors chaining known-interface node types with today's reference table, got: %v", ve.Errors)
	}
}

// TestValidateEdgeAssignability_UnknownInterfaceSkipsCheck proves an edge
// touching a node with no known interface (here, 'code' -- deliberately
// excluded from the reference table) is not checked at all, rather than
// treated as a violation.
func TestValidateEdgeAssignability_UnknownInterfaceSkipsCheck(t *testing.T) {
	p := &models.Pipeline{
		Name: "test",
		Nodes: []models.Node{
			{ID: "src", Type: models.NodeTypeCode, Name: "Src", Config: map[string]interface{}{"language": "python", "script": "output_data = {'columns': [], 'rows': []}"}, Capabilities: []string{models.CapabilitySource, models.CapabilityDatasetOutput}},
			{ID: "sink", Type: models.NodeTypeSinkFile, Name: "Sink", Config: map[string]interface{}{"path": "/y.csv"},
				Interface: interfaceWithRecordInput("score:float64")},
		},
		Edges: []models.Edge{{From: "src", To: "sink"}},
	}
	ve := ValidatePipeline(p)
	for _, e := range ve.Errors {
		if strings.Contains(e, "src -> sink") {
			t.Errorf("expected no assignability check for a node with no known interface, got: %s", e)
		}
	}
}
