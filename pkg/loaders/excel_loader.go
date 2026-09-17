package loaders

import (
	"fmt"
	"strings"

	"github.com/Tnsor-Labs/brokoli/pkg/common"

	"github.com/xuri/excelize/v2"
)

type ExcelLoader struct{}

// safeExcelize runs one excelize call and converts a panic into an error.
//
// GO-2026-6452 (panic via a negative shared-string index) affects every
// released version of excelize and has no upstream fix, so this cannot be
// closed by upgrading the way its sibling GO-2026-6453 was. The advisory's
// impact is a panic, and this loader runs inside the engine process that
// hosts every other run on the node, so an attacker-supplied or merely
// corrupt .xlsx would take all of them down rather than failing its own
// node.
//
// The recover is deliberately wrapped around individual excelize calls
// rather than around Load as a whole: a panic from Brokoli's own code is a
// bug that should keep crashing loudly, and only the third-party parse is
// being contained. Errors excelize returns normally are passed through
// untouched, so nothing real is swallowed.
//
// See security/vuln-allowlist.json, which records this as the mitigation
// for the allowlisted advisory; scripts/check-vulns.sh fails the build if
// a fixed version is ever published, at which point this can go.
func safeExcelize(filePath, op string, fn func() error) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("excel file %s could not be parsed: the spreadsheet library panicked during %s (%v); "+
				"this usually means the file is corrupt or crafted (see GO-2026-6452)", filePath, op, r)
		}
	}()
	return fn()
}

func (l *ExcelLoader) Load(filePath string) (*common.DataSet, error) {
	// Validate file path using safe file operations
	_, err := common.SafeOpenFile(filePath)
	if err != nil {
		return nil, fmt.Errorf("failed to safely access Excel file: %w", err)
	}

	// Now use the excelize library to open the Excel file
	var file *excelize.File
	if err := safeExcelize(filePath, "open", func() error {
		var openErr error
		file, openErr = excelize.OpenFile(filePath)
		return openErr
	}); err != nil {
		return nil, fmt.Errorf("failed to open Excel file: %w", err)
	}
	defer func(file *excelize.File) {
		err := file.Close()
		if err != nil {
			common.DefaultLogger.Warning("Failed to close Excel file: %v", err)
		}
	}(file)

	var sheets []string
	if err := safeExcelize(filePath, "sheet listing", func() error {
		sheets = file.GetSheetList()
		return nil
	}); err != nil {
		return nil, err
	}
	if len(sheets) == 0 {
		return nil, fmt.Errorf("no sheets found in Excel file")
	}

	sheetName := sheets[0]

	var rows [][]string
	if err := safeExcelize(filePath, "row read", func() error {
		var rowsErr error
		rows, rowsErr = file.GetRows(sheetName)
		return rowsErr
	}); err != nil {
		return nil, fmt.Errorf("failed to read rows from Excel sheet: %w", err)
	}

	if len(rows) < 2 {
		return nil, fmt.Errorf("excel file must contain at least a header row and one data row")
	}

	headers := rows[0]

	for i, header := range headers {
		headers[i] = strings.TrimSpace(header)
	}

	dataRows := make([]common.DataRow, 0, len(rows)-1)
	for _, row := range rows[1:] {
		dataRow := make(common.DataRow)
		for i, value := range row {
			if i < len(headers) && headers[i] != "" {
				dataRow[headers[i]] = value
			}
		}
		dataRows = append(dataRows, dataRow)
	}

	return &common.DataSet{
		Columns: headers,
		Rows:    dataRows,
	}, nil
}
