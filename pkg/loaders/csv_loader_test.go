package loaders

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/hc12r/brokolisql-go/pkg/common"
)

func TestCSVLoader_Load(t *testing.T) {
	// Create a temporary directory for test files
	tempDir, err := os.MkdirTemp("", "csv_loader_test")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	// Create a test CSV file
	csvContent := "Name,Age,City\nJohn Doe,30,New York\nJane Smith,25,London\n"
	csvPath := filepath.Join(tempDir, "test.csv")
	if err := os.WriteFile(csvPath, []byte(csvContent), 0644); err != nil {
		t.Fatalf("Failed to write test CSV file: %v", err)
	}

	// Create a test CSV file with empty rows
	emptyCSVContent := "Name,Age,City\n"
	emptyCSVPath := filepath.Join(tempDir, "empty.csv")
	if err := os.WriteFile(emptyCSVPath, []byte(emptyCSVContent), 0644); err != nil {
		t.Fatalf("Failed to write empty CSV file: %v", err)
	}

	// Create a test CSV file with missing values
	missingCSVContent := "Name,Age,City\nJohn Doe,30,\n,25,London\n"
	missingCSVPath := filepath.Join(tempDir, "missing.csv")
	if err := os.WriteFile(missingCSVPath, []byte(missingCSVContent), 0644); err != nil {
		t.Fatalf("Failed to write missing CSV file: %v", err)
	}

	tests := []struct {
		name     string
		filePath string
		want     *common.DataSet
		wantErr  bool
	}{
		{
			name:     "Valid CSV file",
			filePath: csvPath,
			want: &common.DataSet{
				Columns: []string{"Name", "Age", "City"},
				Rows: []common.DataRow{
					{"Name": "John Doe", "Age": "30", "City": "New York"},
					{"Name": "Jane Smith", "Age": "25", "City": "London"},
				},
			},
			wantErr: false,
		},
		{
			name:     "Empty CSV file",
			filePath: emptyCSVPath,
			want: &common.DataSet{
				Columns: []string{"Name", "Age", "City"},
				Rows:    []common.DataRow{},
			},
			wantErr: false,
		},
		{
			name:     "CSV file with missing values",
			filePath: missingCSVPath,
			want: &common.DataSet{
				Columns: []string{"Name", "Age", "City"},
				Rows: []common.DataRow{
					{"Name": "John Doe", "Age": "30", "City": ""},
					{"Name": "", "Age": "25", "City": "London"},
				},
			},
			wantErr: false,
		},
		{
			name:     "Non-existent file",
			filePath: filepath.Join(tempDir, "nonexistent.csv"),
			want:     nil,
			wantErr:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			l := &CSVLoader{}
			got, err := l.Load(tt.filePath)
			if (err != nil) != tt.wantErr {
				t.Errorf("CSVLoader.Load() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !tt.wantErr && !reflect.DeepEqual(got, tt.want) {
				t.Errorf("CSVLoader.Load() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestGetLoader_CSV(t *testing.T) {
	loader, err := GetLoader("test.csv")
	if err != nil {
		t.Errorf("GetLoader() error = %v", err)
		return
	}

	if _, ok := loader.(*CSVLoader); !ok {
		t.Errorf("GetLoader() returned wrong loader type for CSV file")
	}
}
