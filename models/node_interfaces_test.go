package models_test

// Validates every models.NodeTypeInterfaces entry against
// docs/schema/task-interface-v1.json (ADR-032). A typo in one of the
// JSON literals in models/node_interfaces.go fails here, not silently
// at deploy time -- these documents are exposed via GET
// /api/capabilities and consulted by engine/validate.go's assignability
// check, and are meant to be genuinely valid task interfaces, not
// merely well-formed JSON.
//
// compileTaskInterfaceSchema (task_interface_schema_test.go, same
// package) is reused here.

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/Tnsor-Labs/brokoli/models"
	"github.com/santhosh-tekuri/jsonschema/v6"
)

func TestNodeTypeInterfacesValidateAgainstSchema(t *testing.T) {
	sch := compileTaskInterfaceSchema(t)
	if len(models.NodeTypeInterfaces) == 0 {
		t.Fatal("models.NodeTypeInterfaces is empty -- this contract check is running against nothing")
	}
	for nodeType, iface := range models.NodeTypeInterfaces {
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
				t.Errorf("NodeTypeInterfaces[%s] is not a valid task interface:\n%v", nodeType, err)
			}
		})
	}
}

// TestNodeTypeInterfacesExcludedTypesAreDeliberate documents, in a form a
// test can pin, that the exclusions models/node_interfaces.go's doc
// comment names are still actually excluded -- catching an accidental
// removal of the comment without removing the (still-wrong) entry, or
// vice versa.
func TestNodeTypeInterfacesExcludedTypesAreDeliberate(t *testing.T) {
	excluded := []models.NodeType{
		models.NodeTypeSourceAPI, models.NodeTypeJoin, models.NodeTypeDBT,
		models.NodeTypeWait, models.NodeTypeSQLGenerate, models.NodeTypeCode,
	}
	for _, nt := range excluded {
		if _, ok := models.NodeTypeInterfaces[nt]; ok {
			t.Errorf("%s has a NodeTypeInterfaces entry now -- update the exclusion doc comment in models/node_interfaces.go (and this test) in the same change", nt)
		}
	}
}
