package engine

import (
	"testing"

	"github.com/Tnsor-Labs/brokoli/models"
)

func TestArrangeStacked(t *testing.T) {
	stacked := &models.Pipeline{
		Nodes: []models.Node{{ID: "a"}, {ID: "b"}, {ID: "c"}, {ID: "d"}},
		Edges: []models.Edge{{From: "a", To: "b"}, {From: "b", To: "c"}, {From: "a", To: "d"}},
	}
	if !ArrangeStacked(stacked) {
		t.Fatal("stacked nodes were not laid out")
	}
	at := map[string]models.Position{}
	for _, n := range stacked.Nodes {
		if prev, seen := positionTaken(at, n.Position); seen {
			t.Fatalf("%s and %s share %+v", n.ID, prev, n.Position)
		}
		at[n.ID] = n.Position
	}
	// Left to right along the edges; siblings one under the other.
	if !(at["a"].X < at["b"].X && at["b"].X < at["c"].X && at["b"].X == at["d"].X && at["b"].Y != at["d"].Y) {
		t.Fatalf("layout = %+v", at)
	}

	// All at one place other than the origin counts too.
	same := &models.Pipeline{Nodes: []models.Node{{ID: "a", Position: models.Position{X: 5, Y: 5}}, {ID: "b", Position: models.Position{X: 5, Y: 5}}}}
	if !ArrangeStacked(same) || same.Nodes[0].Position == same.Nodes[1].Position {
		t.Fatalf("same place = %+v", same.Nodes)
	}

	// A layout is kept, a single node is left alone.
	laid := &models.Pipeline{Nodes: []models.Node{{ID: "a"}, {ID: "b", Position: models.Position{X: 300}}, {ID: "c"}}}
	if ArrangeStacked(laid) || laid.Nodes[1].Position.X != 300 || laid.Nodes[2].Position != (models.Position{}) {
		t.Fatalf("a layout was changed: %+v", laid.Nodes)
	}
	if ArrangeStacked(&models.Pipeline{Nodes: []models.Node{{ID: "a"}}}) || ArrangeStacked(nil) {
		t.Fatal("one node or none was laid out")
	}
}

func positionTaken(at map[string]models.Position, p models.Position) (string, bool) {
	for id, q := range at {
		if q == p {
			return id, true
		}
	}
	return "", false
}
