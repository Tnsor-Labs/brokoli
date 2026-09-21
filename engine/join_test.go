package engine

import (
	"reflect"
	"strings"
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

func TestJoin_NullKeysDoNotMatch(t *testing.T) {
	left := &common.DataSet{Columns: []string{"id", "left"}, Rows: []common.DataRow{{"id": nil, "left": "l"}}}
	right := &common.DataSet{Columns: []string{"id", "right"}, Rows: []common.DataRow{{"id": nil, "right": "r"}}}
	result, err := JoinDatasets(left, right, "id", "id", JoinInner)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Rows) != 0 {
		t.Fatalf("null join keys must not match, got %#v", result.Rows)
	}
}

func TestJoinTypedKeysDoNotCollide(t *testing.T) {
	left := &common.DataSet{Columns: []string{"id"}, Rows: []common.DataRow{{"id": int64(1)}}}
	right := &common.DataSet{Columns: []string{"id"}, Rows: []common.DataRow{{"id": "1"}}}
	result, err := JoinDatasets(left, right, "id", "id", JoinInner)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Rows) != 0 {
		t.Fatalf("typed join keys must not collide, got %#v", result.Rows)
	}
}

func TestJoinPrefixAvoidsGeneratedNameCollision(t *testing.T) {
	left := &common.DataSet{Columns: []string{"id", "value", "right_value"}, Rows: []common.DataRow{{"id": "1", "value": "left", "right_value": "existing"}}}
	right := &common.DataSet{Columns: []string{"id", "value"}, Rows: []common.DataRow{{"id": "1", "value": "right"}}}
	result, err := JoinDatasetsWithOptions(left, right, "id", "id", JoinInner, JoinOptions{CollisionPolicy: JoinCollisionPrefix})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(result.Columns, []string{"id", "value", "right_value", "right_right_value"}) {
		t.Fatalf("unexpected collision-safe columns: %v", result.Columns)
	}
	if result.Rows[0]["right_right_value"] != "right" {
		t.Fatalf("right value was not retained under generated alias: %#v", result.Rows[0])
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

func TestJoin_CollisionPolicyErrorNamesColumns(t *testing.T) {
	left := &common.DataSet{
		Columns: []string{"id", "name"},
		Rows:    []common.DataRow{{"id": "1", "name": "left"}},
	}
	right := &common.DataSet{
		Columns: []string{"id", "name"},
		Rows:    []common.DataRow{{"id": "1", "name": "right"}},
	}

	_, err := JoinDatasetsWithOptions(left, right, "id", "id", JoinInner, JoinOptions{
		CollisionPolicy: JoinCollisionError,
	})
	if err == nil {
		t.Fatal("expected colliding columns to fail")
	}
	if !strings.Contains(err.Error(), "name") {
		t.Fatalf("error %q does not name the colliding column", err)
	}
}

func TestJoin_CollisionPolicyAliasUsesStableRightNames(t *testing.T) {
	left := &common.DataSet{
		Columns: []string{"id", "name"},
		Rows:    []common.DataRow{{"id": "1", "name": "left"}},
	}
	right := &common.DataSet{
		Columns: []string{"id", "name"},
		Rows:    []common.DataRow{{"id": "1", "name": "right"}},
	}

	result, err := JoinDatasetsWithOptions(left, right, "id", "id", JoinInner, JoinOptions{
		CollisionPolicy: JoinCollisionAlias,
		RightAlias:      "customer",
	})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := result.Columns, []string{"id", "name", "customer_name"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("columns = %v, want %v", got, want)
	}
	if got, want := result.Rows[0]["customer_name"], "right"; got != want {
		t.Fatalf("customer_name = %v, want %v", got, want)
	}
}

func TestJoin_PrefixPolicyAvoidsRecursiveOutputCollisions(t *testing.T) {
	left := &common.DataSet{
		Columns: []string{"id", "right_id", "user_key"},
		Rows:    []common.DataRow{{"id": "1", "right_id": "existing", "user_key": "u1"}},
	}
	right := &common.DataSet{
		Columns: []string{"user_key", "id", "value"},
		Rows:    []common.DataRow{{"user_key": "u1", "id": "1", "value": "right"}},
	}

	result, err := JoinDatasetsWithOptions(left, right, "user_key", "user_key", JoinInner, JoinOptions{
		CollisionPolicy: JoinCollisionPrefix,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := result.Columns, []string{"id", "right_id", "user_key", "right_right_id", "right_value"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("columns = %v, want %v", got, want)
	}
}

func TestJoin_AliasRequiresAnAlias(t *testing.T) {
	_, err := JoinDatasetsWithOptions(ordersDS(), customersDS(), "customer_id", "customer_id", JoinInner, JoinOptions{
		CollisionPolicy: JoinCollisionAlias,
	})
	if err == nil || !strings.Contains(err.Error(), "right_alias") {
		t.Fatalf("expected right_alias error, got %v", err)
	}
}

func TestJoin_KeysMustExistInInputSchemas(t *testing.T) {
	_, err := JoinDatasetsWithOptions(ordersDS(), customersDS(), "missing", "customer_id", JoinInner, JoinOptions{
		CollisionPolicy: JoinCollisionPrefix,
	})
	if err == nil || !strings.Contains(err.Error(), "missing") {
		t.Fatalf("expected missing left key error, got %v", err)
	}

	_, err = JoinDatasetsWithOptions(ordersDS(), customersDS(), "customer_id", "missing", JoinInner, JoinOptions{
		CollisionPolicy: JoinCollisionPrefix,
	})
	if err == nil || !strings.Contains(err.Error(), "missing") {
		t.Fatalf("expected missing right key error, got %v", err)
	}
}
