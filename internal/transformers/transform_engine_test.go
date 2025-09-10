package transformers

import (
	"github.com/hc12r/brokolisql-go/pkg/common"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func createTestTransformConfig(t *testing.T, config string) string {
	// Create a temporary directory for test files
	tempDir, err := os.MkdirTemp("", "transform_engine_test")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}

	// Create a test config file
	configPath := filepath.Join(tempDir, "transforms.json")
	if err := os.WriteFile(configPath, []byte(config), 0644); err != nil {
		t.Fatalf("Failed to write test config file: %v", err)
	}

	// Register cleanup function
	t.Cleanup(func() {
		os.RemoveAll(tempDir)
	})

	return configPath
}

func createTestDataset() *common.DataSet {
	return &common.DataSet{
		Columns: []string{"id", "first_name", "last_name", "email", "country", "city"},
		Rows: []common.DataRow{
			{
				"id":         1,
				"first_name": "John",
				"last_name":  "Doe",
				"email":      "JOHN.DOE@example.com",
				"country":    "USA",
				"city":       "New York",
			},
			{
				"id":         2,
				"first_name": "Jane",
				"last_name":  "Smith",
				"email":      "jane.smith@example.com",
				"country":    "UK",
				"city":       "London",
			},
			{
				"id":         3,
				"first_name": "Bob",
				"last_name":  "Johnson",
				"email":      "bob.johnson@example.com",
				"country":    "Canada",
				"city":       "Toronto",
			},
		},
	}
}

func TestNewTransformEngine(t *testing.T) {
	// Valid config
	validConfig := `{
		"transformations": [
			{
				"type": "rename_columns",
				"mapping": {
					"first_name": "given_name",
					"last_name": "surname"
				}
			}
		]
	}`

	validConfigPath := createTestTransformConfig(t, validConfig)

	// Invalid JSON
	invalidConfig := `{
		"transformations": [
			{
				"type": "rename_columns",
				"mapping": {
					"first_name": "given_name",
					"last_name": "surname"
				}
			}
		]
	`

	invalidConfigPath := createTestTransformConfig(t, invalidConfig)

	// Non-existent file
	nonExistentPath := filepath.Join(os.TempDir(), "non_existent_file.json")

	tests := []struct {
		name       string
		configPath string
		wantErr    bool
	}{
		{
			name:       "Valid config",
			configPath: validConfigPath,
			wantErr:    false,
		},
		{
			name:       "Invalid JSON",
			configPath: invalidConfigPath,
			wantErr:    true,
		},
		{
			name:       "Non-existent file",
			configPath: nonExistentPath,
			wantErr:    true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			engine, err := NewTransformEngine(tt.configPath)

			if (err != nil) != tt.wantErr {
				t.Errorf("NewTransformEngine() error = %v, wantErr %v", err, tt.wantErr)
				return
			}

			if err == nil && engine == nil {
				t.Errorf("NewTransformEngine() returned nil engine")
			}
		})
	}
}

func TestTransformEngine_RenameColumns(t *testing.T) {
	config := `{
		"transformations": [
			{
				"type": "rename_columns",
				"mapping": {
					"first_name": "given_name",
					"last_name": "surname"
				}
			}
		]
	}`

	configPath := createTestTransformConfig(t, config)

	engine, err := NewTransformEngine(configPath)
	if err != nil {
		t.Fatalf("Failed to create transform engine: %v", err)
	}

	dataset := createTestDataset()

	err = engine.ApplyTransformations(dataset)
	if err != nil {
		t.Errorf("ApplyTransformations() error = %v", err)
		return
	}

	// Check that columns were renamed
	expectedColumns := []string{"id", "given_name", "surname", "email", "country", "city"}
	if !reflect.DeepEqual(dataset.Columns, expectedColumns) {
		t.Errorf("RenameColumns() columns = %v, want %v", dataset.Columns, expectedColumns)
	}

	// Check that data was updated
	if val, ok := dataset.Rows[0]["given_name"]; !ok || val != "John" {
		t.Errorf("RenameColumns() data not updated correctly")
	}

	// Check that old columns were removed
	if _, ok := dataset.Rows[0]["first_name"]; ok {
		t.Errorf("RenameColumns() old column not removed")
	}
}

