package common

import (
	"bytes"
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"strings"
)

type DataRow map[string]interface{}

type DataSet struct {
	Columns []string
	Rows    []DataRow
}

// KeyOrder records, for every object key in a JSON document, the position
// of its first appearance. Record keys keep their relative order even
// when nested objects' keys are interleaved with them, which is all a
// column order needs. A document that does not parse yields what was read
// before the error.
func KeyOrder(raw []byte) map[string]int {
	order := map[string]int{}
	merge := func(key string) {
		if _, seen := order[key]; !seen {
			order[key] = len(order)
		}
	}
	KeyOrderInto(raw, merge)
	return order
}

// KeyOrderInto calls add with each object key in first-appearance order;
// MergeKeyOrder uses it to extend an order across several documents, such
// as the pages of a paginated response.
func KeyOrderInto(raw []byte, add func(key string)) {
	type frame struct{ object, expectKey bool }
	var stack []frame
	dec := json.NewDecoder(bytes.NewReader(raw))
	for {
		tok, err := dec.Token()
		if err != nil {
			return
		}
		switch v := tok.(type) {
		case json.Delim:
			switch v {
			case '{':
				stack = append(stack, frame{object: true, expectKey: true})
				continue
			case '[':
				stack = append(stack, frame{})
				continue
			default: // '}' or ']': the container is a completed value
				if len(stack) > 0 {
					stack = stack[:len(stack)-1]
				}
			}
		case string:
			if n := len(stack); n > 0 && stack[n-1].object && stack[n-1].expectKey {
				add(v)
				stack[n-1].expectKey = false
				continue
			}
		}
		// A value just completed; inside an object, a key comes next.
		if n := len(stack); n > 0 && stack[n-1].object {
			stack[n-1].expectKey = true
		}
	}
}

// MergeKeyOrder extends order with the keys of another document, keeping
// every key's earliest position.
func MergeKeyOrder(order map[string]int, raw []byte) {
	KeyOrderInto(raw, func(key string) {
		if _, seen := order[key]; !seen {
			order[key] = len(order)
		}
	})
}

func ParseJSONData(jsonBytes []byte) ([]map[string]interface{}, error) {
	// Try 1: array of objects — most common: [{...}, {...}]
	var data []map[string]interface{}
	if err := json.Unmarshal(jsonBytes, &data); err == nil && len(data) > 0 {
		return data, nil
	}

	// Try 2: single object — {...}
	var singleObject map[string]interface{}
	if err := json.Unmarshal(jsonBytes, &singleObject); err == nil {
		return []map[string]interface{}{singleObject}, nil
	}

	// Try 3: mixed array — [dict, [array], value, ...]
	// Handles APIs like World Bank: [metadata_dict, [data_dict, data_dict, ...]]
	var mixedArray []interface{}
	if err := json.Unmarshal(jsonBytes, &mixedArray); err == nil && len(mixedArray) > 0 {
		var result []map[string]interface{}
		for _, item := range mixedArray {
			switch v := item.(type) {
			case map[string]interface{}:
				result = append(result, v)
			case []interface{}:
				for _, sub := range v {
					if m, ok := sub.(map[string]interface{}); ok {
						result = append(result, m)
					}
				}
			}
		}
		if len(result) > 0 {
			return result, nil
		}
	}

	return nil, fmt.Errorf("no data found in JSON content")
}

// ExtractPath navigates a dot-separated path (e.g. "data.items" or
// "meta.next_cursor") through nested maps in a decoded JSON value and
// returns the value found there. An empty path returns root unchanged.
// Used by source_api's `records`/`value_path` config and by pagination
// strategies that read a cursor/next-link/end-flag out of the response body.
func ExtractPath(root interface{}, path string) (interface{}, bool) {
	if path == "" {
		return root, true
	}
	current := root
	for _, key := range strings.Split(path, ".") {
		m, ok := current.(map[string]interface{})
		if !ok {
			return nil, false
		}
		v, ok := m[key]
		if !ok {
			return nil, false
		}
		current = v
	}
	return current, true
}

// ExtractRecordsAtPath unmarshals jsonBytes and extracts the array of record
// objects found at the given dot-path (e.g. "results" or "data.items"),
// converting each element to a map[string]interface{}. This is the explicit
// counterpart to ParseJSONData's auto-detection, used when a source_api
// node's `records` config names exactly where the record array lives in an
// envelope-shaped response (e.g. {"results": [...], "endOfRecords": true}) —
// a shape ParseJSONData's auto-detection mishandles by wrapping the whole
// envelope as a single row.
func ExtractRecordsAtPath(jsonBytes []byte, path string) ([]map[string]interface{}, error) {
	var root interface{}
	if err := json.Unmarshal(jsonBytes, &root); err != nil {
		return nil, fmt.Errorf("invalid JSON: %w", err)
	}
	value, ok := ExtractPath(root, path)
	if !ok {
		return nil, fmt.Errorf("records path %q not found in response", path)
	}
	arr, ok := value.([]interface{})
	if !ok {
		return nil, fmt.Errorf("records path %q did not resolve to an array (got %T)", path, value)
	}
	records := make([]map[string]interface{}, 0, len(arr))
	for _, item := range arr {
		m, ok := item.(map[string]interface{})
		if !ok {
			return nil, fmt.Errorf("records path %q contains a non-object element (got %T)", path, item)
		}
		records = append(records, m)
	}
	return records, nil
}

// ConvertToDataSet builds a dataset from decoded JSON objects, with the
// columns in alphabetical order. Decoding into Go maps loses the order the
// keys had in the document; callers that still have the raw bytes use
// ConvertToDataSetOrdered to keep it.
func ConvertToDataSet(data []map[string]interface{}) *DataSet {
	return ConvertToDataSetOrdered(data, nil)
}

// ConvertToDataSetOrdered builds a dataset whose columns follow order
// (from KeyOrder): each key's first appearance in the document. Keys the
// order does not know come after, alphabetically. Either way the columns
// are the same on every run: they used to come from ranging over a map,
// so a JSON source feeding a CSV sink wrote its columns in a different
// order from one run to the next.
func ConvertToDataSetOrdered(data []map[string]interface{}, order map[string]int) *DataSet {
	columnSet := make(map[string]bool)
	for _, obj := range data {
		for key := range obj {
			columnSet[key] = true
		}
	}

	columns := make([]string, 0, len(columnSet))
	for col := range columnSet {
		columns = append(columns, col)
	}
	sort.Slice(columns, func(i, j int) bool {
		oi, iKnown := order[columns[i]]
		oj, jKnown := order[columns[j]]
		switch {
		case iKnown && jKnown:
			return oi < oj
		case iKnown != jKnown:
			return iKnown
		}
		return columns[i] < columns[j]
	})

	rows := make([]DataRow, 0, len(data))
	for _, obj := range data {
		row := make(DataRow)
		for key, value := range obj {
			// Keep native types — Python code nodes need dicts/lists as-is.
			// Stringification happens at the output boundary (CSV, SQL, JSON preview).
			row[key] = value
		}
		rows = append(rows, row)
	}

	return &DataSet{
		Columns: columns,
		Rows:    rows,
	}
}

func IsComplex(v interface{}) bool {
	if v == nil {
		return false
	}

	kind := reflect.TypeOf(v).Kind()
	return kind == reflect.Map || kind == reflect.Slice || kind == reflect.Array
}
