package engine

import (
	"testing"

	"github.com/Tnsor-Labs/brokoli/pkg/common"
)

// A REST source passes nested objects and arrays through as they are. The
// text writers rendered them with %v, Go's own syntax (map[a:1 b:[2 3]]),
// which nothing downstream can parse. They now write JSON.
func TestNestedValuesAreWrittenAsJSON(t *testing.T) {
	object := map[string]interface{}{"b": []interface{}{int64(2), "x<y & z"}, "a": int64(9007199254740993)}
	const wantObject = `{"a":9007199254740993,"b":[2,"x<y & z"]}`
	array := []interface{}{map[string]interface{}{"k": true}, nil, 1.5}
	const wantArray = `[{"k":true},null,1.5]`

	writers := map[string]func(interface{}) string{
		"postgres COPY":   copyEscape,
		"mysql LOAD DATA": mysqlLoadEscape,
		"csv": func(v interface{}) string {
			return csvRecordFor(common.DataRow{"c": v}, []string{"c"})[0]
		},
		"postgres literal": func(v interface{}) string { return getDialect("postgres").formatValue(v) },
	}
	for name, write := range writers {
		gotObject, gotArray := write(object), write(array)
		if name == "postgres literal" {
			gotObject, gotArray = unquote(t, gotObject), unquote(t, gotArray)
		}
		if gotObject != wantObject {
			t.Errorf("%s object = %s, want %s", name, gotObject, wantObject)
		}
		if gotArray != wantArray {
			t.Errorf("%s array = %s, want %s", name, gotArray, wantArray)
		}
	}
}

func unquote(t *testing.T, s string) string {
	t.Helper()
	if len(s) < 2 || s[0] != '\'' || s[len(s)-1] != '\'' {
		t.Fatalf("not a quoted literal: %s", s)
	}
	return s[1 : len(s)-1]
}

// Only nested values change. Bytes stay text, scalars stay %v, and a value
// JSON cannot hold falls back to %v rather than failing the write.
func TestValueTextLeavesEverythingElseAlone(t *testing.T) {
	for v, want := range map[interface{}]string{
		"plain": "plain", int64(7): "7", true: "true", 2.5: "2.5",
	} {
		if got := valueText(v); got != want {
			t.Errorf("valueText(%v) = %q, want %q", v, got, want)
		}
	}
	if got := valueText([]byte("raw")); got != "[114 97 119]" {
		t.Errorf("bytes became %q; valueText keeps %%v for them, the writers handle bytes first", got)
	}
	if _, ok := nestedJSON([]interface{}{func() {}}); ok {
		t.Error("a value JSON cannot hold was reported as JSON")
	}
}
