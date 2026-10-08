package loaders

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xuri/excelize/v2"
)

// A legacy binary workbook (.xls) used to be handed to the Excel loader,
// which reads .xlsx only and failed with "unsupported workbook file
// format". It is now refused with a message that says what to do.
func TestLegacyExcelWorkbookIsRefusedClearly(t *testing.T) {
	path := filepath.Join(t.TempDir(), "old.xls")
	// The OLE compound-document signature every legacy workbook starts with.
	content := append([]byte{0xD0, 0xCF, 0x11, 0xE0, 0xA1, 0xB1, 0x1A, 0xE1}, make([]byte, 504)...)
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := GetLoader(path)
	if !errors.Is(err, ErrLegacyExcel) {
		t.Fatalf("GetLoader(.xls) error = %v, want ErrLegacyExcel", err)
	}
	if !strings.Contains(err.Error(), "save it as .xlsx") {
		t.Fatalf("the refusal does not say what to do: %v", err)
	}
}

// Some exporters write an .xlsx workbook under an .xls name. That is
// decided by the file's content, so it reads normally.
func TestXlsxWorkbookNamedXlsStillLoads(t *testing.T) {
	path := filepath.Join(t.TempDir(), "export.xls")
	f := excelize.NewFile()
	_ = f.SetCellValue("Sheet1", "A1", "id")
	_ = f.SetCellValue("Sheet1", "B1", "name")
	_ = f.SetCellValue("Sheet1", "A2", 1)
	_ = f.SetCellValue("Sheet1", "B2", "alpha")
	if err := f.SaveAs(path + ".xlsx"); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(path+".xlsx", path); err != nil {
		t.Fatal(err)
	}
	loader, err := GetLoader(path)
	if err != nil {
		t.Fatalf("GetLoader: %v", err)
	}
	ds, err := loader.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(ds.Rows) != 1 || ds.Rows[0]["name"] != "alpha" {
		t.Fatalf("rows = %v", ds.Rows)
	}
}

// An unsupported or missing extension names the formats that are.
func TestUnsupportedFormatNamesTheSupportedOnes(t *testing.T) {
	for _, path := range []string{"notes.txt", "noext"} {
		_, err := GetLoader(path)
		if err == nil || !strings.Contains(err.Error(), ".csv, .json, .xml and .xlsx") {
			t.Errorf("GetLoader(%q) error = %v", path, err)
		}
	}
}
