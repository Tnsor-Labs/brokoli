package models_test

// Contract tests binding models.Pipeline to the canonical IR schema at
// docs/schema/pipeline-ir-2.2.json (issue #109 M1, ADR-014 rule 1; ADR-032
// rollout step 2/#439 for the 'interface'/'parameters' fields).
//
// Two directions of drift are caught:
//   - model grows a field the schema doesn't know: the fully-populated
//     round-trip fails, because the schema sets additionalProperties: false;
//     the reflection sweep names the missing property exactly.
//   - schema documents a property the model lost: the reflection sweep
//     fails in the other direction.
//
// Every seeded template must validate too -- they are the first payloads a
// new install persists, so they are the minimum honest fixtures.

import (
	"bytes"
	"encoding/json"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/Tnsor-Labs/brokoli/models"
	"github.com/Tnsor-Labs/brokoli/pkg/templates"
	"github.com/santhosh-tekuri/jsonschema/v6"
)

const schemaPath = "../docs/schema/pipeline-ir-2.2.json"

// taskInterfaceSchemaURL is task-interface-v1.json's own declared $id --
// taskInterfaceSchemaPath (the file path) is declared in
// task_interface_schema_test.go, same package.
const taskInterfaceSchemaURL = "https://github.com/Tnsor-Labs/brokoli/docs/schema/task-interface-v1.json"

// taskInterfaceSchemaURL/taskInterfaceSchemaPath are shared with
// task_interface_schema_test.go (same package) -- pipeline-ir-2.2.json's
// 'interface'/'parameters' fields $ref into task-interface-v1.json by its
// absolute $id, so the compiler needs that document registered via
// AddResource before compiling schemaPath: a bare Compile() of it first
// does not share that registration, and once compilation of the
// referencing document begins, a relative "task-interface-v1.json#/..."
// $ref resolves into the https:// URL-space both schemas' $id declare,
// not the filesystem.
func compileSchema(t *testing.T) *jsonschema.Schema {
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
	sch, err := c.Compile(schemaPath)
	if err != nil {
		t.Fatalf("compile canonical schema: %v", err)
	}
	return sch
}

func validate(t *testing.T, sch *jsonschema.Schema, v interface{}) error {
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

func fullyPopulatedPipeline() *models.Pipeline {
	tr := true
	fa := false
	now := time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC)
	return &models.Pipeline{
		ID:        "pipe-1",
		IRVersion: models.TaskInterfaceIRVersion,
		Name:      "everything",
		Description: "every field set, so a model field the schema " +
			"doesn't know fails additionalProperties",
		Nodes: []models.Node{
			{
				ID: "src", Type: models.NodeTypeSourceFile, Name: "Source",
				Config:       map[string]interface{}{"path": "/tmp/in.csv", "custom_key": 1},
				Position:     models.Position{X: 50, Y: 200},
				Capabilities: []string{models.CapabilitySource, models.CapabilityDatasetOutput},
				Interface: map[string]interface{}{
					"contract": "brokoli.task-interface/v1",
					"inputs":   map[string]interface{}{},
					"outputs": map[string]interface{}{
						"result": map[string]interface{}{"value": map[string]interface{}{"kind": "dataset"}},
					},
				},
			},
			{ID: "gate", Type: models.NodeTypeCondition, Name: "Gate",
				Config: map[string]interface{}{"expression": "row_count > 0"}},
			{ID: "yes", Type: models.NodeTypeNotify, Name: "Yes", Config: map[string]interface{}{}},
			{ID: "no", Type: models.NodeTypeNotify, Name: "No", Config: map[string]interface{}{}},
		},
		Edges: []models.Edge{
			{From: "src", To: "gate"},
			{From: "gate", To: "yes", Condition: &tr},
			{From: "gate", To: "no", Condition: &fa},
		},
		Schedule:         "0 6 * * *",
		ScheduleTimezone: "America/New_York",
		WebhookURL:       "https://hooks.example/x",
		Params:           map[string]string{"limit": "10"},
		Parameters: map[string]interface{}{
			"threshold": map[string]interface{}{
				"type": map[string]interface{}{"kind": "float64"}, "required": false, "default": 0.5,
			},
		},
		Tags: []string{"etl"},
		Hooks: map[string]models.Hook{
			"on_failure": {Type: "webhook", URL: "https://hooks.example/f", Enabled: true,
				Extra: map[string]string{"channel": "alerts"}},
		},
		SLADeadline: "07:30",
		SLATimezone: "UTC",
		DependsOn:   []string{"upstream"},
		DependencyRules: []models.DependencyRule{
			{PipelineID: "upstream", State: models.DepStateSucceeded, WithinSec: 3600, Mode: models.DepModeGate},
		},
		WebhookToken: "whk_x",
		Extensions: map[string]json.RawMessage{
			"x_future_feature": json.RawMessage(`{"anything": true}`),
		},
		Enabled:     true,
		PipelineID:  "everything",
		Source:      models.PipelineSourceUI,
		WorkspaceID: "ws-1",
		OrgID:       "org-1",
		CreatedAt:   now,
		UpdatedAt:   now,
	}
}

