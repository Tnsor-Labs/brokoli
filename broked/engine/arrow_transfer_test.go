package engine

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/hc12r/brokolisql-go/pkg/common"
)

func TestWriteReadArrowJSON(t *testing.T) {
	ds := &common.DataSet{
		Columns: []string{"id", "name", "value"},
		Rows: []common.DataRow{
			{"id": 1.0, "name": "Alice", "value": 100.5},
			{"id": 2.0, "name": "Bob", "value": 200.0},
			{"id": 3.0, "name": "Charlie", "value": nil},
		},
	}

	tmpFile := filepath.Join(os.TempDir(), "test_arrow.ndjson")
	defer os.Remove(tmpFile)

	if err := WriteArrowJSON(tmpFile, ds); err != nil {
		t.Fatalf("write error: %v", err)
	}

	result, err := ReadArrowJSON(tmpFile)
	if err != nil {
		t.Fatalf("read error: %v", err)
	}

	if len(result.Rows) != 3 {
		t.Errorf("expected 3 rows, got %d", len(result.Rows))
	}
	if result.Rows[0]["name"] != "Alice" {
		t.Errorf("expected Alice, got %v", result.Rows[0]["name"])
	}
}

func TestWriteReadArrowJSON_Empty(t *testing.T) {
	ds := &common.DataSet{Columns: []string{}, Rows: []common.DataRow{}}

	tmpFile := filepath.Join(os.TempDir(), "test_arrow_empty.ndjson")
	defer os.Remove(tmpFile)

	if err := WriteArrowJSON(tmpFile, ds); err != nil {
		t.Fatalf("write error: %v", err)
	}

	result, err := ReadArrowJSON(tmpFile)
	if err != nil {
		t.Fatalf("read error: %v", err)
	}

	if len(result.Rows) != 0 {
		t.Errorf("expected 0 rows, got %d", len(result.Rows))
	}
}

func TestWriteReadArrowJSON_Nil(t *testing.T) {
	tmpFile := filepath.Join(os.TempDir(), "nil.ndjson")
	defer os.Remove(tmpFile)

	if err := WriteArrowJSON(tmpFile, nil); err != nil {
		t.Fatalf("write nil error: %v", err)
	}
}

func TestWriteReadColumnarBinary(t *testing.T) {
	ds := &common.DataSet{
		Columns: []string{"x", "y"},
		Rows: []common.DataRow{
			{"x": 1.0, "y": "a"},
			{"x": 2.0, "y": "b"},
		},
	}

	tmpFile := filepath.Join(os.TempDir(), "test_brok.bin")
	defer os.Remove(tmpFile)

	if err := WriteColumnarBinary(tmpFile, ds); err != nil {
		t.Fatalf("write error: %v", err)
	}

	result, err := ReadColumnarBinary(tmpFile)
	if err != nil {
		t.Fatalf("read error: %v", err)
	}

	if len(result.Rows) != 2 {
		t.Errorf("expected 2 rows, got %d", len(result.Rows))
	}
	if len(result.Columns) != 2 {
		t.Errorf("expected 2 columns, got %d", len(result.Columns))
	}
}

func TestWriteReadColumnarBinary_Empty(t *testing.T) {
	tmpFile := filepath.Join(os.TempDir(), "test_brok_empty.bin")
	defer os.Remove(tmpFile)

	if err := WriteColumnarBinary(tmpFile, nil); err != nil {
		t.Fatalf("write error: %v", err)
	}

	result, err := ReadColumnarBinary(tmpFile)
	if err != nil {
		t.Fatalf("read error: %v", err)
	}

	if len(result.Rows) != 0 {
		t.Errorf("expected 0 rows, got %d", len(result.Rows))
	}
}

func TestWriteReadArrowJSON_LargeDataset(t *testing.T) {
	// Generate 10K rows
	rows := make([]common.DataRow, 10000)
	for i := range rows {
		rows[i] = common.DataRow{
			"id":    float64(i),
			"name":  "user_" + string(rune('a'+i%26)),
			"score": float64(i) * 1.5,
		}
	}
	ds := &common.DataSet{
		Columns: []string{"id", "name", "score"},
		Rows:    rows,
	}

	tmpFile := filepath.Join(os.TempDir(), "test_arrow_large.ndjson")
	defer os.Remove(tmpFile)

	if err := WriteArrowJSON(tmpFile, ds); err != nil {
		t.Fatalf("write error: %v", err)
	}

	result, err := ReadArrowJSON(tmpFile)
	if err != nil {
		t.Fatalf("read error: %v", err)
	}

	if len(result.Rows) != 10000 {
		t.Errorf("expected 10000 rows, got %d", len(result.Rows))
	}
}

func TestReadColumnarBinary_InvalidMagic(t *testing.T) {
	tmpFile := filepath.Join(os.TempDir(), "test_brok_invalid.bin")
	defer os.Remove(tmpFile)

	// Write invalid data
	os.WriteFile(tmpFile, []byte("XXXX12345678"), 0o644)

	_, err := ReadColumnarBinary(tmpFile)
	if err == nil {
		t.Error("expected error for invalid magic bytes")
	}
}
