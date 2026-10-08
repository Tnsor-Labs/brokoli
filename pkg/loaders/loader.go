package loaders

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/Tnsor-Labs/brokoli/pkg/common"
)

type Loader interface {
	Load(filePath string) (*common.DataSet, error)
}

// ErrLegacyExcel is returned for a workbook in the binary Excel format used
// before Excel 2007 (.xls). The Excel loader reads the Office Open XML
// format (.xlsx) only; it used to be handed .xls files anyway and failed
// with "unsupported workbook file format", which did not say what to do.
var ErrLegacyExcel = errors.New("this is a legacy Excel workbook (the binary .xls format used before Excel 2007, or a password-protected workbook), which cannot be read: open it in Excel or LibreOffice and save it as .xlsx without a password")

// oleMagic is how a legacy binary workbook starts: it is an OLE compound
// document, where an .xlsx workbook is a zip archive.
var oleMagic = []byte{0xD0, 0xCF, 0x11, 0xE0, 0xA1, 0xB1, 0x1A, 0xE1}

// GetLoader picks the loader for filePath by its extension, ignoring case
// (DATA.CSV and Report.XLSX are ordinary file names; the streaming path
// already matched without case, and this did not).
func GetLoader(filePath string) (Loader, error) {
	ext := strings.ToLower(filepath.Ext(filePath))

	switch ext {
	case ".csv":
		return &CSVLoader{}, nil
	case ".json":
		return &JSONLoader{}, nil
	case ".xml":
		return &XMLLoader{}, nil
	case ".xlsx":
		return &ExcelLoader{}, nil
	case ".xls":
		// Decided by content, not name: some exporters write an .xlsx
		// workbook under an .xls name, and that one reads fine.
		if isLegacyExcel(filePath) {
			return nil, ErrLegacyExcel
		}
		return &ExcelLoader{}, nil
	default:
		if ext == "" {
			return nil, fmt.Errorf("file %q has no extension; supported formats are .csv, .json, .xml and .xlsx", filepath.Base(filePath))
		}
		return nil, fmt.Errorf("unsupported file format %s; supported formats are .csv, .json, .xml and .xlsx", ext)
	}
}

// isLegacyExcel reports whether the file starts with the OLE signature of a
// binary workbook. Anything else, including a file that cannot be read, goes
// to the Excel loader, which reports its own error.
func isLegacyExcel(filePath string) bool {
	f, err := os.Open(filePath) // #nosec G304 -- a data-directory path the caller has already validated
	if err != nil {
		return false
	}
	defer f.Close()
	head := make([]byte, len(oleMagic))
	if _, err := io.ReadFull(f, head); err != nil {
		return false
	}
	return bytes.Equal(head, oleMagic)
}
