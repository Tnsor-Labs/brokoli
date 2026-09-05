package api

// Validates every nodeTypeInterfaces entry against docs/schema/task-interface-v1.json
// (ADR-032). A typo in one of the JSON literals in capabilities.go fails
// here, not silently at deploy time -- these documents are exposed via
// GET /api/capabilities and are meant to be genuinely valid task
// interfaces, not merely well-formed JSON.

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Tnsor-Labs/brokoli/models"
	"github.com/santhosh-tekuri/jsonschema/v6"
)

func compileTaskInterfaceSchemaForAPI(t *testing.T) *jsonschema.Schema {
	t.Helper()
	c := jsonschema.NewCompiler()
	sch, err := c.Compile("../docs/schema/task-interface-v1.json")
	if err != nil {
		t.Fatalf("compile task-interface-v1.json: %v", err)
	}
	return sch
}

func TestNodeTypeInterfacesValidateAgainstSchema(t *testing.T) {
	sch := compileTaskInterfaceSchemaForAPI(t)
	if len(nodeTypeInterfaces) == 0 {
		t.Fatal("nodeTypeInterfaces is empty -- this contract check is running against nothing")
	}
	for nodeType, iface := range nodeTypeInterfaces {
		t.Run(string(nodeType), func(t *testing.T) {
			data, err := json.Marshal(iface)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			inst, err := jsonschema.UnmarshalJSON(bytes.NewReader(data))
			if err != nil {
				t.Fatalf("reparse: %v", err)
			}
			if err := sch.Validate(inst); err != nil {
				t.Errorf("nodeTypeInterfaces[%s] is not a valid task interface:\n%v", nodeType, err)
			}
		})
	}
}

// TestNodeTypeInterfacesExcludedTypesAreDeliberate documents, in a form a
// test can pin, that the exclusions capabilities.go's doc comment names
// are still actually excluded -- catching an accidental removal of the
// comment without removing the (still-wrong) entry, or vice versa.
func TestNodeTypeInterfacesExcludedTypesAreDeliberate(t *testing.T) {
	excluded := []models.NodeType{
		models.NodeTypeSourceAPI, models.NodeTypeJoin, models.NodeTypeDBT,
		models.NodeTypeWait, models.NodeTypeSQLGenerate, models.NodeTypeCode,
	}
	for _, nt := range excluded {
		if _, ok := nodeTypeInterfaces[nt]; ok {
			t.Errorf("%s has a nodeTypeInterfaces entry now -- update the exclusion doc comment in capabilities.go (and this test) in the same change", nt)
		}
	}
}

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
