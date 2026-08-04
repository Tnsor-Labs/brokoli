package common

import "testing"

func TestExtractPath(t *testing.T) {
	root := map[string]interface{}{
		"meta": map[string]interface{}{
			"next_cursor": "abc123",
			"count":       float64(5),
		},
		"results": []interface{}{1, 2, 3},
	}

	tests := []struct {
		name    string
		path    string
		wantOk  bool
		wantVal interface{}
	}{
		{"empty path returns root", "", true, root},
		{"single key", "results", true, root["results"]},
		{"nested key", "meta.next_cursor", true, "abc123"},
		{"nested numeric", "meta.count", true, float64(5)},
		{"missing key", "meta.missing", false, nil},
		{"missing top-level key", "nope", false, nil},
		{"path through non-object", "results.foo", false, nil},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := ExtractPath(root, tt.path)
			if ok != tt.wantOk {
				t.Fatalf("ExtractPath(%q) ok = %v, want %v", tt.path, ok, tt.wantOk)
			}
			if ok {
				gotSlice, gotIsSlice := got.([]interface{})
				wantSlice, wantIsSlice := tt.wantVal.([]interface{})
				if gotIsSlice && wantIsSlice {
					if len(gotSlice) != len(wantSlice) {
						t.Fatalf("ExtractPath(%q) = %v, want %v", tt.path, got, tt.wantVal)
					}
					return
				}
				if tt.path != "" && got != tt.wantVal {
					t.Fatalf("ExtractPath(%q) = %v, want %v", tt.path, got, tt.wantVal)
				}
			}
		})
	}
}

func TestExtractRecordsAtPath(t *testing.T) {
	jsonBytes := []byte(`{"results": [{"id": 1}, {"id": 2}], "endOfRecords": true}`)

	records, err := ExtractRecordsAtPath(jsonBytes, "results")
	if err != nil {
		t.Fatalf("ExtractRecordsAtPath() error = %v", err)
	}
	if len(records) != 2 {
		t.Fatalf("expected 2 records, got %d", len(records))
	}
	if records[0]["id"] != float64(1) {
		t.Errorf("expected first record id=1, got %v", records[0]["id"])
	}
}

func TestExtractRecordsAtPath_NestedPath(t *testing.T) {
	jsonBytes := []byte(`{"data": {"items": [{"id": 1}, {"id": 2}, {"id": 3}]}}`)

	records, err := ExtractRecordsAtPath(jsonBytes, "data.items")
	if err != nil {
		t.Fatalf("ExtractRecordsAtPath() error = %v", err)
	}
	if len(records) != 3 {
		t.Fatalf("expected 3 records, got %d", len(records))
	}
}

func TestExtractRecordsAtPath_MissingPath(t *testing.T) {
	jsonBytes := []byte(`{"results": [{"id": 1}]}`)

	if _, err := ExtractRecordsAtPath(jsonBytes, "data.items"); err == nil {
		t.Fatal("expected error for missing records path, got nil")
	}
}

func TestExtractRecordsAtPath_NotAnArray(t *testing.T) {
	jsonBytes := []byte(`{"results": {"not": "an array"}}`)

	if _, err := ExtractRecordsAtPath(jsonBytes, "results"); err == nil {
		t.Fatal("expected error when records path resolves to a non-array, got nil")
	}
}

func TestExtractRecordsAtPath_InvalidJSON(t *testing.T) {
	if _, err := ExtractRecordsAtPath([]byte(`not json`), "results"); err == nil {
		t.Fatal("expected error for invalid JSON, got nil")
	}
}
