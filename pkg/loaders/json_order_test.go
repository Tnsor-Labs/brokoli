package loaders

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// A JSON file's columns come out in the order the file has them, the same
// on every load. They used to come from ranging over a map.
func TestJSONLoaderKeepsTheFilesColumnOrder(t *testing.T) {
	p := filepath.Join(t.TempDir(), "orders.json")
	if err := os.WriteFile(p, []byte(`[{"zeta": 1, "alpha": 2, "mid": 3}, {"alpha": 4, "late": 5}]`), 0o644); err != nil {
		t.Fatal(err)
	}
	want := []string{"zeta", "alpha", "mid", "late"}
	for i := 0; i < 20; i++ {
		ds, err := (&JSONLoader{}).Load(p)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(ds.Columns, want) {
			t.Fatalf("load %d: columns %v, want %v", i, ds.Columns, want)
		}
	}
}
