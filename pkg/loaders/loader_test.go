package loaders

import (
	"testing"

	"github.com/Tnsor-Labs/brokoli/pkg/common"
)

func TestGetLoader(t *testing.T) {
	tests := []struct {
		name     string
		filePath string
		wantType interface{}
		wantErr  bool
	}{
		{
			name:     "CSV file",
			filePath: "test.csv",
			wantType: &CSVLoader{},
			wantErr:  false,
		},
		{
			name:     "JSON file",
			filePath: "test.json",
			wantType: &JSONLoader{},
			wantErr:  false,
		},
		{
			name:     "XML file",
			filePath: "test.xml",
			wantType: &XMLLoader{},
			wantErr:  false,
		},
		{
			name:     "Excel file (xlsx)",
			filePath: "test.xlsx",
			wantType: &ExcelLoader{},
			wantErr:  false,
		},
		{
			name:     "Excel file (xls)",
			filePath: "test.xls",
			wantType: &ExcelLoader{},
			wantErr:  false,
		},
		{
			name:     "Unsupported file type",
			filePath: "test.unsupported",
			wantType: nil,
			wantErr:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			loader, err := GetLoader(tt.filePath)

			// Check error
			if (err != nil) != tt.wantErr {
				t.Errorf("GetLoader() error = %v, wantErr %v", err, tt.wantErr)
				return
			}

			// If we expect an error, no need to check the loader type
			if tt.wantErr {
				return
			}

			// Check loader type
			switch tt.wantType.(type) {
			case *CSVLoader:
				if _, ok := loader.(*CSVLoader); !ok {
					t.Errorf("GetLoader() returned wrong type for %s", tt.filePath)
				}
			case *JSONLoader:
				if _, ok := loader.(*JSONLoader); !ok {
					t.Errorf("GetLoader() returned wrong type for %s", tt.filePath)
				}
			case *XMLLoader:
				if _, ok := loader.(*XMLLoader); !ok {
					t.Errorf("GetLoader() returned wrong type for %s", tt.filePath)
				}
			case *ExcelLoader:
				if _, ok := loader.(*ExcelLoader); !ok {
					t.Errorf("GetLoader() returned wrong type for %s", tt.filePath)
				}
			default:
				t.Errorf("Unknown expected type for %s", tt.filePath)
			}
		})
	}
}

func TestDataSet(t *testing.T) {
	// Test creating and manipulating a DataSet
	ds := &common.DataSet{
		Columns: []string{"name", "age", "city"},
		Rows: []common.DataRow{
			{"name": "John Doe", "age": "30", "city": "New York"},
			{"name": "Jane Smith", "age": "25", "city": "London"},
		},
	}

	// Test DataSet structure
	if len(ds.Columns) != 3 {
		t.Errorf("Expected 3 columns, got %d", len(ds.Columns))
	}

	if len(ds.Rows) != 2 {
		t.Errorf("Expected 2 rows, got %d", len(ds.Rows))
	}

	// Test accessing data
	if ds.Rows[0]["name"] != "John Doe" {
		t.Errorf("Expected name 'John Doe', got %v", ds.Rows[0]["name"])
	}

	if ds.Rows[1]["city"] != "London" {
		t.Errorf("Expected city 'London', got %v", ds.Rows[1]["city"])
	}

	// Test modifying data
	ds.Rows[0]["age"] = "31"
	if ds.Rows[0]["age"] != "31" {
		t.Errorf("Expected age '31' after modification, got %v", ds.Rows[0]["age"])
	}

	// Test adding a new row
	ds.Rows = append(ds.Rows, common.DataRow{"name": "Bob Johnson", "age": "40", "city": "Chicago"})
	if len(ds.Rows) != 3 {
		t.Errorf("Expected 3 rows after adding a row, got %d", len(ds.Rows))
	}

	// Test adding a new column
	ds.Columns = append(ds.Columns, "country")
	ds.Rows[0]["country"] = "USA"
	ds.Rows[1]["country"] = "UK"
	ds.Rows[2]["country"] = "USA"

	if len(ds.Columns) != 4 {
		t.Errorf("Expected 4 columns after adding a column, got %d", len(ds.Columns))
	}

	if ds.Rows[1]["country"] != "UK" {
		t.Errorf("Expected country 'UK', got %v", ds.Rows[1]["country"])
	}
}
