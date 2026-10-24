package engine

import (
	"testing"

	"github.com/Tnsor-Labs/brokoli/pkg/common"
)

// dedup built its key with %v joined on a NUL, so rows that were not
// duplicates were dropped: the number 1 and the text "1" printed the same,
// a missing value printed as "<nil>" like the text "<nil>", and a NUL
// inside a value could line two different rows up. Rows are now duplicates
// only when their values are equal: numbers by value, text as text.
func TestDedupDropsOnlyRowsWithEqualValues(t *testing.T) {
	ds := &common.DataSet{Columns: []string{"id"}, Rows: []common.DataRow{
		{"id": int64(1)},
		{"id": "1"},
		{"id": nil},
		{"id": "<nil>"},
		{"id": float64(1)},
		{"id": int64(9007199254740993)},
		{"id": int64(9007199254740992)},
	}}
	if err := ApplyTransforms([]TransformRule{{Type: "deduplicate", Columns: []string{"id"}}}, ds); err != nil {
		t.Fatal(err)
	}
	var got []interface{}
	for _, r := range ds.Rows {
		got = append(got, r["id"])
	}
	want := []interface{}{int64(1), "1", nil, "<nil>", int64(9007199254740993), int64(9007199254740992)}
	if len(got) != len(want) {
		t.Fatalf("kept %#v, want %#v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("kept %#v, want %#v", got, want)
		}
	}
}

func TestDedupKeysDoNotRunTogetherAcrossColumns(t *testing.T) {
	ds := &common.DataSet{Columns: []string{"a", "b"}, Rows: []common.DataRow{
		{"a": "x\x00", "b": "y"},
		{"a": "x", "b": "\x00y"},
		{"a": "x", "b": "\x00y"},
		// Without each part's length these two run together as well,
		// since a value can spell another value's kind prefix.
		{"a": "xs:", "b": "y"},
		{"a": "x", "b": "s:y"},
		// And these, when the parts are only separated.
		{"a": "a:s:b", "b": "c"},
		{"a": "a", "b": "b:s:c"},
	}}
	if err := ApplyTransforms([]TransformRule{{Type: "deduplicate", Columns: []string{"a", "b"}}}, ds); err != nil {
		t.Fatal(err)
	}
	if len(ds.Rows) != 6 {
		t.Fatalf("kept %d rows, want 6 (only the third is a duplicate): %#v", len(ds.Rows), ds.Rows)
	}
}

// A join key was %T:%#v, so an int64 id from a database never matched the
// same id decoded as a float64 elsewhere. Numbers now match by value; a
// number still never matches text (TestJoinTypedKeysDoNotCollide).
func TestJoinMatchesNumbersByValue(t *testing.T) {
	left := &common.DataSet{Columns: []string{"id", "l"}, Rows: []common.DataRow{{"id": int64(5), "l": "left"}}}
	right := &common.DataSet{Columns: []string{"id", "r"}, Rows: []common.DataRow{{"id": float64(5), "r": "right"}}}
	result, err := JoinDatasets(left, right, "id", "id", JoinInner)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Rows) != 1 || result.Rows[0]["r"] != "right" {
		t.Fatalf("int64 5 and float64 5 did not join: %#v", result.Rows)
	}
}

func TestCountDistinctCountsNumbersByValue(t *testing.T) {
	ds := &common.DataSet{Columns: []string{"g", "v"}, Rows: []common.DataRow{
		{"g": "a", "v": int64(1)},
		{"g": "a", "v": float64(1)},
		{"g": "a", "v": 1.5},
	}}
	if err := ApplyTransforms([]TransformRule{{Type: "aggregate", GroupBy: []string{"g"}, AggFields: []AggField{{Column: "v", Function: "count_distinct", Alias: "n"}}}}, ds); err != nil {
		t.Fatal(err)
	}
	if ds.Rows[0]["n"] != 2 {
		t.Fatalf("count_distinct = %v, want 2 (1 and 1.0 are one value)", ds.Rows[0]["n"])
	}
}
