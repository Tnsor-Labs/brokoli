package engine

import (
	"bytes"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
)

// valueText is a cell as a writer that has only text to put it in renders
// it: a nested value (a map, a slice or an array, other than bytes) as
// JSON, anything else with %v.
//
// %v gave a nested value Go's own syntax, map[a:1 b:[2 3]], which no
// reader can parse back. A REST source hands nested objects and arrays
// through as they are, so that is what reached a Postgres or MySQL text
// column, a statement literal or a CSV field.
func valueText(v interface{}) string {
	if s, ok := nestedJSON(v); ok {
		return s
	}
	return fmt.Sprintf("%v", v)
}

// nestedJSON renders v as JSON when it is nested. It keeps <, > and &
// as they are, unlike json.Marshal, since the text is data rather than
// HTML. A value JSON cannot hold (a NaN inside it, say) is not nested
// JSON; the caller falls back to %v.
func nestedJSON(v interface{}) (string, bool) {
	if v == nil {
		return "", false
	}
	if _, isBytes := v.([]byte); isBytes {
		return "", false
	}
	switch reflect.ValueOf(v).Kind() {
	case reflect.Map, reflect.Slice, reflect.Array:
	default:
		return "", false
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return "", false
	}
	return strings.TrimSuffix(buf.String(), "\n"), true
}
