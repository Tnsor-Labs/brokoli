package models_test

// Contract tests for the ADR-032 portable task interface schema at
// docs/schema/task-interface-v1.json (rollout step 1: schema and
// fixtures only, per ADR-032 section 12 -- this schema is not yet wired
// into pipeline-ir-2.1.json or any Go model, so there is no two-way
// field sweep here the way ir_schema_contract_test.go has for
// models.Pipeline. That binding lands with IR 2.2 in a later PR.
//
// Fixtures live in docs/schema/fixtures/task-interface/{positive,negative}.
// Every positive fixture must validate; every negative fixture must be
// rejected, and names/documents (via its own `_violation` field, stripped
// before validation) exactly which rule it breaks.

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

const taskInterfaceSchemaPath = "../docs/schema/task-interface-v1.json"

func compileTaskInterfaceSchema(t *testing.T) *jsonschema.Schema {
	t.Helper()
	c := jsonschema.NewCompiler()
	sch, err := c.Compile(taskInterfaceSchemaPath)
	if err != nil {
		t.Fatalf("compile task-interface-v1.json: %v", err)
	}
	return sch
}

func validateTaskInterfaceFixture(t *testing.T, sch *jsonschema.Schema, path string, stripViolationField bool) error {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	if stripViolationField {
		var m map[string]interface{}
		if err := json.Unmarshal(data, &m); err != nil {
			t.Fatalf("%s: unmarshal for _violation strip: %v", path, err)
		}
		if _, ok := m["_violation"]; !ok {
			t.Fatalf("%s: negative fixture is missing its _violation field -- every negative fixture must document which rule it breaks", path)
		}
		delete(m, "_violation")
		data, err = json.Marshal(m)
		if err != nil {
			t.Fatalf("%s: remarshal after stripping _violation: %v", path, err)
		}
	}
	inst, err := jsonschema.UnmarshalJSON(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("%s: reparse: %v", path, err)
	}
	return sch.Validate(inst)
}

func TestTaskInterfacePositiveFixturesValidate(t *testing.T) {
	sch := compileTaskInterfaceSchema(t)
	dir := "../docs/schema/fixtures/task-interface/positive"
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read %s: %v", dir, err)
	}
	validated := 0
	for _, e := range entries {
		if filepath.Ext(e.Name()) != ".json" {
			continue
		}
		path := filepath.Join(dir, e.Name())
		if err := validateTaskInterfaceFixture(t, sch, path, false); err != nil {
			t.Errorf("positive fixture %s rejected by task-interface-v1.json:\n%v", e.Name(), err)
		}
		validated++
	}
	if validated == 0 {
		t.Fatal("no positive fixtures found -- this contract check is running against nothing")
	}
}

func TestTaskInterfaceNegativeFixturesAreRejected(t *testing.T) {
	sch := compileTaskInterfaceSchema(t)
	dir := "../docs/schema/fixtures/task-interface/negative"
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read %s: %v", dir, err)
	}
	validated := 0
	for _, e := range entries {
		if filepath.Ext(e.Name()) != ".json" {
			continue
		}
		path := filepath.Join(dir, e.Name())
		if err := validateTaskInterfaceFixture(t, sch, path, true); err == nil {
			t.Errorf("negative fixture %s validated successfully -- it should have been rejected", e.Name())
		}
		validated++
	}
	if validated == 0 {
		t.Fatal("no negative fixtures found -- this contract check is running against nothing")
	}
}

// TestTaskInterfaceSchemaStructuralCases pins specific behaviors the
// fixture files above don't each get their own file for -- narrow,
// inline cases exercising one JSON Schema mechanism at a time.
func TestTaskInterfaceSchemaStructuralCases(t *testing.T) {
	sch := compileTaskInterfaceSchema(t)
	base := func() map[string]interface{} {
		return map[string]interface{}{
			"contract": "brokoli.task-interface/v1",
			"inputs":   map[string]interface{}{},
			"outputs":  map[string]interface{}{},
		}
	}
	validate := func(t *testing.T, m map[string]interface{}) error {
		t.Helper()
		data, err := json.Marshal(m)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		inst, err := jsonschema.UnmarshalJSON(bytes.NewReader(data))
		if err != nil {
			t.Fatalf("reparse: %v", err)
		}
		return sch.Validate(inst)
	}

	t.Run("required:false with default is fine", func(t *testing.T) {
		m := base()
		m["parameters"] = map[string]interface{}{
			"x": map[string]interface{}{"type": map[string]interface{}{"kind": "int64"}, "required": false, "default": 1},
		}
		if err := validate(t, m); err != nil {
			t.Fatalf("required:false with default should be accepted: %v", err)
		}
	})

	t.Run("required:true without default is fine", func(t *testing.T) {
		m := base()
		m["parameters"] = map[string]interface{}{
			"x": map[string]interface{}{"type": map[string]interface{}{"kind": "int64"}, "required": true},
		}
		if err := validate(t, m); err != nil {
			t.Fatalf("required:true without default should be accepted: %v", err)
		}
	})

	t.Run("unknown top-level field on task_interface is rejected", func(t *testing.T) {
		m := base()
		m["surprise"] = true
		if err := validate(t, m); err == nil {
			t.Fatal("expected rejection of an unknown top-level field")
		}
	})

	t.Run("dataset row may be entirely absent (unknown row shape)", func(t *testing.T) {
		m := base()
		m["outputs"] = map[string]interface{}{
			"result": map[string]interface{}{"value": map[string]interface{}{"kind": "dataset"}},
		}
		if err := validate(t, m); err != nil {
			t.Fatalf("a dataset port with no 'row' should be accepted (unknown, not an error): %v", err)
		}
	})

	t.Run("cardinality outside the closed enum is rejected", func(t *testing.T) {
		m := base()
		m["outputs"] = map[string]interface{}{
			"result": map[string]interface{}{
				"value":       map[string]interface{}{"kind": "scalar", "type": map[string]interface{}{"kind": "int64"}},
				"cardinality": "all",
			},
		}
		if err := validate(t, m); err == nil {
			t.Fatal("expected rejection of a cardinality value outside one/optional/many")
		}
	})
}
