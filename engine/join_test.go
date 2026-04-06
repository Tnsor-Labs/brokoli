package engine

import (
	"testing"

	"github.com/Tnsor-Labs/brokoli/pkg/common"
)

func ordersDS() *common.DataSet {
	return &common.DataSet{
		Columns: []string{"order_id", "customer_id", "amount"},
		Rows: []common.DataRow{
			{"order_id": "1", "customer_id": "c1", "amount": "100"},
			{"order_id": "2", "customer_id": "c2", "amount": "200"},
			{"order_id": "3", "customer_id": "c1", "amount": "150"},
			{"order_id": "4", "customer_id": "c3", "amount": "50"},
		},
	}
}

func customersDS() *common.DataSet {
	return &common.DataSet{
		Columns: []string{"customer_id", "name"},
		Rows: []common.DataRow{
			{"customer_id": "c1", "name": "Alice"},
			{"customer_id": "c2", "name": "Bob"},
			{"customer_id": "c4", "name": "Diana"},
		},
	}
}

func TestJoin_Inner(t *testing.T) {
	result, err := JoinDatasets(ordersDS(), customersDS(), "customer_id", "customer_id", JoinInner)
	if err != nil {
		t.Fatal(err)
	}
	// c1 matches 2 orders, c2 matches 1, c3 has no match -> 3 rows
	if len(result.Rows) != 3 {
		t.Errorf("expected 3 rows, got %d", len(result.Rows))
	}
	// Should have merged columns
	if len(result.Columns) < 3 {
		t.Errorf("expected at least 3 columns, got %d: %v", len(result.Columns), result.Columns)
	}
}

func TestJoin_Left(t *testing.T) {
	result, err := JoinDatasets(ordersDS(), customersDS(), "customer_id", "customer_id", JoinLeft)
	if err != nil {
		t.Fatal(err)
	}
	// All 4 left rows preserved, c3 gets nulls
	if len(result.Rows) != 4 {
		t.Errorf("expected 4 rows, got %d", len(result.Rows))
	}
}

func TestJoin_Right(t *testing.T) {
	result, err := JoinDatasets(ordersDS(), customersDS(), "customer_id", "customer_id", JoinRight)
	if err != nil {
		t.Fatal(err)
	}
	// 3 matched + c4 unmatched = 4
	if len(result.Rows) != 4 {
		t.Errorf("expected 4 rows, got %d", len(result.Rows))
	}
}

func TestJoin_Full(t *testing.T) {
	result, err := JoinDatasets(ordersDS(), customersDS(), "customer_id", "customer_id", JoinFull)
	if err != nil {
		t.Fatal(err)
	}
	// 3 matched + c3 unmatched left + c4 unmatched right = 5
	if len(result.Rows) != 5 {
		t.Errorf("expected 5 rows, got %d", len(result.Rows))
	}
}

func TestJoin_DifferentKeys(t *testing.T) {
	left := &common.DataSet{
		Columns: []string{"id", "value"},
		Rows:    []common.DataRow{{"id": "1", "value": "a"}, {"id": "2", "value": "b"}},
	}
	right := &common.DataSet{
		Columns: []string{"ref_id", "label"},
		Rows:    []common.DataRow{{"ref_id": "1", "label": "x"}, {"ref_id": "3", "label": "y"}},
	}
	result, err := JoinDatasets(left, right, "id", "ref_id", JoinInner)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Rows) != 1 {
		t.Errorf("expected 1 row, got %d", len(result.Rows))
	}
}

func TestJoin_NilInputs(t *testing.T) {
	_, err := JoinDatasets(nil, customersDS(), "id", "id", JoinInner)
	if err == nil {
		t.Error("expected error for nil left")
	}
}

func TestJoin_EmptyKey(t *testing.T) {
	_, err := JoinDatasets(ordersDS(), customersDS(), "", "id", JoinInner)
	if err == nil {
		t.Error("expected error for empty key")
	}
}
