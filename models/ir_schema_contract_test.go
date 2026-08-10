package models_test

// Contract tests binding models.Pipeline to the canonical IR schema at
// docs/schema/pipeline-ir-2.1.json (issue #109 M1, ADR-014 rule 1).
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

const schemaPath = "../docs/schema/pipeline-ir-2.1.json"

func compileSchema(t *testing.T) *jsonschema.Schema {
	t.Helper()
	c := jsonschema.NewCompiler()
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
		IRVersion: "2.1",
		Name:      "everything",
		Description: "every field set, so a model field the schema " +
			"doesn't know fails additionalProperties",
		Nodes: []models.Node{
			{
				ID: "src", Type: models.NodeTypeSourceFile, Name: "Source",
				Config:       map[string]interface{}{"path": "/tmp/in.csv", "custom_key": 1},
				Position:     models.Position{X: 50, Y: 200},
				Capabilities: []string{models.CapabilitySource, models.CapabilityDatasetOutput},
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
		Tags:             []string{"etl"},
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
			t.Errorf("models.Pipeline field %q is missing from the canonical schema -- update docs/schema/pipeline-ir-2.1.json in the same change", name)
		}
	}
	for name := range schemaProps {
		if !modelProps[name] {
			t.Errorf("schema property %q has no models.Pipeline field -- the schema documents a contract the server no longer honors", name)
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

func mustReadSchema(t *testing.T) []byte {
	t.Helper()
	data, err := os.ReadFile(schemaPath)
	if err != nil {
		t.Fatalf("read schema: %v", err)
	}
	return data
}
