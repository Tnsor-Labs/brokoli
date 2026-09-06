package models_test

// Contract tests for the ADR-033 worker capabilities schema at
// docs/schema/worker-capabilities-v1.json (ADR-032/033 rollout phase 1,
// issue #439 step 5 -- schema and fixtures only: no worker registration
// endpoint, no placement predicate, and no extension of
// extensions.InstanceWorkOrder exist yet).
//
// Fixtures live in docs/schema/fixtures/worker-capabilities-v1/{positive,negative}.
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

const workerCapabilitiesSchemaPath = "../docs/schema/worker-capabilities-v1.json"

func compileWorkerCapabilitiesSchema(t *testing.T) *jsonschema.Schema {
	t.Helper()
	c := jsonschema.NewCompiler()
	sch, err := c.Compile(workerCapabilitiesSchemaPath)
	if err != nil {
		t.Fatalf("compile worker-capabilities-v1.json: %v", err)
	}
	return sch
}

func validateWorkerCapabilitiesFixture(t *testing.T, sch *jsonschema.Schema, path string, stripViolationField bool) error {
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

func TestWorkerCapabilitiesPositiveFixturesValidate(t *testing.T) {
	sch := compileWorkerCapabilitiesSchema(t)
	dir := "../docs/schema/fixtures/worker-capabilities-v1/positive"
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
		if err := validateWorkerCapabilitiesFixture(t, sch, path, false); err != nil {
			t.Errorf("positive fixture %s rejected by worker-capabilities-v1.json:\n%v", e.Name(), err)
		}
		validated++
	}
	if validated == 0 {
		t.Fatal("no positive fixtures found -- this contract check is running against nothing")
	}
}

func TestWorkerCapabilitiesNegativeFixturesAreRejected(t *testing.T) {
	sch := compileWorkerCapabilitiesSchema(t)
	dir := "../docs/schema/fixtures/worker-capabilities-v1/negative"
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
		if err := validateWorkerCapabilitiesFixture(t, sch, path, true); err == nil {
			t.Errorf("negative fixture %s validated successfully -- it should have been rejected", e.Name())
		}
		validated++
	}
	if validated == 0 {
		t.Fatal("no negative fixtures found -- this contract check is running against nothing")
	}
}

// TestWorkerCapabilitiesSchemaStructuralCases pins specific behaviors the
// fixture files above don't each get their own file for.
func TestWorkerCapabilitiesSchemaStructuralCases(t *testing.T) {
	sch := compileWorkerCapabilitiesSchema(t)
	base := func() map[string]interface{} {
		return map[string]interface{}{
			"worker_id": "w",
			"protocols": []interface{}{"brokoli.instance/v2"},
			"platform":  map[string]interface{}{"os": "linux", "arch": "amd64"},
			"runtimes":  []interface{}{},
			"io":        []interface{}{"batch-reference"},
			"isolation": []interface{}{"process"},
			"resources": map[string]interface{}{"cpu_millis": 1000, "memory_bytes": 1073741824},
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

	t.Run("empty runtimes is fine (a code-node-only worker)", func(t *testing.T) {
		if err := validate(t, base()); err != nil {
			t.Fatalf("empty runtimes should be accepted: %v", err)
		}
	})

	t.Run("labels is optional", func(t *testing.T) {
		m := base()
		delete(m, "labels")
		if err := validate(t, m); err != nil {
			t.Fatalf("missing labels should be accepted: %v", err)
		}
	})

	t.Run("runtime adapter version is optional", func(t *testing.T) {
		m := base()
		m["runtimes"] = []interface{}{map[string]interface{}{"class": "native", "versions": []interface{}{"1.0.0"}}}
		if err := validate(t, m); err != nil {
			t.Fatalf("a runtime with no adapter version should be accepted: %v", err)
		}
	})

	t.Run("isolation accepts a mechanism name outside any fixed list", func(t *testing.T) {
		m := base()
		m["isolation"] = []interface{}{"gvisor"}
		if err := validate(t, m); err != nil {
			t.Fatalf("isolation is deliberately not a closed enum: %v", err)
		}
	})

	t.Run("empty protocols array is rejected", func(t *testing.T) {
		m := base()
		m["protocols"] = []interface{}{}
		if err := validate(t, m); err == nil {
			t.Fatal("expected rejection of an empty protocols array (minItems: 1)")
		}
	})

	t.Run("runtime with empty versions array is rejected", func(t *testing.T) {
		m := base()
		m["runtimes"] = []interface{}{map[string]interface{}{"class": "python", "versions": []interface{}{}}}
		if err := validate(t, m); err == nil {
			t.Fatal("expected rejection of a runtime capability with an empty versions array")
		}
	})
}
