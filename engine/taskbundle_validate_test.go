package engine

// task_bundle config validation (ADR-031): a code node may carry either
// a 'script' or a 'task_bundle' reference. The validator must accept the
// well-formed bundle shape, refuse malformed digests/formats/object
// shapes, enforce mutual exclusivity with 'script', and refuse bundles
// whose language this server cannot mount yet.

import (
	"strings"
	"testing"

	"github.com/Tnsor-Labs/brokoli/models"
	"github.com/Tnsor-Labs/brokoli/pkg/taskbundle"
)

var validDigest = "sha256:" + strings.Repeat("a", 64)

func bundleConfig(over map[string]interface{}) *models.Pipeline {
	nodes := []models.Node{
		{ID: "src", Type: models.NodeTypeSourceFile, Name: "In", Config: map[string]interface{}{"path": "/tmp/in.csv"}},
		{ID: "code", Type: models.NodeTypeCode, Name: "Code", Config: map[string]interface{}{
			"task_bundle": map[string]interface{}{"digest": validDigest, "format": taskbundle.Format},
		}},
	}
	if over != nil {
		nodes = []models.Node{
			{ID: "src", Type: models.NodeTypeSourceFile, Name: "In", Config: map[string]interface{}{"path": "/tmp/in.csv"}},
			{ID: "code", Type: models.NodeTypeCode, Name: "Code", Config: over},
		}
	}
	return &models.Pipeline{Name: "task-bundle-valid", Nodes: nodes, Edges: []models.Edge{{From: "src", To: "code"}}}
}

func TestValidateTaskBundle_AcceptsWellFormedReference(t *testing.T) {
	ve := ValidatePipeline(bundleConfig(nil))
	if ve.HasErrors() {
		t.Fatalf("valid task_bundle reference rejected: %v", ve.Errors)
	}
}

func TestValidateTaskBundle_CapabilitiesAdvertiseTheFeature(t *testing.T) {
	found := false
	for _, f := range models.SupportedExecutionFeatures {
		if f == "task-bundles" {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("'task-bundles' is not in models.SupportedExecutionFeatures; the capabilities endpoint and SDK preflight cannot see it")
	}
}

func TestValidateTaskBundle_MalformedReferencesRejected(t *testing.T) {
	ok := func(c map[string]interface{}) bool {
		return !ValidatePipeline(bundleConfig(c)).HasErrors()
	}
	cases := map[string]struct {
		config map[string]interface{}
		want   string
	}{
		"not an object": {
			config: map[string]interface{}{"task_bundle": "sha256:abc"},
			want:   "'task_bundle' must be an object",
		},
		"missing digest": {
			config: map[string]interface{}{"task_bundle": map[string]interface{}{"format": taskbundle.Format}},
			want:   "task_bundle.digest",
		},
		"bad digest charset": {
			config: map[string]interface{}{"task_bundle": map[string]interface{}{"digest": "sha256:XYZ", "format": taskbundle.Format}},
			want:   "task_bundle.digest",
		},
		"short digest": {
			config: map[string]interface{}{"task_bundle": map[string]interface{}{"digest": "sha256:abc", "format": taskbundle.Format}},
			want:   "task_bundle.digest",
		},
		"wrong scheme": {
			config: map[string]interface{}{"task_bundle": map[string]interface{}{"digest": "md5:" + strings.Repeat("a", 64), "format": taskbundle.Format}},
			want:   "task_bundle.digest",
		},
		"unknown format": {
			config: map[string]interface{}{"task_bundle": map[string]interface{}{"digest": validDigest, "format": "task-bundle/9"}},
			want:   "task_bundle.format",
		},
		"both script and bundle": {
			config: map[string]interface{}{
				"script":      "output_data = {}",
				"task_bundle": map[string]interface{}{"digest": validDigest, "format": taskbundle.Format},
			},
			want: "mutually exclusive",
		},
		"typescript bundle": {
			config: map[string]interface{}{
				"language":    "typescript",
				"task_bundle": map[string]interface{}{"digest": validDigest, "format": taskbundle.Format},
			},
			want: "python bundles",
		},
	}
	for name, tc := range cases {
		if ok(tc.config) {
			t.Errorf("%s: malformed task_bundle config accepted", name)
			continue
		}
		ve := ValidatePipeline(bundleConfig(tc.config))
		if !strings.Contains(strings.Join(ve.Errors, " | "), tc.want) {
			t.Errorf("%s: rejection does not name the problem: %v", name, ve.Errors)
		}
	}
}