func TestFullyPopulatedPipelineMatchesSchema(t *testing.T) {
	sch := compileSchema(t)
	if err := validate(t, sch, fullyPopulatedPipeline()); err != nil {
		t.Fatalf("fully populated pipeline rejected by canonical schema:\n%v", err)
	}
}

// TestGoldenClientFixturesMatchSchema validates real client-emitted
// payloads checked into docs/schema/fixtures/ — currently an SDK-compiled
// IR 2.1 pipeline exercising conditional routing, pagination execution
// policy, module-context packaging, and node_key. Refresh a fixture by
// recompiling it with the client that owns it (provenance in the fixture
// README); a failure here means a client and this server disagree about
// the contract, and one of them is wrong on purpose.
func TestGoldenClientFixturesMatchSchema(t *testing.T) {
	sch := compileSchema(t)
	entries, err := os.ReadDir("../docs/schema/fixtures")
	if err != nil {
		t.Fatalf("read fixtures dir: %v", err)
	}
	validated := 0
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		data, err := os.ReadFile("../docs/schema/fixtures/" + e.Name())
		if err != nil {
			t.Fatal(err)
		}
		inst, err := jsonschema.UnmarshalJSON(bytes.NewReader(data))
		if err != nil {
			t.Fatalf("%s: %v", e.Name(), err)
		}
		if err := sch.Validate(inst); err != nil {
			t.Errorf("fixture %s rejected by canonical schema:\n%v", e.Name(), err)
		}
		validated++
	}
	if validated == 0 {
		t.Fatal("no golden fixtures found -- the cross-model contract check is running against nothing")
	}
}

func TestEverySeededTemplateMatchesSchema(t *testing.T) {
	sch := compileSchema(t)
	for _, tmpl := range templates.Builtin {
		p := &models.Pipeline{Name: tmpl.Name, Nodes: tmpl.Nodes, Edges: tmpl.Edges}
		if err := validate(t, sch, p); err != nil {
			t.Errorf("template %q rejected by canonical schema:\n%v", tmpl.Name, err)
		}
	}
}

func TestSchemaRejectsContractViolations(t *testing.T) {
	sch := compileSchema(t)
	base := func() map[string]interface{} {
		data, _ := json.Marshal(fullyPopulatedPipeline())
		var m map[string]interface{}
		_ = json.Unmarshal(data, &m)
		return m
	}

	cases := []struct {
		name   string
		mutate func(m map[string]interface{})
	}{
		{"unknown top-level field", func(m map[string]interface{}) { m["surprise"] = 1 }},
		{"unknown edge field", func(m map[string]interface{}) {
			m["edges"].([]interface{})[0].(map[string]interface{})["branch"] = "yes"
		}},
		{"string edge condition", func(m map[string]interface{}) {
			m["edges"].([]interface{})[1].(map[string]interface{})["condition"] = "true"
		}},
		{"node missing id", func(m map[string]interface{}) {
			delete(m["nodes"].([]interface{})[0].(map[string]interface{}), "id")
		}},
		{"bad hook type", func(m map[string]interface{}) {
			m["hooks"].(map[string]interface{})["on_failure"].(map[string]interface{})["type"] = "carrier-pigeon"
		}},
		{"unknown hook event", func(m map[string]interface{}) {
			m["hooks"].(map[string]interface{})["on_teardown"] = map[string]interface{}{
				"type": "webhook", "url": "https://x",
			}
		}},
		{"bad ir_version", func(m map[string]interface{}) { m["ir_version"] = "3.0" }},
		{"bad capability", func(m map[string]interface{}) {
			m["nodes"].([]interface{})[0].(map[string]interface{})["capabilities"] = []interface{}{"quantum"}
		}},
		{"node interface with the wrong contract string", func(m map[string]interface{}) {
			m["nodes"].([]interface{})[0].(map[string]interface{})["interface"].(map[string]interface{})["contract"] = "not-the-right-contract-string"
		}},
		{"pipeline parameter with both required:true and a default", func(m map[string]interface{}) {
			m["parameters"].(map[string]interface{})["threshold"].(map[string]interface{})["required"] = true
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := base()
			tc.mutate(m)
			if err := validate(t, sch, m); err == nil {
				t.Fatalf("schema accepted a contract violation: %s", tc.name)
			}
		})
	}
}

