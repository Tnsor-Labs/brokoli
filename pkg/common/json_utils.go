package common

import (
	"encoding/json"
	"fmt"
	"reflect"
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
