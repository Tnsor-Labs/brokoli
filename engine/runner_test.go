package engine

import (
	"testing"

	"github.com/Tnsor-Labs/brokoli/models"
)

func TestTopoSort_Linear(t *testing.T) {
	nodes := []models.Node{
		{ID: "a"}, {ID: "b"}, {ID: "c"},
	}
	edges := []models.Edge{
		{From: "a", To: "b"}, {From: "b", To: "c"},
	}
	sorted, err := topoSort(nodes, edges)
	if err != nil {
		t.Fatal(err)
	}
	if len(sorted) != 3 {
		t.Fatalf("expected 3, got %d", len(sorted))
	}
	if sorted[0].ID != "a" || sorted[1].ID != "b" || sorted[2].ID != "c" {
		t.Errorf("wrong order: %v", sorted)
	}
}

func TestTopoSort_Parallel(t *testing.T) {
	// a -> c, b -> c  (a and b can run in parallel)
	nodes := []models.Node{
		{ID: "a"}, {ID: "b"}, {ID: "c"},
	}
	edges := []models.Edge{
		{From: "a", To: "c"}, {From: "b", To: "c"},
	}
	sorted, err := topoSort(nodes, edges)
	if err != nil {
		t.Fatal(err)
	}
	if len(sorted) != 3 {
		t.Fatalf("expected 3, got %d", len(sorted))
	}
	// a and b should come before c
	if sorted[2].ID != "c" {
		t.Errorf("c should be last, got %s", sorted[2].ID)
	}
}

func TestTopoSort_Cycle(t *testing.T) {
	nodes := []models.Node{
		{ID: "a"}, {ID: "b"},
	}
	edges := []models.Edge{
		{From: "a", To: "b"}, {From: "b", To: "a"},
	}
	_, err := topoSort(nodes, edges)
	if err == nil {
		t.Error("expected cycle error")
	}
}

func TestTopoSort_Diamond(t *testing.T) {
	// a -> b, a -> c, b -> d, c -> d
	nodes := []models.Node{
		{ID: "a"}, {ID: "b"}, {ID: "c"}, {ID: "d"},
	}
	edges := []models.Edge{
		{From: "a", To: "b"}, {From: "a", To: "c"},
		{From: "b", To: "d"}, {From: "c", To: "d"},
	}
	sorted, err := topoSort(nodes, edges)
	if err != nil {
		t.Fatal(err)
	}
	if sorted[0].ID != "a" {
		t.Errorf("a should be first")
	}
	if sorted[3].ID != "d" {
		t.Errorf("d should be last")
	}
}