// TestSchemaAndModelDeclareTheSameFields sweeps both directions by name, so
// drift produces an error naming the exact property instead of a generic
// additionalProperties failure.
func TestSchemaAndModelDeclareTheSameFields(t *testing.T) {
	schemaProps := topLevelSchemaProperties(t)

	modelProps := map[string]bool{}
	pt := reflect.TypeOf(models.Pipeline{})
	for i := 0; i < pt.NumField(); i++ {
		tag := pt.Field(i).Tag.Get("json")
		name := strings.Split(tag, ",")[0]
		if name != "" && name != "-" {
			modelProps[name] = true
		}
	}

	for name := range modelProps {
		if !schemaProps[name] {
			t.Errorf("models.Pipeline field %q is missing from the canonical schema -- update docs/schema/pipeline-ir-2.2.json in the same change", name)
		}
	}
	for name := range schemaProps {
		if !modelProps[name] {
			t.Errorf("schema property %q has no models.Pipeline field -- the schema documents a contract the server no longer honors", name)
		}
	}
}

// TestSchemaAndModelDeclareTheSameNodeFields is
// TestSchemaAndModelDeclareTheSameFields's counterpart for models.Node
// against $defs.node.properties -- added alongside the ADR-032 'interface'
// field (issue #439 step 2) since no such sweep existed for Node before.
func TestSchemaAndModelDeclareTheSameNodeFields(t *testing.T) {
	schemaProps := defSchemaProperties(t, "node")

	modelProps := map[string]bool{}
	nt := reflect.TypeOf(models.Node{})
	for i := 0; i < nt.NumField(); i++ {
		tag := nt.Field(i).Tag.Get("json")
		name := strings.Split(tag, ",")[0]
		if name != "" && name != "-" {
			modelProps[name] = true
		}
	}

	for name := range modelProps {
		if !schemaProps[name] {
			t.Errorf("models.Node field %q is missing from the canonical schema -- update docs/schema/pipeline-ir-2.2.json's $defs.node in the same change", name)
		}
	}
	for name := range schemaProps {
		if !modelProps[name] {
			t.Errorf("schema $defs.node property %q has no models.Node field -- the schema documents a contract the server no longer honors", name)
		}
	}
}

func topLevelSchemaProperties(t *testing.T) map[string]bool {
	t.Helper()
	raw, err := jsonschema.UnmarshalJSON(bytes.NewReader(mustReadSchema(t)))
	if err != nil {
		t.Fatal(err)
	}
	doc := raw.(map[string]interface{})
	props := doc["properties"].(map[string]interface{})
	out := map[string]bool{}
	for name := range props {
		out[name] = true
	}
	return out
}

func defSchemaProperties(t *testing.T, defName string) map[string]bool {
	t.Helper()
	raw, err := jsonschema.UnmarshalJSON(bytes.NewReader(mustReadSchema(t)))
	if err != nil {
		t.Fatal(err)
	}
	doc := raw.(map[string]interface{})
	defs := doc["$defs"].(map[string]interface{})
	def, ok := defs[defName].(map[string]interface{})
	if !ok {
		t.Fatalf("schema has no $defs.%s", defName)
	}
	props := def["properties"].(map[string]interface{})
	out := map[string]bool{}
	for name := range props {
		out[name] = true
	}
	return out
}

func mustReadSchema(t *testing.T) []byte {
	t.Helper()
	data, err := os.ReadFile(schemaPath)
	if err != nil {
		t.Fatalf("read schema: %v", err)
	}
	return data
}
