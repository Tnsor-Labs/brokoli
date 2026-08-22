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
				if value == "" {
					// An empty CSV field carries no type information —
					// "" and "missing" are the same three characters of
					// nothing in the file — so it becomes NULL here,
					// where the ambiguity actually lives. The SQL writer
					// used to make this call instead, by turning every
					// empty string into NULL regardless of where it came
					// from, which also silently destroyed genuinely
					// empty strings arriving from a database.
					row[headers[i]] = nil
					continue
				}
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
