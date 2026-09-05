package models_test

// Contract tests for docs/schema/pipeline-ir-2.2.json (ADR-032 rollout
// step 2, issue #439): the additive, optional 'interface' (per node) and
// 'parameters' (pipeline-level) fields, $ref-ing task-interface-v1.json.
//
// Deliberately NOT here: models.Pipeline/models.Node gain no new fields
// in this change, so there is no TestSchemaAndModelDeclareTheSameFields
// equivalent for 2.2 yet -- switching the "current" schema pointer in
// ir_schema_contract_test.go to 2.2 before those Go fields exist would
// make that sweep fail on 'parameters' with no models.Pipeline field to
// match, which is exactly the drift-detector doing its job on a change
// that hasn't happened yet. pipeline-ir-2.1.json (and its contract test)
// stays the "what does models.Pipeline actually bind to" source of truth
// until a later step in #439 adds those fields for real.
//
// Cross-file $ref resolution needs the referenced schema registered under
// its own $id via Compiler.AddResource before compiling 2.2 -- a bare
// Compile() of task-interface-v1.json first does not share that
// registration; both files declare an absolute https:// $id, so a
// relative "task-interface-v1.json#/..." $ref resolves into that
// URL-space, not the filesystem, once compilation of the referencing
// document begins.

import (
	"bytes"
	"encoding/json"
	"os"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

const (
	pipelineIR22SchemaPath = "../docs/schema/pipeline-ir-2.2.json"
	taskInterfaceSchemaURL = "https://github.com/Tnsor-Labs/brokoli/docs/schema/task-interface-v1.json"
)

// compilePipelineIR22Schema reuses taskInterfaceSchemaPath from
// task_interface_schema_test.go (same package).
func compilePipelineIR22Schema(t *testing.T) *jsonschema.Schema {
	t.Helper()
	data, err := os.ReadFile(taskInterfaceSchemaPath)
	if err != nil {
		t.Fatalf("read %s: %v", taskInterfaceSchemaPath, err)
	}
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("unmarshal %s: %v", taskInterfaceSchemaPath, err)
	}
	c := jsonschema.NewCompiler()
	if err := c.AddResource(taskInterfaceSchemaURL, doc); err != nil {
		t.Fatalf("register %s: %v", taskInterfaceSchemaURL, err)
	}
	sch, err := c.Compile(pipelineIR22SchemaPath)
	if err != nil {
		t.Fatalf("compile %s: %v", pipelineIR22SchemaPath, err)
	}
	return sch
}

func validateIR22(t *testing.T, sch *jsonschema.Schema, v interface{}) error {
	t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	inst, err := jsonschema.UnmarshalJSON(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("reparse: %v", err)
	}
	return sch.Validate(inst)
}

// TestPipelineIR22AcceptsExistingFullyPopulatedPipeline reuses
// fullyPopulatedPipeline() from ir_schema_contract_test.go (same package)
// to prove 2.2 is a true superset: every 2.0/2.1 pipeline this server
// already accepts remains valid, unchanged, under 2.2.
func TestPipelineIR22AcceptsExistingFullyPopulatedPipeline(t *testing.T) {
	sch := compilePipelineIR22Schema(t)
	if err := validateIR22(t, sch, fullyPopulatedPipeline()); err != nil {
		t.Fatalf("a pipeline valid under 2.1 was rejected by 2.2:\n%v", err)
	}
}

func TestPipelineIR22AcceptsNodeWithKnownInterface(t *testing.T) {
	sch := compilePipelineIR22Schema(t)
	doc := map[string]interface{}{
		"name": "p", "ir_version": "2.2",
		"nodes": []interface{}{
			map[string]interface{}{
				"id": "a", "type": "source_file", "name": "A",
				"interface": map[string]interface{}{
					"contract": "brokoli.task-interface/v1",
					"inputs":   map[string]interface{}{},
					"outputs": map[string]interface{}{
						"result": map[string]interface{}{"value": map[string]interface{}{"kind": "dataset"}},
					},
				},
			},
		},
		"edges": []interface{}{},
	}
	if err := validateIR22(t, sch, doc); err != nil {
		t.Fatalf("a node with a well-formed interface was rejected:\n%v", err)
	}
}

func TestPipelineIR22AcceptsTypedParameters(t *testing.T) {
	sch := compilePipelineIR22Schema(t)
	doc := map[string]interface{}{
		"name": "p", "ir_version": "2.2",
		"nodes": []interface{}{map[string]interface{}{"id": "a", "type": "source_file", "name": "A"}},
		"edges": []interface{}{},
		"parameters": map[string]interface{}{
			"threshold": map[string]interface{}{
				"type": map[string]interface{}{"kind": "float64"}, "required": false, "default": 0.5,
			},
		},
	}
	if err := validateIR22(t, sch, doc); err != nil {
		t.Fatalf("typed pipeline parameters were rejected:\n%v", err)
	}
}

func TestPipelineIR22RejectsMalformedInterface(t *testing.T) {
	sch := compilePipelineIR22Schema(t)
	doc := map[string]interface{}{
		"name": "p",
		"nodes": []interface{}{
			map[string]interface{}{
				"id": "a", "type": "source_file", "name": "A",
				"interface": map[string]interface{}{
					"contract": "not-the-right-contract-string",
					"inputs":   map[string]interface{}{},
					"outputs":  map[string]interface{}{},
				},
			},
		},
		"edges": []interface{}{},
	}
	if err := validateIR22(t, sch, doc); err == nil {
		t.Fatal("a node interface with the wrong contract string should have been rejected")
	}
}

func TestPipelineIR22AcceptsIRVersion22(t *testing.T) {
	sch := compilePipelineIR22Schema(t)
	doc := map[string]interface{}{"name": "p", "ir_version": "2.2", "nodes": []interface{}{}, "edges": []interface{}{}}
	if err := validateIR22(t, sch, doc); err != nil {
		t.Fatalf("ir_version 2.2 should be accepted:\n%v", err)
	}
}