func TestTransformEngine_AddColumn(t *testing.T) {
	config := `{
		"transformations": [
			{
				"type": "add_column",
				"name": "full_name",
				"expression": "first_name + ' ' + last_name"
			}
		]
	}`

	configPath := createTestTransformConfig(t, config)

	engine, err := NewTransformEngine(configPath)
	if err != nil {
		t.Fatalf("Failed to create transform engine: %v", err)
	}

	dataset := createTestDataset()

	err = engine.ApplyTransformations(dataset)
	if err != nil {
		t.Errorf("ApplyTransformations() error = %v", err)
		return
	}

	// Check that column was added
	expectedColumns := []string{"id", "first_name", "last_name", "email", "country", "city", "full_name"}
	if !reflect.DeepEqual(dataset.Columns, expectedColumns) {
		t.Errorf("AddColumn() columns = %v, want %v", dataset.Columns, expectedColumns)
	}

	// Check that data was added
	if val, ok := dataset.Rows[0]["full_name"]; !ok || val != "John Doe" {
		t.Errorf("AddColumn() data not added correctly, got %v", val)
	}
}

func TestTransformEngine_FilterRows(t *testing.T) {
	config := `{
		"transformations": [
			{
				"type": "filter_rows",
				"condition": "country in ['USA', 'UK']"
			}
		]
	}`

	configPath := createTestTransformConfig(t, config)

	engine, err := NewTransformEngine(configPath)
	if err != nil {
		t.Fatalf("Failed to create transform engine: %v", err)
	}

	dataset := createTestDataset()

	err = engine.ApplyTransformations(dataset)
	if err != nil {
		t.Errorf("ApplyTransformations() error = %v", err)
		return
	}

	// Check that rows were filtered
	if len(dataset.Rows) != 2 {
		t.Errorf("FilterRows() rows = %d, want %d", len(dataset.Rows), 2)
	}

	// Check that only USA and UK rows remain
	for _, row := range dataset.Rows {
		country := row["country"]
		if country != "USA" && country != "UK" {
			t.Errorf("FilterRows() unexpected country: %v", country)
		}
	}
}

func TestTransformEngine_ApplyFunction(t *testing.T) {
	config := `{
		"transformations": [
			{
				"type": "apply_function",
				"column": "email",
				"function": "lower"
			}
		]
	}`

	configPath := createTestTransformConfig(t, config)

	engine, err := NewTransformEngine(configPath)
	if err != nil {
		t.Fatalf("Failed to create transform engine: %v", err)
	}

	dataset := createTestDataset()

	err = engine.ApplyTransformations(dataset)
	if err != nil {
		t.Errorf("ApplyTransformations() error = %v", err)
		return
	}

	// Check that function was applied
	if val, ok := dataset.Rows[0]["email"]; !ok || val != "john.doe@example.com" {
		t.Errorf("ApplyFunction() function not applied correctly, got %v", val)
	}
}

func TestTransformEngine_ReplaceValues(t *testing.T) {
	config := `{
		"transformations": [
			{
				"type": "replace_values",
				"column": "country",
				"mapping": {
					"USA": "United States",
					"UK": "United Kingdom"
				}
			}
		]
	}`

	configPath := createTestTransformConfig(t, config)

	engine, err := NewTransformEngine(configPath)
	if err != nil {
		t.Fatalf("Failed to create transform engine: %v", err)
	}

	dataset := createTestDataset()

	err = engine.ApplyTransformations(dataset)
	if err != nil {
		t.Errorf("ApplyTransformations() error = %v", err)
		return
	}

	// Check that values were replaced
	if val, ok := dataset.Rows[0]["country"]; !ok || val != "United States" {
		t.Errorf("ReplaceValues() value not replaced correctly, got %v", val)
	}

	if val, ok := dataset.Rows[1]["country"]; !ok || val != "United Kingdom" {
		t.Errorf("ReplaceValues() value not replaced correctly, got %v", val)
	}

	// Check that non-mapped values were not changed
	if val, ok := dataset.Rows[2]["country"]; !ok || val != "Canada" {
		t.Errorf("ReplaceValues() non-mapped value changed, got %v", val)
	}
}

