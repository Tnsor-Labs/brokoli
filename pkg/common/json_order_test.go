package common

import (
	"reflect"
	"strings"
	"testing"
)

// Columns used to come from ranging over a map, so they changed order from
// run to run. A JSON source now keeps the order its keys have in the file.

const ordered = `[{"zeta": 1, "alpha": 2, "mid": {"inner": 3, "deep": {"x": 1}}, "beta": 4},
                  {"alpha": 5, "zeta": 6, "late": 7, "beta": 8}]`

func TestColumnsFollowTheDocumentsKeyOrder(t *testing.T) {
	data, err := ParseJSONData([]byte(ordered))
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"zeta", "alpha", "mid", "beta", "late"}
	for i := 0; i < 50; i++ {
		ds := ConvertToDataSetOrdered(data, KeyOrder([]byte(ordered)))
		if !reflect.DeepEqual(ds.Columns, want) {
			t.Fatalf("run %d: columns %v, want %v", i, ds.Columns, want)
		}
	}
}

func TestWithoutAnOrderColumnsAreAlphabeticalAndStable(t *testing.T) {
	data, _ := ParseJSONData([]byte(ordered))
	want := []string{"alpha", "beta", "late", "mid", "zeta"}
	for i := 0; i < 50; i++ {
		if got := ConvertToDataSet(data).Columns; !reflect.DeepEqual(got, want) {
			t.Fatalf("run %d: %v, want %v", i, got, want)
		}
	}
}

// Keys an order does not know (records from a resumed run, say) come after
// the known ones, alphabetically.
func TestUnknownKeysComeAfterKnownOnes(t *testing.T) {
	data := []map[string]interface{}{{"b": 1, "a": 2, "d": 3, "c": 4}}
	got := ConvertToDataSetOrdered(data, map[string]int{"d": 0, "b": 1}).Columns
	if want := []string{"d", "b", "a", "c"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("%v, want %v", got, want)
	}
}

func TestKeyOrderReadsKeysNotValues(t *testing.T) {
	doc := `{"k1": "k2", "arr": ["k3", {"k4": ["k5"]}], "k6": {"k1": 0}}`
	got := KeyOrder([]byte(doc))
	want := map[string]int{"k1": 0, "arr": 1, "k4": 2, "k6": 3}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("%v, want %v", got, want)
	}
}

func TestMergeKeyOrderKeepsEarliestPositions(t *testing.T) {
	order := KeyOrder([]byte(`[{"b":1,"a":2}]`))
	MergeKeyOrder(order, []byte(`[{"c":1,"a":2,"b":3}]`))
	if want := map[string]int{"b": 0, "a": 1, "c": 2}; !reflect.DeepEqual(order, want) {
		t.Fatalf("%v, want %v", order, want)
	}
	if !strings.Contains(strings.Join(ConvertToDataSetOrdered([]map[string]interface{}{{"a": 1, "b": 2, "c": 3}}, order).Columns, ","), "b,a,c") {
		t.Fatal("merged order not applied")
	}
}
