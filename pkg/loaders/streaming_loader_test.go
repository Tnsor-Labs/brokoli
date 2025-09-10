package loaders

import (
	"brokolisql-go/pkg/common"
	"os"
	"path/filepath"
	"testing"
)

func TestStreamingJSONLoader(t *testing.T) {
	// Create a temporary JSON file for testing
	tempDir := t.TempDir()
	jsonFilePath := filepath.Join(tempDir, "test.json")

	// Create test data
	jsonData := `[
		{"id": 1, "name": "John", "email": "john@example.com"},
		{"id": 2, "name": "Jane", "email": "jane@example.com"},
		{"id": 3, "name": "Bob", "email": "bob@example.com"}
	]`

	// Write test data to file
	err := os.WriteFile(jsonFilePath, []byte(jsonData), 0644)
	if err != nil {
		t.Fatalf("Failed to write test JSON file: %v", err)
	}

	// Create a streaming JSON loader
	loader := &StreamingJSONLoader{}

	// Test StreamLoad
	columns, rowsChan, doneChan, err := loader.StreamLoad(jsonFilePath)
	if err != nil {
		t.Fatalf("StreamLoad failed: %v", err)
	}

	// Verify columns
	expectedColumns := []string{"id", "name", "email"}
	if len(columns) != len(expectedColumns) {
		t.Errorf("Expected %d columns, got %d", len(expectedColumns), len(columns))
	}

	// Check that all expected columns are present
	columnMap := make(map[string]bool)
	for _, col := range columns {
		columnMap[col] = true
	}
	for _, col := range expectedColumns {
		if !columnMap[col] {
			t.Errorf("Expected column %s not found", col)
		}
	}

	// Read rows from channel
	var rows []common.DataRow
	for row := range rowsChan {
		if row == nil {
			t.Fatalf("Received nil row, indicating an error")
		}
		rows = append(rows, row)
	}

	// Wait for done signal
	<-doneChan

	// Verify number of rows
	if len(rows) != 3 {
		t.Errorf("Expected 3 rows, got %d", len(rows))
	}

	// Verify row data
	if rows[0]["id"] != float64(1) {
		t.Errorf("Expected id=1, got %v", rows[0]["id"])
	}
	if rows[0]["name"] != "John" {
		t.Errorf("Expected name=John, got %v", rows[0]["name"])
	}
	if rows[1]["name"] != "Jane" {
		t.Errorf("Expected name=Jane, got %v", rows[1]["name"])
	}
	if rows[2]["email"] != "bob@example.com" {
		t.Errorf("Expected email=bob@example.com, got %v", rows[2]["email"])
	}
}

func TestStreamingJSONLoader_SingleObject(t *testing.T) {
	// Create a temporary JSON file for testing
	tempDir := t.TempDir()
	jsonFilePath := filepath.Join(tempDir, "test_single.json")

	// Create test data with a single object
	jsonData := `{"id": 1, "name": "John", "email": "john@example.com"}`

	// Write test data to file
	err := os.WriteFile(jsonFilePath, []byte(jsonData), 0644)
	if err != nil {
		t.Fatalf("Failed to write test JSON file: %v", err)
	}

	// Create a streaming JSON loader
	loader := &StreamingJSONLoader{}

	// Test StreamLoad
	columns, rowsChan, doneChan, err := loader.StreamLoad(jsonFilePath)
	if err != nil {
		t.Fatalf("StreamLoad failed: %v", err)
	}

	// Verify columns
	expectedColumns := []string{"id", "name", "email"}
	if len(columns) != len(expectedColumns) {
		t.Errorf("Expected %d columns, got %d", len(expectedColumns), len(columns))
	}

	// Check that all expected columns are present
	columnMap := make(map[string]bool)
	for _, col := range columns {
		columnMap[col] = true
	}
	for _, col := range expectedColumns {
		if !columnMap[col] {
			t.Errorf("Expected column %s not found", col)
		}
	}

	// Read rows from channel
	var rows []common.DataRow
	for row := range rowsChan {
		if row == nil {
			t.Fatalf("Received nil row, indicating an error")
		}
		rows = append(rows, row)
	}

	// Wait for done signal
	<-doneChan

	// Verify number of rows
	if len(rows) != 1 {
		t.Errorf("Expected 1 row, got %d", len(rows))
	}

	// Verify row data
	if rows[0]["id"] != float64(1) {
		t.Errorf("Expected id=1, got %v", rows[0]["id"])
	}
	if rows[0]["name"] != "John" {
		t.Errorf("Expected name=John, got %v", rows[0]["name"])
	}
	if rows[0]["email"] != "john@example.com" {
		t.Errorf("Expected email=john@example.com, got %v", rows[0]["email"])
	}
}

