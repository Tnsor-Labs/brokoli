package engine

import (
	"strings"
	"testing"

	"github.com/Tnsor-Labs/brokoli/models"
)

// validate.go keeps two parallel per-node validators: validateNodeConfig,
// behind ValidatePipeline and therefore every save, and
// validateNodeConfigDetailed, behind ValidateNodes and the per-node
// detail view. A check added to one is not in the other. v0.13.0 shipped
// its transform-rule check in the detail validator only, so saves were
// never refused while its unit tests, which called ValidateNodes, passed.
//
// This pins that a config which cannot run is refused by BOTH. Add a row
// whenever a save-time check is added, so the next one cannot land in
// only one of them either.
func TestBothValidatorsRefuseTheSameUnrunnableConfigs(t *testing.T) {
	source := models.Node{ID: "src", Type: models.NodeTypeSourceAPI, Name: "Src",
		Config: map[string]interface{}{"url": "https://example.com/rows"}}

	cases := []struct {
		name string
		node models.Node
		want string
	}{
		{"transform: sort without columns",
			models.Node{ID: "t", Type: models.NodeTypeTransform, Name: "T",
				Config: map[string]interface{}{"rules": []interface{}{map[string]interface{}{"type": "sort"}}}},
			"sort requires columns list"},
		{"transform: misspelled rule type",
			models.Node{ID: "t", Type: models.NodeTypeTransform, Name: "T",
				Config: map[string]interface{}{"rules": []interface{}{map[string]interface{}{"type": "sorrt", "columns": []interface{}{"id"}}}}},
			"unsupported transform type"},
		{"contract_gate: unique on a record rule",
			models.Node{ID: "g", Type: models.NodeTypeContractGate, Name: "G",
				Config: map[string]interface{}{"contract": map[string]interface{}{
					"ir_version": "1.0",
					"contract":   map[string]interface{}{"id": "c", "version": "1"},
					"input":      map[string]interface{}{"kind": "record-stream"},
					"rules": []interface{}{map[string]interface{}{
						"id": "u", "kind": "record", "path": "$.id",
						"predicate": map[string]interface{}{"op": "unique"},
						"on_breach": map[string]interface{}{"action": "reject"}}},
				}}},
			"unique must be a stream rule"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			pipe := &models.Pipeline{ID: "p", Name: "p",
				Nodes: []models.Node{source, tc.node},
				Edges: []models.Edge{{From: "src", To: tc.node.ID}}}

			save := ValidatePipeline(pipe)
			if save == nil || !save.HasErrors() || !strings.Contains(save.Error(), tc.want) {
				t.Errorf("ValidatePipeline (the save path) did not refuse it with %q: %v", tc.want, save)
			}

			var detail []string
			for _, r := range ValidateNodes(pipe.Nodes) {
				detail = append(detail, r.Errors...)
			}
			if !strings.Contains(strings.Join(detail, "; "), tc.want) {
				t.Errorf("ValidateNodes (the detail path) did not report %q: %v", tc.want, detail)
			}
		})
	}
}
