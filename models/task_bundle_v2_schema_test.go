package models_test

// Contract tests for the ADR-033 task-bundle/v2 manifest schema at
// docs/schema/task-bundle-v2.json (ADR-032/033 rollout phase 0, issue
// #439 step 5 -- schema and fixtures only, zero execution: no Go code
// parses, extracts, or dispatches a v2 bundle yet). Additive alongside
// pkg/taskbundle's task-bundle/1 (ADR-031); this file does not touch
// that package.
//
// Fixtures live in docs/schema/fixtures/task-bundle-v2/{positive,negative}.
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

const taskBundleV2SchemaPath = "../docs/schema/task-bundle-v2.json"

func compileTaskBundleV2Schema(t *testing.T) *jsonschema.Schema {
	t.Helper()
	c := jsonschema.NewCompiler()
	sch, err := c.Compile(taskBundleV2SchemaPath)
	if err != nil {
		t.Fatalf("compile task-bundle-v2.json: %v", err)
	}
	return sch
}

func validateTaskBundleV2Fixture(t *testing.T, sch *jsonschema.Schema, path string, stripViolationField bool) error {
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

func TestTaskBundleV2PositiveFixturesValidate(t *testing.T) {
	sch := compileTaskBundleV2Schema(t)
	dir := "../docs/schema/fixtures/task-bundle-v2/positive"
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
		if err := validateTaskBundleV2Fixture(t, sch, path, false); err != nil {
			t.Errorf("positive fixture %s rejected by task-bundle-v2.json:\n%v", e.Name(), err)
		}
		validated++
	}
	if validated == 0 {
		t.Fatal("no positive fixtures found -- this contract check is running against nothing")
	}
}

func TestTaskBundleV2NegativeFixturesAreRejected(t *testing.T) {
	sch := compileTaskBundleV2Schema(t)
	dir := "../docs/schema/fixtures/task-bundle-v2/negative"
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
		if err := validateTaskBundleV2Fixture(t, sch, path, true); err == nil {
			t.Errorf("negative fixture %s validated successfully -- it should have been rejected", e.Name())
		}
		validated++
	}
	if validated == 0 {
		t.Fatal("no negative fixtures found -- this contract check is running against nothing")
	}
}

// TestTaskBundleV2SchemaStructuralCases pins specific behaviors the
// fixture files above don't each get their own file for.
func TestTaskBundleV2SchemaStructuralCases(t *testing.T) {
	sch := compileTaskBundleV2Schema(t)
	base := func() map[string]interface{} {
		return map[string]interface{}{
			"format":           "brokoli.task-bundle/v2",
			"name":             "x",
			"interface_digest": "sha256:6b86b273ff34fce19d6b804eff5a3f5747ada4eaa22f1d49c01e52ddb7875b4b",
			"source_digest":    "sha256:d4735e3a265e16eee03f59718b9b5d03019c07d8b6c51f90da3a666eec13ab35",
			"payloads": []interface{}{
				map[string]interface{}{
					"id":             "p",
					"runtime":        "python",
					"os":             "any",
					"arch":           "any",
					"entrypoint":     map[string]interface{}{"module": "m", "symbol": "s"},
					"effects":        "pure",
					"payload_digest": "sha256:4b227777d4dd1fc61c6f884f48641d02b4d121d3fd328cb08b5531fcacdabf8a",
				},
			},
			"files": []interface{}{},
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

	t.Run("minimal python payload is accepted", func(t *testing.T) {
		if err := validate(t, base()); err != nil {
			t.Fatalf("minimal valid manifest should be accepted: %v", err)
		}
	})

	t.Run("build provenance is optional", func(t *testing.T) {
		m := base()
		delete(m, "build")
		if err := validate(t, m); err != nil {
			t.Fatalf("manifest with no build provenance should be accepted: %v", err)
		}
	})

	t.Run("dependency_lock is optional", func(t *testing.T) {
		if err := validate(t, base()); err != nil {
			t.Fatalf("payload with no dependency_lock should be accepted: %v", err)
		}
	})

	t.Run("empty payloads array is rejected", func(t *testing.T) {
		m := base()
		m["payloads"] = []interface{}{}
		if err := validate(t, m); err == nil {
			t.Fatal("expected rejection of an empty payloads array (minItems: 1)")
		}
	})

	t.Run("entrypoint mixing grammars is rejected", func(t *testing.T) {
		m := base()
		payloads := m["payloads"].([]interface{})
		payload := payloads[0].(map[string]interface{})
		payload["entrypoint"] = map[string]interface{}{"module": "m", "symbol": "s", "executable": "bin/x"}
		if err := validate(t, m); err == nil {
			t.Fatal("expected rejection of an entrypoint mixing module/symbol with executable")
		}
	})

	t.Run("container runtime without image is rejected", func(t *testing.T) {
		m := base()
		payloads := m["payloads"].([]interface{})
		payload := payloads[0].(map[string]interface{})
		payload["runtime"] = "container"
		payload["entrypoint"] = map[string]interface{}{"command": []interface{}{"run"}}
		delete(payload, "requires")
		if err := validate(t, m); err == nil {
			t.Fatal("expected rejection of a container payload with no image reference")
		}
	})

	t.Run("container runtime with image is accepted", func(t *testing.T) {
		m := base()
		payloads := m["payloads"].([]interface{})
		payload := payloads[0].(map[string]interface{})
		payload["runtime"] = "container"
		payload["entrypoint"] = map[string]interface{}{"command": []interface{}{"run"}}
		payload["image"] = map[string]interface{}{"digest": "sha256:6b86b273ff34fce19d6b804eff5a3f5747ada4eaa22f1d49c01e52ddb7875b4b"}
		if err := validate(t, m); err != nil {
			t.Fatalf("container payload with an image reference should be accepted: %v", err)
		}
	})

	t.Run("unrecognized runtime class is rejected", func(t *testing.T) {
		m := base()
		payloads := m["payloads"].([]interface{})
		payload := payloads[0].(map[string]interface{})
		payload["runtime"] = "rust"
		if err := validate(t, m); err == nil {
			t.Fatal("expected rejection of a runtime class outside the closed vocabulary (ADR-033 section 3)")
		}
	})
}
