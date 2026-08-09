package models

import (
	"encoding/json"
	"reflect"
	"regexp"
	"strings"
	"testing"
)

// slugFromName mirrors the slug generation logic in handlers_pipeline.go Create().
func slugFromName(name string) string {
	pid := strings.ToLower(name)
	pid = strings.ReplaceAll(pid, " ", "-")
	pid = regexp.MustCompile(`[^a-z0-9-]`).ReplaceAllString(pid, "")
	pid = regexp.MustCompile(`-+`).ReplaceAllString(pid, "-")
	pid = strings.Trim(pid, "-")
	return pid
}

func TestPipelineID_AutoGenerate(t *testing.T) {
	tests := []struct {
		name     string
		expected string
	}{
		{"Acme Orders ETL", "acme-orders-etl"},
		{"my-pipeline", "my-pipeline"},
		{"Hello World!", "hello-world"},
		{"  Spaced  Out  ", "spaced-out"}, // double spaces become -- then collapsed to -
		{"UPPER CASE", "upper-case"},
		{"special@#$chars", "specialchars"},
		{"multi---dash", "multi-dash"},
		{"trailing-", "trailing"},
		{"-leading", "leading"},
		{"123 numeric start", "123-numeric-start"},
		{"a", "a"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := slugFromName(tt.name)
			if got != tt.expected {
				t.Errorf("slugFromName(%q) = %q, want %q", tt.name, got, tt.expected)
			}
		})
	}
}

func TestPipelineSource_Constants(t *testing.T) {
	if PipelineSourceUI != "ui" {
		t.Errorf("PipelineSourceUI = %q, want %q", PipelineSourceUI, "ui")
	}
	if PipelineSourceGit != "git" {
		t.Errorf("PipelineSourceGit = %q, want %q", PipelineSourceGit, "git")
	}
}

func TestPipeline_Validate_WithPipelineIDAndSource(t *testing.T) {
	p := Pipeline{
		Name:       "Test",
		PipelineID: "test",
		Source:     PipelineSourceGit,
		Nodes:      []Node{},
		Edges:      []Edge{},
	}
	if err := p.Validate(); err != nil {
		t.Errorf("valid pipeline with pipeline_id and source should not error: %v", err)
	}
}

func TestIsIRVersionSupported(t *testing.T) {
	cases := []struct {
		v    string
		want bool
	}{
		{"", true},    // pre-versioned pipelines are always accepted
		{"2.0", true}, // current IR version
		{"1.0", false},
		{"99.0", false},
	}
	for _, c := range cases {
		if got := IsIRVersionSupported(c.v); got != c.want {
			t.Errorf("IsIRVersionSupported(%q) = %v, want %v", c.v, got, c.want)
		}
	}
}

// TestNode_CapabilitiesRoundTrip pins the JSON shape of the "capabilities"
// field added in Phase 0 protocol alignment so this host correctly
// accepts pipelines deployed by the upgraded SDK, which now tags every
// node with a capabilities list (e.g. ["source", "dataset-output"]).
func TestNode_CapabilitiesRoundTrip(t *testing.T) {
	n := Node{
		ID:           "n1",
		Type:         NodeTypeCode,
		Name:         "My Source",
		Capabilities: []string{CapabilitySource, CapabilityDatasetOutput},
	}
	buf, err := json.Marshal(n)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(buf), `"capabilities":["source","dataset-output"]`) {
		t.Errorf("expected capabilities field in JSON, got: %s", buf)
	}

	var got Node
	if err := json.Unmarshal(buf, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !reflect.DeepEqual(got.Capabilities, n.Capabilities) {
		t.Errorf("Capabilities round-trip = %v, want %v", got.Capabilities, n.Capabilities)
	}

	// Nodes with no capabilities field at all (old SDK clients) must
	// decode to a nil/empty slice, not error.
	var old Node
	if err := json.Unmarshal([]byte(`{"id":"n2","type":"source_file"}`), &old); err != nil {
		t.Fatalf("unmarshal old-style node: %v", err)
	}
	if len(old.Capabilities) != 0 {
		t.Errorf("expected empty Capabilities for old-style node, got: %v", old.Capabilities)
	}
}

// TestPipeline_IRVersionRoundTrip pins the JSON shape of the top-level
// "ir_version" field added in Phase 0 protocol alignment.
func TestPipeline_IRVersionRoundTrip(t *testing.T) {
	p := Pipeline{Name: "x", IRVersion: "2.0", Nodes: []Node{}, Edges: []Edge{}}
	buf, err := json.Marshal(p)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(buf), `"ir_version":"2.0"`) {
		t.Errorf("expected ir_version field in JSON, got: %s", buf)
	}

	var got Pipeline
	if err := json.Unmarshal(buf, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.IRVersion != "2.0" {
		t.Errorf("IRVersion round-trip = %q, want %q", got.IRVersion, "2.0")
	}

	// Old pipeline JSON with no ir_version field must decode to empty
	// string, not error, and Validate() must still accept it.
	var old Pipeline
	if err := json.Unmarshal([]byte(`{"name":"legacy"}`), &old); err != nil {
		t.Fatalf("unmarshal old-style pipeline: %v", err)
	}
	if old.IRVersion != "" {
		t.Errorf("expected empty IRVersion for old-style pipeline, got: %q", old.IRVersion)
	}
}

func TestEdge_ConditionRoundTrip(t *testing.T) {
	trueValue := true
	falseValue := false
	edges := []Edge{
		{From: "condition", To: "yes", Condition: &trueValue},
		{From: "condition", To: "no", Condition: &falseValue},
		{From: "source", To: "condition"},
	}

	buf, err := json.Marshal(edges)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	jsonText := string(buf)
	if !strings.Contains(jsonText, `"condition":true`) || !strings.Contains(jsonText, `"condition":false`) {
		t.Fatalf("conditional edge values missing from JSON: %s", jsonText)
	}
	if strings.Contains(jsonText, `"to":"condition","condition"`) {
		t.Fatalf("ordinary edge should omit condition: %s", jsonText)
	}

	var got []Edge
	if err := json.Unmarshal(buf, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got[0].Condition == nil || !*got[0].Condition {
		t.Errorf("true condition was not preserved: %#v", got[0])
	}
	if got[1].Condition == nil || *got[1].Condition {
		t.Errorf("false condition was not preserved: %#v", got[1])
	}
	if got[2].Condition != nil {
		t.Errorf("omitted condition should remain nil: %#v", got[2])
	}
}

func TestPipelineValidate_ConditionalEdgesFailClosedBeforeIR21Rollout(t *testing.T) {
	condition := true
	p := Pipeline{
		Name:      "conditional",
		IRVersion: "2.0",
		Nodes: []Node{
			{ID: "check", Type: NodeTypeCondition},
			{ID: "yes", Type: NodeTypeNotify},
		},
		Edges: []Edge{{From: "check", To: "yes", Condition: &condition}},
	}

	err := p.Validate()
	if err == nil || !strings.Contains(err.Error(), "requires pipeline IR 2.1") {
		t.Fatalf("IR 2.0 conditional edge error = %v", err)
	}

	p.IRVersion = ConditionalEdgesIRVersion
	err = p.Validate()
	if err == nil || !strings.Contains(err.Error(), "unsupported pipeline IR version") {
		t.Fatalf("IR 2.1 should remain unsupported until runtime rollout, error = %v", err)
	}
}
