package models_test

// Contract tests for the ADR-033 task runtime protocol v1 frame schema
// at docs/schema/task-runtime-v1.json (ADR-032/033 rollout phase 0, issue
// #439 step 5 -- schema and fixtures only, zero execution: no Go/Python/
// Node code speaks this protocol yet). Does not replace ADR-029's framed
// binary pool protocol (pkg/codeexec), which continues to govern 'code'
// nodes exclusively per ADR-035 Decision 3.
//
// Fixtures live in docs/schema/fixtures/task-runtime-v1/{positive,negative}.
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

const taskRuntimeV1SchemaPath = "../docs/schema/task-runtime-v1.json"

func compileTaskRuntimeV1Schema(t *testing.T) *jsonschema.Schema {
	t.Helper()
	c := jsonschema.NewCompiler()
	sch, err := c.Compile(taskRuntimeV1SchemaPath)
	if err != nil {
		t.Fatalf("compile task-runtime-v1.json: %v", err)
	}
	return sch
}

func validateTaskRuntimeV1Fixture(t *testing.T, sch *jsonschema.Schema, path string, stripViolationField bool) error {
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

func TestTaskRuntimeV1PositiveFixturesValidate(t *testing.T) {
	sch := compileTaskRuntimeV1Schema(t)
	dir := "../docs/schema/fixtures/task-runtime-v1/positive"
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
		if err := validateTaskRuntimeV1Fixture(t, sch, path, false); err != nil {
			t.Errorf("positive fixture %s rejected by task-runtime-v1.json:\n%v", e.Name(), err)
		}
		validated++
	}
	if validated == 0 {
		t.Fatal("no positive fixtures found -- this contract check is running against nothing")
	}
}

func TestTaskRuntimeV1NegativeFixturesAreRejected(t *testing.T) {
	sch := compileTaskRuntimeV1Schema(t)
	dir := "../docs/schema/fixtures/task-runtime-v1/negative"
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
		if err := validateTaskRuntimeV1Fixture(t, sch, path, true); err == nil {
			t.Errorf("negative fixture %s validated successfully -- it should have been rejected", e.Name())
		}
		validated++
	}
	if validated == 0 {
		t.Fatal("no negative fixtures found -- this contract check is running against nothing")
	}
}

// TestTaskRuntimeV1AllTenFrameTypesHaveAPositiveFixture guards against a
// new frame type being added to the schema without a corresponding
// fixture (or vice versa) silently going untested.
func TestTaskRuntimeV1AllTenFrameTypesHaveAPositiveFixture(t *testing.T) {
	dir := "../docs/schema/fixtures/task-runtime-v1/positive"
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read %s: %v", dir, err)
	}
	seen := make(map[string]bool)
	for _, e := range entries {
		if filepath.Ext(e.Name()) != ".json" {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			t.Fatalf("read %s: %v", e.Name(), err)
		}
		var m map[string]interface{}
		if err := json.Unmarshal(data, &m); err != nil {
			t.Fatalf("%s: unmarshal: %v", e.Name(), err)
		}
		typ, _ := m["type"].(string)
		seen[typ] = true
	}
	want := []string{"start", "cancel", "ready", "log", "progress", "warning", "metric", "completed", "failed", "cancel_ack"}
	for _, w := range want {
		if !seen[w] {
			t.Errorf("no positive fixture exercises frame type %q", w)
		}
	}
}
