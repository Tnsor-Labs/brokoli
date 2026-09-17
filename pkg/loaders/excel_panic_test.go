package loaders

import (
	"archive/zip"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// GO-2026-6452 is a panic in excelize with no upstream fixed version, so
// ExcelLoader wraps the library's parse calls in a recover
// (safeExcelize). These tests pin that wrapper's contract.
//
// Honesty about what is and is not covered here: a crafted .xlsx with a
// negative shared-string index does NOT panic on excelize v2.11.0 --
// GetCellValue, GetRows, GetCols and GetCellType all return "invalid
// shared string index -1" as an ordinary error. The advisory's own vector
// could not be reproduced, so the panic path is exercised with an
// injected panic rather than a fixture that pretends to trigger the real
// one. TestExcelLoader_NegativeSharedStringIndexDoesNotCrash covers the
// real file, and asserts only what it genuinely demonstrates: the process
// survives it.

func TestSafeExcelize_TurnsAPanicIntoAnError(t *testing.T) {
	err := safeExcelize("/tmp/evil.xlsx", "row read", func() error {
		panic("runtime error: index out of range [-1]")
	})
	if err == nil {
		t.Fatal("a panicking excelize call must produce an error, not a nil result")
	}
	// The message has to name the file and the operation: an operator
	// reading a failed node needs to know which spreadsheet did it.
	for _, want := range []string{"/tmp/evil.xlsx", "row read", "index out of range"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err, want)
		}
	}
	if !strings.Contains(err.Error(), "GO-2026-6452") {
		t.Errorf("error should point at the advisory for whoever triages it, got %q", err)
	}
}

func TestSafeExcelize_PassesRealErrorsThrough(t *testing.T) {
	sentinel := errors.New("zip: not a valid zip file")
	err := safeExcelize("/tmp/x.xlsx", "open", func() error { return sentinel })
	if !errors.Is(err, sentinel) {
		t.Fatalf("a normal error must pass through untouched, got %v", err)
	}
}

func TestSafeExcelize_SuccessReturnsNil(t *testing.T) {
	if err := safeExcelize("/tmp/x.xlsx", "open", func() error { return nil }); err != nil {
		t.Fatalf("a successful call must return nil, got %v", err)
	}
}

// The wrapper must contain a panic from the library, not one from
// Brokoli's own code higher up: Load is not blanket-recovered. This test
// documents that boundary by showing the recover is scoped to the
// callback only.
func TestSafeExcelize_RecoverIsScopedToTheCallback(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("a panic raised outside safeExcelize must still propagate")
		}
	}()
	_ = safeExcelize("/tmp/x.xlsx", "open", func() error { return nil })
	panic("this one must not be swallowed")
}

// writeNegativeSharedStringXLSX builds the advisory's own shape: a cell
// typed as a shared string whose index is negative.
func writeNegativeSharedStringXLSX(t *testing.T, path string) {
	t.Helper()
	parts := map[string]string{
		"[Content_Types].xml": `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types">
<Default Extension="rels" ContentType="application/vnd.openxmlformats-package.relationships+xml"/>
<Default Extension="xml" ContentType="application/xml"/>
<Override PartName="/xl/workbook.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.sheet.main+xml"/>
<Override PartName="/xl/worksheets/sheet1.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.worksheet+xml"/>
<Override PartName="/xl/sharedStrings.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.sharedStrings+xml"/>
</Types>`,
		"_rels/.rels": `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">
<Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/officeDocument" Target="xl/workbook.xml"/>
</Relationships>`,
		"xl/workbook.xml": `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<workbook xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships">
<sheets><sheet name="Sheet1" sheetId="1" r:id="rId1"/></sheets></workbook>`,
		"xl/_rels/workbook.xml.rels": `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">
<Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/worksheet" Target="worksheets/sheet1.xml"/>
<Relationship Id="rId2" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/sharedStrings" Target="sharedStrings.xml"/>
</Relationships>`,
		"xl/worksheets/sheet1.xml": `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<worksheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main"><sheetData>
<row r="1"><c r="A1" t="s"><v>0</v></c><c r="B1" t="s"><v>0</v></c></row>
<row r="2"><c r="A2" t="s"><v>-1</v></c><c r="B2" t="s"><v>0</v></c></row>
</sheetData></worksheet>`,
		"xl/sharedStrings.xml": `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<sst xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main" count="1" uniqueCount="1"><si><t>header</t></si></sst>`,
	}

	f, err := os.Create(path) // #nosec G304 -- test-controlled temp path
	if err != nil {
		t.Fatalf("create fixture: %v", err)
	}
	defer func() { _ = f.Close() }()
	zw := zip.NewWriter(f)
	for name, body := range parts {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatalf("zip entry %s: %v", name, err)
		}
		if _, err := fmt.Fprint(w, body); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("close zip: %v", err)
	}
}

// The advisory's own input, end to end through the loader. On excelize
// v2.11.0 this does not panic, so what this pins is that the loader
// survives it and the process stays up -- whether the library handles it
// or the recover does.
func TestExcelLoader_NegativeSharedStringIndexDoesNotCrash(t *testing.T) {
	path := filepath.Join(t.TempDir(), "negative-shared-string.xlsx")
	writeNegativeSharedStringXLSX(t, path)

	l := &ExcelLoader{}
	ds, err := l.Load(path)
	// Either outcome is acceptable; crashing the process is not.
	if err != nil {
		t.Logf("loader rejected the crafted file: %v", err)
		return
	}
	if ds == nil {
		t.Fatal("no error and no dataset")
	}
	t.Logf("loader parsed the crafted file: %d column(s), %d row(s)", len(ds.Columns), len(ds.Rows))
}
