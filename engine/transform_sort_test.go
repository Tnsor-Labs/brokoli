package engine

import (
	"fmt"
	"math/rand"
	"testing"

	"github.com/Tnsor-Labs/brokoli/pkg/common"
)

// The sort transform orders numbers as numbers (#642). It used to compare
// every value as text, so ascending 2, 10 came out 10, 2.

func sortValues(t *testing.T, values []interface{}, ascending bool) []interface{} {
	t.Helper()
	ds := &common.DataSet{Columns: []string{"v"}}
	for _, v := range values {
		ds.Rows = append(ds.Rows, common.DataRow{"v": v})
	}
	if err := ApplyTransforms([]TransformRule{{Type: "sort", Columns: []string{"v"}, Ascending: ascending}}, ds); err != nil {
		t.Fatalf("sort: %v", err)
	}
	out := make([]interface{}, len(ds.Rows))
	for i, row := range ds.Rows {
		out[i] = row["v"]
	}
	return out
}

func TestSortOrdersEachKindOfValueCorrectly(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   []interface{}
		asc  []interface{}
	}{
		{"integers", []interface{}{2, 10, 1}, []interface{}{1, 2, 10}},
		// CSV sources often hold numbers as text.
		{"numbers held as text", []interface{}{"2", "10", "1"}, []interface{}{"1", "2", "10"}},
		{"decimals", []interface{}{9.5, 10.5, -1.0}, []interface{}{-1.0, 9.5, 10.5}},
		{"negatives", []interface{}{-1, -10}, []interface{}{-10, -1}},
		// Adjacent integers a float64 cannot tell apart.
		{"large integers", []interface{}{int64(9007199254740993), int64(9007199254740992)},
			[]interface{}{int64(9007199254740992), int64(9007199254740993)}},
		// Numbers, then text, then empty values.
		{"mixed", []interface{}{"b", 10, "a", 2, nil}, []interface{}{2, 10, "a", "b", nil}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := sortValues(t, tc.in, true); fmt.Sprint(got) != fmt.Sprint(tc.asc) {
				t.Errorf("ascending: got %v, want %v", got, tc.asc)
			}
			desc := make([]interface{}, len(tc.asc))
			for i := range tc.asc {
				desc[i] = tc.asc[len(tc.asc)-1-i]
			}
			if got := sortValues(t, tc.in, false); fmt.Sprint(got) != fmt.Sprint(desc) {
				t.Errorf("descending must be the exact reverse: got %v, want %v", got, desc)
			}
		})
	}
}

func TestSortIsStable(t *testing.T) {
	ds := &common.DataSet{Columns: []string{"v", "id"}, Rows: []common.DataRow{
		{"v": 1, "id": "a"}, {"v": 1, "id": "b"}, {"v": 0, "id": "c"}, {"v": 1, "id": "d"},
	}}
	if err := ApplyTransforms([]TransformRule{{Type: "sort", Columns: []string{"v"}, Ascending: true}}, ds); err != nil {
		t.Fatal(err)
	}
	var ids string
	for _, r := range ds.Rows {
		ids += r["id"].(string)
	}
	if ids != "cabd" {
		t.Errorf("order = %s, want cabd: rows with equal keys must keep their order", ids)
	}
}

func TestSortByTwoColumnsUsesTheSecondOnlyForTies(t *testing.T) {
	ds := &common.DataSet{Columns: []string{"g", "v"}, Rows: []common.DataRow{
		{"g": "x", "v": 10}, {"g": "y", "v": 1}, {"g": "x", "v": 2},
	}}
	if err := ApplyTransforms([]TransformRule{{Type: "sort", Columns: []string{"g", "v"}, Ascending: true}}, ds); err != nil {
		t.Fatal(err)
	}
	got := fmt.Sprint(ds.Rows[0]["v"], ds.Rows[1]["v"], ds.Rows[2]["v"])
	if got != "2 10 1" {
		t.Errorf("order = %s, want 2 10 1: x before y, then 2 before 10 numerically", got)
	}
}

// Every pair in the output respects the order. A comparison that is not
// a consistent order -- the pairwise numeric-or-text rule the filter uses
// -- produces cycles, and this catches the arbitrary output that follows.
func TestSortProducesAConsistentTotalOrder(t *testing.T) {
	rng := rand.New(rand.NewSource(642))
	pool := []interface{}{nil, "", "a", "b", "10", "9", "5a", "-1", 3, -7, 2.5, int64(9007199254740993), "Z", "true"}
	values := make([]interface{}, 200)
	for i := range values {
		values[i] = pool[rng.Intn(len(pool))]
	}
	got := sortValues(t, values, true)
	for i := 0; i < len(got); i++ {
		for j := i + 1; j < len(got); j++ {
			if compareSortKeys(sortKeyOf(got[i]), sortKeyOf(got[j])) > 0 {
				t.Fatalf("position %d (%v) orders after position %d (%v)", i, got[i], j, got[j])
			}
		}
	}
}