func TestStreamingCSVLoader(t *testing.T) {
	// Create a temporary CSV file for testing
	tempDir := t.TempDir()
	csvFilePath := filepath.Join(tempDir, "test.csv")

	// Create test data
	csvData := "id,name,email\n1,John,john@example.com\n2,Jane,jane@example.com\n3,Bob,bob@example.com"

	// Write test data to file
	err := os.WriteFile(csvFilePath, []byte(csvData), 0644)
	if err != nil {
		t.Fatalf("Failed to write test CSV file: %v", err)
	}

	// Create a streaming CSV loader
	loader := &StreamingCSVLoader{}

	// Test StreamLoad
	columns, rowsChan, doneChan, err := loader.StreamLoad(csvFilePath)
	if err != nil {
		t.Fatalf("StreamLoad failed: %v", err)
	}

	// Verify columns
	expectedColumns := []string{"id", "name", "email"}
	if len(columns) != len(expectedColumns) {
		t.Errorf("Expected %d columns, got %d", len(expectedColumns), len(columns))
	}
	for i, col := range columns {
		if col != expectedColumns[i] {
			t.Errorf("Expected column %s, got %s", expectedColumns[i], col)
		}
	}

	// Read rows from channel
	var rows []common.DataRow
	for row := range rowsChan {
		if row == nil {
			t.Fatalf("Received nil row, indicating an error")
		}
		rows = append(rows, row)
	}

	// Wait for done signal
	<-doneChan

	// Verify number of rows
	if len(rows) != 3 {
		t.Errorf("Expected 3 rows, got %d", len(rows))
	}

	// Verify row data
	if rows[0]["id"] != "1" {
		t.Errorf("Expected id=1, got %v", rows[0]["id"])
	}
	if rows[0]["name"] != "John" {
		t.Errorf("Expected name=John, got %v", rows[0]["name"])
	}
	if rows[1]["name"] != "Jane" {
		t.Errorf("Expected name=Jane, got %v", rows[1]["name"])
	}
	if rows[2]["email"] != "bob@example.com" {
		t.Errorf("Expected email=bob@example.com, got %v", rows[2]["email"])
	}
}

func TestGetStreamingLoader(t *testing.T) {
	// Test JSON loader
	loader, err := GetStreamingLoader("test.json")
	if err != nil {
		t.Errorf("Failed to get JSON loader: %v", err)
	}
	if _, ok := loader.(*StreamingJSONLoader); !ok {
		t.Errorf("Expected StreamingJSONLoader, got %T", loader)
	}

	// Test CSV loader
	loader, err = GetStreamingLoader("test.csv")
	if err != nil {
		t.Errorf("Failed to get CSV loader: %v", err)
	}
	if _, ok := loader.(*StreamingCSVLoader); !ok {
		t.Errorf("Expected StreamingCSVLoader, got %T", loader)
	}

	// Test unsupported format
	_, err = GetStreamingLoader("test.unsupported")
	if err == nil {
		t.Errorf("Expected error for unsupported format, got nil")
	}
}
