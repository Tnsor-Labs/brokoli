package loaders

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/Tnsor-Labs/brokoli/pkg/common"
)

func TestJSONLoader_Load(t *testing.T) {
	// Create a temporary directory for test files
	tempDir, err := os.MkdirTemp("", "json_loader_test")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	// Create a test JSON file with array of objects
	arrayJSON := `[
		{"name": "John Doe", "age": 30, "city": "New York"},
		{"name": "Jane Smith", "age": 25, "city": "London"}
	]`
	arrayJSONPath := filepath.Join(tempDir, "array.json")
	if err := os.WriteFile(arrayJSONPath, []byte(arrayJSON), 0644); err != nil {
		t.Fatalf("Failed to write array JSON file: %v", err)
	}

	// Create a test JSON file with a single object
	singleJSON := `{"name": "John Doe", "age": 30, "city": "New York"}`
	singleJSONPath := filepath.Join(tempDir, "single.json")
	if err := os.WriteFile(singleJSONPath, []byte(singleJSON), 0644); err != nil {
		t.Fatalf("Failed to write single JSON file: %v", err)
	}

	// Create a test JSON file with nested objects
	nestedJSON := `[
		{"name": "John Doe", "age": 30, "address": {"city": "New York", "zip": "10001"}},
		{"name": "Jane Smith", "age": 25, "address": {"city": "London", "zip": "SW1A 1AA"}}
	]`
	nestedJSONPath := filepath.Join(tempDir, "nested.json")
	if err := os.WriteFile(nestedJSONPath, []byte(nestedJSON), 0644); err != nil {
		t.Fatalf("Failed to write nested JSON file: %v", err)
	}

	// Create an empty JSON file
	emptyJSON := `[]`
	emptyJSONPath := filepath.Join(tempDir, "empty.json")
	if err := os.WriteFile(emptyJSONPath, []byte(emptyJSON), 0644); err != nil {
		t.Fatalf("Failed to write empty JSON file: %v", err)
	}

	// Create an invalid JSON file
	invalidJSON := `{invalid json`
	invalidJSONPath := filepath.Join(tempDir, "invalid.json")
	if err := os.WriteFile(invalidJSONPath, []byte(invalidJSON), 0644); err != nil {
		t.Fatalf("Failed to write invalid JSON file: %v", err)
	}

	tests := []struct {
		name     string
		filePath string
		wantErr  bool
		checkFn  func(*testing.T, *common.DataSet)
	}{
		{
			name:     "Array of objects",
			filePath: arrayJSONPath,
			wantErr:  false,
			checkFn: func(t *testing.T, ds *common.DataSet) {
				if len(ds.Columns) != 3 {
					t.Errorf("Expected 3 columns, got %d", len(ds.Columns))
				}
				if len(ds.Rows) != 2 {
					t.Errorf("Expected 2 rows, got %d", len(ds.Rows))
				}

				// Check that all expected columns exist
				columnMap := make(map[string]bool)
				for _, col := range ds.Columns {
					columnMap[col] = true
				}
				for _, col := range []string{"name", "age", "city"} {
					if !columnMap[col] {
						t.Errorf("Expected column %s not found", col)
					}
				}

				// Check first row values
				if ds.Rows[0]["name"] != "John Doe" {
					t.Errorf("Expected name 'John Doe', got %v", ds.Rows[0]["name"])
				}
				if ds.Rows[0]["age"] != float64(30) {
					t.Errorf("Expected age 30, got %v", ds.Rows[0]["age"])
				}
			},
		},
		{
			name:     "Single object",
			filePath: singleJSONPath,
			wantErr:  false,
			checkFn: func(t *testing.T, ds *common.DataSet) {
				if len(ds.Columns) != 3 {
					t.Errorf("Expected 3 columns, got %d", len(ds.Columns))
				}
				if len(ds.Rows) != 1 {
					t.Errorf("Expected 1 row, got %d", len(ds.Rows))
				}
			},
		},
		{
			name:     "Nested objects",
			filePath: nestedJSONPath,
			wantErr:  false,
			checkFn: func(t *testing.T, ds *common.DataSet) {
				if len(ds.Columns) != 3 {
					t.Errorf("Expected 3 columns, got %d", len(ds.Columns))
				}
				if len(ds.Rows) != 2 {
					t.Errorf("Expected 2 rows, got %d", len(ds.Rows))
				}

				// Nested objects are kept as maps (not stringified)
				addressMap, ok := ds.Rows[0]["address"].(map[string]interface{})
				if !ok {
					t.Errorf("Expected address to be a map, got %T", ds.Rows[0]["address"])
				}
				if len(addressMap) == 0 {
					t.Errorf("Expected non-empty address map")
				}
			},
		},
		{
			name:     "Empty JSON array",
			filePath: emptyJSONPath,
			wantErr:  true,
		},
		{
			name:     "Invalid JSON",
			filePath: invalidJSONPath,
			wantErr:  true,
		},
		{
			name:     "Non-existent file",
			filePath: filepath.Join(tempDir, "nonexistent.json"),
			wantErr:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			l := &JSONLoader{}
			got, err := l.Load(tt.filePath)
			if (err != nil) != tt.wantErr {
				t.Errorf("JSONLoader.Load() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !tt.wantErr && tt.checkFn != nil {
				tt.checkFn(t, got)
			}
		})
	}
}

func TestGetLoader_JSON(t *testing.T) {
	loader, err := GetLoader("test.json")
	if err != nil {
		t.Errorf("GetLoader() error = %v", err)
		return
	}

	if _, ok := loader.(*JSONLoader); !ok {
		t.Errorf("GetLoader() returned wrong loader type for JSON file")
	}
}
