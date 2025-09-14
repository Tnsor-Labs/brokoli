package loaders

import (
	"fmt"
	"strings"

	"github.com/hc12r/brokolisql-go/pkg/common"

	"github.com/xuri/excelize/v2"
)

type ExcelLoader struct{}

func (l *ExcelLoader) Load(filePath string) (*common.DataSet, error) {
	// Validate file path using safe file operations
	_, err := common.SafeOpenFile(filePath)
	if err != nil {
		return nil, fmt.Errorf("failed to safely access Excel file: %w", err)
	}

	// Now use the excelize library to open the Excel file
	file, err := excelize.OpenFile(filePath)
	if err != nil {
		return nil, fmt.Errorf("failed to open Excel file: %w", err)
	}
	defer func(file *excelize.File) {
		err := file.Close()
		if err != nil {
			common.DefaultLogger.Warning("Failed to close Excel file: %v", err)
		}
	}(file)

	sheets := file.GetSheetList()
	if len(sheets) == 0 {
		return nil, fmt.Errorf("no sheets found in Excel file")
	}

	sheetName := sheets[0]

	rows, err := file.GetRows(sheetName)
	if err != nil {
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
