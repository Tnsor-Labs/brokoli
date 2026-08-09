package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestCapabilitiesHandler exercises GET /api/capabilities directly (the
// handler has no store/engine dependency, so it's invoked without the
// full router). SDK clients hit this endpoint to discover which
// ir_version(s) and node/connector capability tags a given Brokoli
// deployment understands before deploying a pipeline.
func TestCapabilitiesHandler(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/api/capabilities", nil)
	rec := httptest.NewRecorder()

	CapabilitiesHandler(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var body map[string]interface{}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	if body["ir_version"] != "2.0" {
		t.Errorf("expected ir_version 2.0, got %v", body["ir_version"])
	}

	supported, ok := body["supported_ir_versions"].([]interface{})
	if !ok || len(supported) == 0 {
		t.Fatalf("expected non-empty supported_ir_versions, got %v", body["supported_ir_versions"])
	}
	found := map[string]bool{"2.0": false, "2.1": false}
	for _, v := range supported {
		if version, ok := v.(string); ok {
			if _, expected := found[version]; expected {
				found[version] = true
			}
		}
	}
	if !found["2.0"] || !found["2.1"] {
		t.Errorf("expected supported_ir_versions to include 2.0 and 2.1, got %v", supported)
	}

	if body["plugin_protocol_version"] == nil {
		t.Error("expected plugin_protocol_version to be present")
	}
	if body["supported_plugin_protocol_versions"] == nil {
		t.Error("expected supported_plugin_protocol_versions to be present")
	}

	nodeCaps, ok := body["node_capabilities"].([]interface{})
	if !ok || len(nodeCaps) == 0 {
		t.Fatalf("expected non-empty node_capabilities, got %v", body["node_capabilities"])
	}
	wantCaps := map[string]bool{"source": false, "sink": false, "compute": false, "dataset-output": false}
	for _, c := range nodeCaps {
		if s, ok := c.(string); ok {
			if _, known := wantCaps[s]; known {
				wantCaps[s] = true
			}
		}
	}
	for capName, seen := range wantCaps {
		if !seen {
			t.Errorf("expected node_capabilities to include %q, got %v", capName, nodeCaps)
		}
	}

	if body["node_type_capabilities"] == nil {
		t.Error("expected node_type_capabilities to be present")
	}
}