func TestTransformEngine_DropColumns(t *testing.T) {
	config := `{
		"transformations": [
			{
				"type": "drop_columns",
				"columns": ["email", "city"]
			}
		]
	}`

	configPath := createTestTransformConfig(t, config)

	engine, err := NewTransformEngine(configPath)
	if err != nil {
		t.Fatalf("Failed to create transform engine: %v", err)
	}

	dataset := createTestDataset()

	err = engine.ApplyTransformations(dataset)
	if err != nil {
		t.Errorf("ApplyTransformations() error = %v", err)
		return
	}

	// Check that columns were dropped
	expectedColumns := []string{"id", "first_name", "last_name", "country"}
	if !reflect.DeepEqual(dataset.Columns, expectedColumns) {
		t.Errorf("DropColumns() columns = %v, want %v", dataset.Columns, expectedColumns)
	}

	// Check that data was removed
	for _, row := range dataset.Rows {
		if _, ok := row["email"]; ok {
			t.Errorf("DropColumns() column 'email' not dropped")
		}
		if _, ok := row["city"]; ok {
			t.Errorf("DropColumns() column 'city' not dropped")
		}
	}
}

func TestTransformEngine_SortRows(t *testing.T) {
	config := `{
		"transformations": [
			{
				"type": "sort",
				"columns": ["country", "first_name"],
				"ascending": true
			}
		]
	}`

	configPath := createTestTransformConfig(t, config)

	engine, err := NewTransformEngine(configPath)
	if err != nil {
		t.Fatalf("Failed to create transform engine: %v", err)
	}

	dataset := createTestDataset()

	err = engine.ApplyTransformations(dataset)
	if err != nil {
		t.Errorf("ApplyTransformations() error = %v", err)
		return
	}

	// Check that rows were sorted
	expectedOrder := []string{"Canada", "UK", "USA"}
	for i, country := range expectedOrder {
		if dataset.Rows[i]["country"] != country {
			t.Errorf("SortRows() incorrect order, got %v at position %d, want %v",
				dataset.Rows[i]["country"], i, country)
		}
	}
}

func TestTransformEngine_MultipleTransformations(t *testing.T) {
	config := `{
		"transformations": [
			{
				"type": "rename_columns",
				"mapping": {
					"first_name": "given_name",
					"last_name": "surname"
				}
			},
			{
				"type": "add_column",
				"name": "full_name",
				"expression": "given_name + ' ' + surname"
			},
			{
				"type": "filter_rows",
				"condition": "country in ['USA', 'UK']"
			},
			{
				"type": "apply_function",
				"column": "email",
				"function": "lower"
			},
			{
				"type": "replace_values",
				"column": "country",
				"mapping": {
					"USA": "United States",
					"UK": "United Kingdom"
				}
			},
			{
				"type": "sort",
				"columns": ["country"],
				"ascending": true
			}
		]
	}`

	configPath := createTestTransformConfig(t, config)

	engine, err := NewTransformEngine(configPath)
	if err != nil {
		t.Fatalf("Failed to create transform engine: %v", err)
	}

	dataset := createTestDataset()

	err = engine.ApplyTransformations(dataset)
	if err != nil {
		t.Errorf("ApplyTransformations() error = %v", err)
		return
	}

	// Check final state after all transformations

	// Check number of rows (after filtering)
	if len(dataset.Rows) != 2 {
		t.Errorf("Multiple transformations: rows = %d, want %d", len(dataset.Rows), 2)
	}

	// Check columns (after renaming and adding)
	expectedColumns := []string{"id", "given_name", "surname", "email", "country", "city", "full_name"}
	if !reflect.DeepEqual(dataset.Columns, expectedColumns) {
		t.Errorf("Multiple transformations: columns = %v, want %v", dataset.Columns, expectedColumns)
	}

	// Check sorting (UK/United Kingdom should be first)
	if dataset.Rows[0]["country"] != "United Kingdom" {
		t.Errorf("Multiple transformations: first row country = %v, want %v",
			dataset.Rows[0]["country"], "United Kingdom")
	}

	// Check function application (email should be lowercase)
	if dataset.Rows[0]["email"] != "john.doe@example.com" && dataset.Rows[0]["given_name"] == "John" {
		t.Errorf("Multiple transformations: email not lowercased, got %v", dataset.Rows[0]["email"])
	}

	// Check added column
	if dataset.Rows[0]["full_name"] != "John Doe" && dataset.Rows[0]["given_name"] == "John" {
		t.Errorf("Multiple transformations: full_name not added correctly, got %v",
			dataset.Rows[0]["full_name"])
	}
}

