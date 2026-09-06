package models_test

// Structural drift guard for the ADR-032 rollout step 3 cross-SDK
// differential fixtures at docs/schema/fixtures/task-interface/differential
// (ADR-032 section 14, issue #439). This test does not exercise any SDK --
// each SDK's own test suite loads the same fixture directory and asserts
// its actual compiled output matches. This test only guarantees the
// fixtures themselves stay schema-valid as task-interface-v1.json evolves,
// so a schema change and its fixtures can never silently drift apart.

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

const differentialFixtureDir = "../docs/schema/fixtures/task-interface/differential"

type differentialFixture struct {
	Description               string          `json:"description"`
	Python                    string          `json:"python"`
	TypeScript                string          `json:"typescript"`
	ExpectedNodeInterface     json.RawMessage `json:"expected_node_interface"`
	ExpectedPipelineParameter json.RawMessage `json:"expected_pipeline_parameters"`
}

func compilePipelineParametersSchema(t *testing.T) *jsonschema.Schema {
	t.Helper()
	c := jsonschema.NewCompiler()
	sch, err := c.Compile(taskInterfaceSchemaPath + "#/$defs/pipeline_parameters")
	if err != nil {
		t.Fatalf("compile task-interface-v1.json#/$defs/pipeline_parameters: %v", err)
	}
	return sch
}

func TestDifferentialFixturesAreSchemaValid(t *testing.T) {
	entries, err := os.ReadDir(differentialFixtureDir)
	if err != nil {
		t.Fatalf("read %s: %v", differentialFixtureDir, err)
	}

	interfaceSchema := compileTaskInterfaceSchema(t)
	parametersSchema := compilePipelineParametersSchema(t)

	found := 0
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		found++
		path := filepath.Join(differentialFixtureDir, entry.Name())
		t.Run(entry.Name(), func(t *testing.T) {
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("read %s: %v", path, err)
			}
			var fx differentialFixture
			if err := json.Unmarshal(data, &fx); err != nil {
				t.Fatalf("%s: unmarshal: %v", path, err)
			}
			if fx.Description == "" {
				t.Errorf("%s: missing description", path)
			}
			if fx.Python == "" {
				t.Errorf("%s: missing python", path)
			}
			if fx.TypeScript == "" {
				t.Errorf("%s: missing typescript", path)
			}

			hasInterface := len(fx.ExpectedNodeInterface) > 0 && string(fx.ExpectedNodeInterface) != "null"
			hasParameters := len(fx.ExpectedPipelineParameter) > 0 && string(fx.ExpectedPipelineParameter) != "null"
			if hasInterface == hasParameters {
				t.Fatalf("%s: exactly one of expected_node_interface/expected_pipeline_parameters must be non-null, got interface=%v parameters=%v", path, hasInterface, hasParameters)
			}

			if hasInterface {
				inst, err := jsonschema.UnmarshalJSON(bytes.NewReader(fx.ExpectedNodeInterface))
				if err != nil {
					t.Fatalf("%s: reparse expected_node_interface: %v", path, err)
				}
				if err := interfaceSchema.Validate(inst); err != nil {
					t.Errorf("%s: expected_node_interface fails task-interface-v1 validation: %v", path, err)
				}
			}
			if hasParameters {
				inst, err := jsonschema.UnmarshalJSON(bytes.NewReader(fx.ExpectedPipelineParameter))
				if err != nil {
					t.Fatalf("%s: reparse expected_pipeline_parameters: %v", path, err)
				}
				if err := parametersSchema.Validate(inst); err != nil {
					t.Errorf("%s: expected_pipeline_parameters fails pipeline_parameters validation: %v", path, err)
				}
			}
		})
	}
	if found == 0 {
		t.Fatalf("no fixtures found in %s", differentialFixtureDir)
	}
}
