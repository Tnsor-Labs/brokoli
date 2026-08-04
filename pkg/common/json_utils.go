package common

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
)

type DataRow map[string]interface{}

type DataSet struct {
	Columns []string
	Rows    []DataRow
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

func ConvertToDataSet(data []map[string]interface{}) *DataSet {

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
