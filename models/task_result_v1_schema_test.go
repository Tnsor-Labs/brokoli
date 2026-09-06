package models_test

// Contract tests for the ADR-033 task result manifest schema at
// docs/schema/task-result-v1.json (ADR-032/033 rollout phase 0, issue
// #439 step 5 -- schema and fixtures only, zero execution: no Go/Python/
// Node code reads or writes this manifest yet).
//
// Fixtures live in docs/schema/fixtures/task-result-v1/{positive,negative}.
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

const taskResultV1SchemaPath = "../docs/schema/task-result-v1.json"

func compileTaskResultV1Schema(t *testing.T) *jsonschema.Schema {
	t.Helper()
	c := jsonschema.NewCompiler()
	sch, err := c.Compile(taskResultV1SchemaPath)
	if err != nil {
		t.Fatalf("compile task-result-v1.json: %v", err)
	}
	return sch
}

func validateTaskResultV1Fixture(t *testing.T, sch *jsonschema.Schema, path string, stripViolationField bool) error {
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

func TestTaskResultV1PositiveFixturesValidate(t *testing.T) {
	sch := compileTaskResultV1Schema(t)
	dir := "../docs/schema/fixtures/task-result-v1/positive"
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
		if err := validateTaskResultV1Fixture(t, sch, path, false); err != nil {
			t.Errorf("positive fixture %s rejected by task-result-v1.json:\n%v", e.Name(), err)
		}
		validated++
	}
	if validated == 0 {
		t.Fatal("no positive fixtures found -- this contract check is running against nothing")
	}
}

func TestTaskResultV1NegativeFixturesAreRejected(t *testing.T) {
	sch := compileTaskResultV1Schema(t)
	dir := "../docs/schema/fixtures/task-result-v1/negative"
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
		if err := validateTaskResultV1Fixture(t, sch, path, true); err == nil {
			t.Errorf("negative fixture %s validated successfully -- it should have been rejected", e.Name())
		}
		validated++
	}
	if validated == 0 {
		t.Fatal("no negative fixtures found -- this contract check is running against nothing")
	}
}