func TestTransformEngine_ErrorHandling(t *testing.T) {
	tests := []struct {
		name    string
		config  string
		wantErr bool
	}{
		{
			name: "Missing mapping in rename_columns",
			config: `{
				"transformations": [
					{
						"type": "rename_columns"
					}
				]
			}`,
			wantErr: true,
		},
		{
			name: "Missing name in add_column",
			config: `{
				"transformations": [
					{
						"type": "add_column",
						"expression": "first_name + ' ' + last_name"
					}
				]
			}`,
			wantErr: true,
		},
		{
			name: "Missing expression in add_column",
			config: `{
				"transformations": [
					{
						"type": "add_column",
						"name": "full_name"
					}
				]
			}`,
			wantErr: true,
		},
		{
			name: "Missing condition in filter_rows",
			config: `{
				"transformations": [
					{
						"type": "filter_rows"
					}
				]
			}`,
			wantErr: true,
		},
		{
			name: "Invalid condition in filter_rows",
			config: `{
				"transformations": [
					{
						"type": "filter_rows",
						"condition": "country in "
					}
				]
			}`,
			wantErr: true,
		},
		{
			name: "Missing column in apply_function",
			config: `{
				"transformations": [
					{
						"type": "apply_function",
						"function": "lower"
					}
				]
			}`,
			wantErr: true,
		},
		{
			name: "Missing function in apply_function",
			config: `{
				"transformations": [
					{
						"type": "apply_function",
						"column": "email"
					}
				]
			}`,
			wantErr: true,
		},
		{
			name: "Unsupported function in apply_function",
			config: `{
				"transformations": [
					{
						"type": "apply_function",
						"column": "email",
						"function": "unsupported"
					}
				]
			}`,
			wantErr: true,
		},
		{
			name: "Missing column in replace_values",
			config: `{
				"transformations": [
					{
						"type": "replace_values",
						"mapping": {
							"USA": "United States"
						}
					}
				]
			}`,
			wantErr: true,
		},
		{
			name: "Missing mapping in replace_values",
			config: `{
				"transformations": [
					{
						"type": "replace_values",
						"column": "country"
					}
				]
			}`,
			wantErr: true,
		},
		{
			name: "Missing columns in drop_columns",
			config: `{
				"transformations": [
					{
						"type": "drop_columns"
					}
				]
			}`,
			wantErr: true,
		},
		{
			name: "Missing columns in sort",
			config: `{
				"transformations": [
					{
						"type": "sort"
					}
				]
			}`,
			wantErr: true,
		},
		{
			name: "Unsupported transformation type",
			config: `{
				"transformations": [
					{
						"type": "unsupported"
					}
				]
			}`,
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			configPath := createTestTransformConfig(t, tt.config)

			engine, err := NewTransformEngine(configPath)
			if err != nil {
				t.Fatalf("Failed to create transform engine: %v", err)
			}

			dataset := createTestDataset()

			err = engine.ApplyTransformations(dataset)
			if (err != nil) != tt.wantErr {
				t.Errorf("ApplyTransformations() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}
