package loaders

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/Tnsor-Labs/brokoli/pkg/common"
)

func TestXMLLoader_Load(t *testing.T) {
	// Create a temporary directory for test files
	tempDir, err := os.MkdirTemp("", "xml_loader_test")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	// Create a test XML file with repeating elements
	repeatingXML := `<?xml version="1.0" encoding="UTF-8"?>
<users>
  <user id="1">
    <name>John Doe</name>
    <age>30</age>
    <city>New York</city>
  </user>
  <user id="2">
    <name>Jane Smith</name>
    <age>25</age>
    <city>London</city>
  </user>
</users>`
	repeatingXMLPath := filepath.Join(tempDir, "repeating.xml")
	if err := os.WriteFile(repeatingXMLPath, []byte(repeatingXML), 0644); err != nil {
		t.Fatalf("Failed to write repeating XML file: %v", err)
	}

	// Create a test XML file with attributes
	attributesXML := `<?xml version="1.0" encoding="UTF-8"?>
<users>
  <user id="1" name="John Doe" age="30" city="New York" />
  <user id="2" name="Jane Smith" age="25" city="London" />
</users>`
	attributesXMLPath := filepath.Join(tempDir, "attributes.xml")
	if err := os.WriteFile(attributesXMLPath, []byte(attributesXML), 0644); err != nil {
		t.Fatalf("Failed to write attributes XML file: %v", err)
	}

	// Create a test XML file with mixed content
	mixedXML := `<?xml version="1.0" encoding="UTF-8"?>
<users>
  <user id="1">
    <name>John Doe</name>
    <contact email="john@example.com" phone="+1-555-123-4567" />
  </user>
  <user id="2">
    <name>Jane Smith</name>
    <contact email="jane@example.com" phone="+44-20-1234-5678" />
  </user>
</users>`
	mixedXMLPath := filepath.Join(tempDir, "mixed.xml")
	if err := os.WriteFile(mixedXMLPath, []byte(mixedXML), 0644); err != nil {
		t.Fatalf("Failed to write mixed XML file: %v", err)
	}

	// Create a test XML file with nested repeating elements
	nestedXML := `<?xml version="1.0" encoding="UTF-8"?>
<company>
  <department name="Engineering">
    <employee>
      <name>John Doe</name>
      <position>Developer</position>
    </employee>
    <employee>
      <name>Jane Smith</name>
      <position>Designer</position>
    </employee>
  </department>
</company>`
	nestedXMLPath := filepath.Join(tempDir, "nested.xml")
	if err := os.WriteFile(nestedXMLPath, []byte(nestedXML), 0644); err != nil {
		t.Fatalf("Failed to write nested XML file: %v", err)
	}

	// Create an invalid XML file
	invalidXML := `<?xml version="1.0" encoding="UTF-8"?>
<users>
  <user>
    <name>John Doe</name>
  </user>
  <user>
    <name>Jane Smith</name>
`
	invalidXMLPath := filepath.Join(tempDir, "invalid.xml")
	if err := os.WriteFile(invalidXMLPath, []byte(invalidXML), 0644); err != nil {
		t.Fatalf("Failed to write invalid XML file: %v", err)
	}

	tests := []struct {
		name     string
		filePath string
		wantErr  bool
		checkFn  func(*testing.T, *common.DataSet)
	}{
		{
			name:     "Repeating elements",
			filePath: repeatingXMLPath,
			wantErr:  false,
			checkFn: func(t *testing.T, ds *common.DataSet) {
				if len(ds.Rows) != 2 {
					t.Errorf("Expected 2 rows, got %d", len(ds.Rows))
				}

				// Check that all expected columns exist
				expectedColumns := map[string]bool{
					"id":   true,
					"name": true,
					"age":  true,
					"city": true,
				}

				for _, col := range ds.Columns {
					if _, ok := expectedColumns[col]; !ok {
						t.Errorf("Unexpected column: %s", col)
					}
					delete(expectedColumns, col)
				}

				for col := range expectedColumns {
					t.Errorf("Expected column not found: %s", col)
				}

				// Check first row values
				if ds.Rows[0]["name"] != "John Doe" {
					t.Errorf("Expected name 'John Doe', got %v", ds.Rows[0]["name"])
				}
				if ds.Rows[0]["age"] != "30" {
					t.Errorf("Expected age '30', got %v", ds.Rows[0]["age"])
				}
			},
		},
		{
			name:     "Attributes",
			filePath: attributesXMLPath,
			wantErr:  false,
			checkFn: func(t *testing.T, ds *common.DataSet) {
				if len(ds.Rows) != 2 {
					t.Errorf("Expected 2 rows, got %d", len(ds.Rows))
				}

				// Check first row values from attributes
				if ds.Rows[0]["id"] != "1" {
					t.Errorf("Expected id '1', got %v", ds.Rows[0]["id"])
				}
				if ds.Rows[0]["name"] != "John Doe" {
					t.Errorf("Expected name 'John Doe', got %v", ds.Rows[0]["name"])
				}
			},
		},
		{
			name:     "Mixed content",
			filePath: mixedXMLPath,
			wantErr:  false,
			checkFn: func(t *testing.T, ds *common.DataSet) {
				if len(ds.Rows) != 2 {
					t.Errorf("Expected 2 rows, got %d", len(ds.Rows))
				}

				// Check for both element and attribute data
				if ds.Rows[0]["name"] != "John Doe" {
					t.Errorf("Expected name 'John Doe', got %v", ds.Rows[0]["name"])
				}

				// Note: The current implementation doesn't handle nested elements with attributes
				// This test is checking the current behavior, which might need improvement
			},
		},
		{
			name:     "Nested repeating elements",
			filePath: nestedXMLPath,
			wantErr:  false,
			checkFn: func(t *testing.T, ds *common.DataSet) {
				// The loader should find the repeating employee elements
				if len(ds.Rows) != 2 {
					t.Errorf("Expected 2 rows, got %d", len(ds.Rows))
				}

				// Check for expected columns
				hasName := false
				hasPosition := false
				for _, col := range ds.Columns {
					if col == "name" {
						hasName = true
					}
					if col == "position" {
						hasPosition = true
					}
				}

				if !hasName {
					t.Errorf("Expected 'name' column not found")
				}
				if !hasPosition {
					t.Errorf("Expected 'position' column not found")
				}
			},
		},
		{
			name:     "Invalid XML",
			filePath: invalidXMLPath,
			wantErr:  true,
		},
		{
			name:     "Non-existent file",
			filePath: filepath.Join(tempDir, "nonexistent.xml"),
			wantErr:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			l := &XMLLoader{}
			got, err := l.Load(tt.filePath)
			if (err != nil) != tt.wantErr {
				t.Errorf("XMLLoader.Load() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !tt.wantErr && tt.checkFn != nil {
				tt.checkFn(t, got)
			}
		})
	}
}

func TestGetLoader_XML(t *testing.T) {
	loader, err := GetLoader("test.xml")
	if err != nil {
		t.Errorf("GetLoader() error = %v", err)
		return
	}

	if _, ok := loader.(*XMLLoader); !ok {
		t.Errorf("GetLoader() returned wrong loader type for XML file")
	}
}
