package api

// The node_type_interfaces schema-validity and exclusion-list tests live
// in models/node_interfaces_test.go (same package as models.NodeTypeInterfaces
// itself, since ADR-032 step 4 moved that table from api to models so
// engine/validate.go could consult it too). This file keeps only the
// HTTP-surface test: that GET /api/capabilities actually exposes the table.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestCapabilitiesHandlerExposesNodeTypeInterfaces(t *testing.T) {
	SetCodeRuntime("")
	t.Cleanup(func() { SetCodeRuntime("") })
	req := httptest.NewRequest(http.MethodGet, "/api/capabilities", nil)
	rec := httptest.NewRecorder()
	CapabilitiesHandler(rec, req)

	var body map[string]interface{}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	raw, ok := body["node_type_interfaces"]
	if !ok {
		t.Fatal("expected node_type_interfaces in the capabilities response")
	}
	table, ok := raw.(map[string]interface{})
	if !ok || len(table) == 0 {
		t.Fatalf("expected a non-empty node_type_interfaces object, got %#v", raw)
	}
	if _, ok := table["source_file"]; !ok {
		t.Error(`expected "source_file" in node_type_interfaces`)
	}
}
