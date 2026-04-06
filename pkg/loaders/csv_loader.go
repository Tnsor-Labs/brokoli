package loaders

import (
	"encoding/csv"
	"fmt"
	"os"
	"strings"

	"github.com/Tnsor-Labs/brokoli/pkg/common"
)

type CSVLoader struct{}

func (l *CSVLoader) Load(filePath string) (*common.DataSet, error) {
	file, err := common.SafeOpenFile(filePath)
	if err != nil {
		return nil, fmt.Errorf("failed to open CSV file: %w", err)
	}
	defer func(file *os.File) {
		err := file.Close()
		if err != nil {
			common.DefaultLogger.Warning("Failed to close CSV file: %v", err)
		}
	}(file)

	reader := csv.NewReader(file)

	headers, err := reader.Read()
	if err != nil {
		return nil, fmt.Errorf("failed to read CSV headers: %w", err)
	}

	for i, header := range headers {
		headers[i] = strings.TrimSpace(header)
	}

	records, err := reader.ReadAll()
	if err != nil {
		return nil, fmt.Errorf("failed to read CSV data: %w", err)
	}

	rows := make([]common.DataRow, 0, len(records))
	for _, record := range records {
		row := make(common.DataRow)
		for i, value := range record {
			if i < len(headers) {
				row[headers[i]] = value
			}
		}
		rows = append(rows, row)
	}

	return &common.DataSet{
		Columns: headers,
		Rows:    rows,
	}, nil
}
