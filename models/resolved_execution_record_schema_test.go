package models_test

// Contract tests for the ADR-033 resolved execution record schema at
// docs/schema/resolved-execution-record-v1.json (ADR-032/033 rollout
// phase 1, issue #439 step 5 -- schema and fixtures only: no physical
// planning integration, no ClaimAttempt wiring, and retries do not reuse
// this record anywhere yet).
//
// Fixtures live in docs/schema/fixtures/resolved-execution-record-v1/{positive,negative}.
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

const resolvedExecutionRecordSchemaPath = "../docs/schema/resolved-execution-record-v1.json"

func compileResolvedExecutionRecordSchema(t *testing.T) *jsonschema.Schema {
	t.Helper()
	c := jsonschema.NewCompiler()
	sch, err := c.Compile(resolvedExecutionRecordSchemaPath)
	if err != nil {
		t.Fatalf("compile resolved-execution-record-v1.json: %v", err)
	}
	return sch
}

func validateResolvedExecutionRecordFixture(t *testing.T, sch *jsonschema.Schema, path string, stripViolationField bool) error {
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

func TestResolvedExecutionRecordPositiveFixturesValidate(t *testing.T) {
	sch := compileResolvedExecutionRecordSchema(t)
	dir := "../docs/schema/fixtures/resolved-execution-record-v1/positive"
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
		if err := validateResolvedExecutionRecordFixture(t, sch, path, false); err != nil {
			t.Errorf("positive fixture %s rejected by resolved-execution-record-v1.json:\n%v", e.Name(), err)
		}
		validated++
	}
	if validated == 0 {
		t.Fatal("no positive fixtures found -- this contract check is running against nothing")
	}
}

func TestResolvedExecutionRecordNegativeFixturesAreRejected(t *testing.T) {
	sch := compileResolvedExecutionRecordSchema(t)
	dir := "../docs/schema/fixtures/resolved-execution-record-v1/negative"
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
		if err := validateResolvedExecutionRecordFixture(t, sch, path, true); err == nil {
			t.Errorf("negative fixture %s validated successfully -- it should have been rejected", e.Name())
		}
		validated++
	}
	if validated == 0 {
		t.Fatal("no negative fixtures found -- this contract check is running against nothing")
	}
}
