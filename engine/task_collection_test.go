package engine

// Collection-output rules that a well-behaved reference harness cannot
// produce, so they are checked directly rather than through a pipeline:
// a harness's declarations are claims, not facts (ADR-033 section 3
// admits native payloads speaking the protocol directly), and a test
// that could never fail end to end would read as coverage that does not
// exist.

import (
	"context"
	"strings"
	"testing"

	"github.com/Tnsor-Labs/brokoli/pkg/taskinterface"
)

func scalarItem(key interface{}, value interface{}) taskOutputPort {
	return taskOutputPort{Kind: "scalar", Value: value, ItemKey: key}
}

// ADR-032 section 6: "Duplicate keys are invalid even when duplicate
// values are allowed." A duplicate means the items are not separately
// addressable, which is the one property the kind promises.
func TestReadTaskCollectionOutput_DuplicateItemKeysAreRefused(t *testing.T) {
	items := []taskOutputPort{scalarItem("a", 1), scalarItem("b", 2), scalarItem("a", 3)}
	_, err := readTaskCollectionOutput(context.Background(), nil, "run-1", t.TempDir(), items, taskinterface.PortValue{})
	if err == nil {
		t.Fatal("duplicate item keys were accepted")
	}
	if !strings.Contains(err.Error(), "duplicate keys") {
		t.Errorf("err = %v, want the duplicate-key rule named", err)
	}
	// Both offending positions named, or an author of a 500-item
	// collection has to go looking.
	if !strings.Contains(err.Error(), "0") || !strings.Contains(err.Error(), "2") {
		t.Errorf("err = %v, want both colliding item positions named", err)
	}
}

// Duplicate VALUES with distinct keys are explicitly fine -- the rule is
// about addressability, not about the payload.
func TestReadTaskCollectionOutput_DuplicateValuesWithDistinctKeysAreFine(t *testing.T) {
	items := []taskOutputPort{scalarItem("a", 7), scalarItem("b", 7)}
	ds, err := readTaskCollectionOutput(context.Background(), nil, "run-1", t.TempDir(), items, taskinterface.PortValue{})
	if err != nil {
		t.Fatalf("duplicate values with distinct keys were refused: %v", err)
	}
	if len(ds.Rows) != 2 {
		t.Fatalf("rows = %v, want 2", ds.Rows)
	}
}

func TestReadTaskCollectionOutput_MissingItemKeyIsRefused(t *testing.T) {
	items := []taskOutputPort{{Kind: "scalar", Value: 1}}
	_, err := readTaskCollectionOutput(context.Background(), nil, "run-1", t.TempDir(), items, taskinterface.PortValue{})
	if err == nil || !strings.Contains(err.Error(), "item_key") {
		t.Fatalf("err = %v, want a keyless item refused by name", err)
	}
}

// The item's own declared contract is enforced, so a collection cannot
// smuggle past the validation a standalone scalar output would face.
func TestReadTaskCollectionOutput_ItemViolatingItsDeclaredTypeIsRefused(t *testing.T) {
	intType := taskinterface.Type{Kind: taskinterface.KindInt64}
	port := taskinterface.PortValue{
		Kind:  taskinterface.ValueCollection,
		Items: &taskinterface.PortValue{Kind: taskinterface.ValueScalar, ScalarType: &intType},
	}
	items := []taskOutputPort{scalarItem("a", float64(1)), scalarItem("b", "not-an-int")}
	_, err := readTaskCollectionOutput(context.Background(), nil, "run-1", t.TempDir(), items, port)
	if err == nil {
		t.Fatal("an item violating its declared type was accepted")
	}
	if !strings.Contains(err.Error(), "items[1]") {
		t.Errorf("err = %v, want the offending item index named", err)
	}
}

// The contract permits nested collections; this server's one-row-per-item
// mapping has no shape for them. That is a limit of the mapping, and it
// says so rather than silently flattening.
func TestReadTaskCollectionOutput_NestedCollectionIsRefusedAsAMappingLimit(t *testing.T) {
	items := []taskOutputPort{{Kind: "collection", ItemKey: "a", Items: []taskOutputPort{scalarItem("x", 1)}}}
	_, err := readTaskCollectionOutput(context.Background(), nil, "run-1", t.TempDir(), items, taskinterface.PortValue{})
	if err == nil || !strings.Contains(err.Error(), "nested collections") {
		t.Fatalf("err = %v, want nested collections refused by name", err)
	}
}

func TestReadTaskCollectionOutput_OverTheItemCapIsRefused(t *testing.T) {
	items := make([]taskOutputPort, maxCollectionItems+1)
	for i := range items {
		items[i] = scalarItem(i, i)
	}
	_, err := readTaskCollectionOutput(context.Background(), nil, "run-1", t.TempDir(), items, taskinterface.PortValue{})
	if err == nil || !strings.Contains(err.Error(), "cap") {
		t.Fatalf("err = %v, want the item cap enforced", err)
	}
}
