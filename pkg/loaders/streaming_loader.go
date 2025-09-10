package loaders

import (
	"brokolisql-go/pkg/common"
	"brokolisql-go/pkg/errors"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
)

// RowChannel is a channel for streaming rows of data
type RowChannel chan common.DataRow

// StreamingLoader defines an interface for loaders that support streaming
type StreamingLoader interface {
	// StreamLoad starts loading data from the file and returns channels for receiving rows and errors
	// The columns slice contains the column names
	// The rows channel will receive rows of data
	// The done channel will be closed when all rows have been processed
	// The error channel will receive any errors that occur during loading
	StreamLoad(filePath string) (columns []string, rows RowChannel, done chan struct{}, err error)
}

// StreamingJSONLoader implements the StreamingLoader interface for JSON files
type StreamingJSONLoader struct{}

// StreamLoad implements the StreamingLoader interface for JSON files
func (l *StreamingJSONLoader) StreamLoad(filePath string) ([]string, RowChannel, chan struct{}, error) {
	file, err := os.Open(filePath)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("failed to open JSON file: %w", err)
	}

	// Create channels
	rowsChan := make(RowChannel, 100) // Buffer size of 100
	doneChan := make(chan struct{})

	// Start a goroutine to read and process the file
	go func() {
		defer file.Close()
		defer close(rowsChan)
		defer close(doneChan)

		// Create a JSON decoder
		decoder := json.NewDecoder(file)

		// Check if the JSON is an array
		token, err := decoder.Token()
		if err != nil {
			rowsChan <- nil // Signal error
			return
		}

		// If it's not an array, rewind and try to read a single object
		isArray := token == json.Delim('[')
		if !isArray {
			// Rewind the file and create a new decoder
			_, err := file.Seek(0, 0)
			if err != nil {
				return
			}
			decoder = json.NewDecoder(file)
		}

		// Process JSON data
		columnSet := make(map[string]bool)

		if isArray {
			// Process array of objects
			for decoder.More() {
				var obj map[string]interface{}
				if err := decoder.Decode(&obj); err != nil {
					rowsChan <- nil // Signal error
					return
				}

				// Update column set
				for key := range obj {
					columnSet[key] = true
				}

				// Process the row
				row := processJSONRow(obj)
				rowsChan <- row
			}
		} else {
			// Process single object
			var obj map[string]interface{}
			if err := decoder.Decode(&obj); err != nil {
				rowsChan <- nil // Signal error
				return
			}

			// Update column set
			for key := range obj {
				columnSet[key] = true
			}

			// Process the row
			row := processJSONRow(obj)
			rowsChan <- row
		}
	}()

	// Eish! We need to read the first few rows to determine the columns
	// This is a limitation of the current design that we'll need to address in a future version
	// For now, we'll read a small portion of the file to determine the columns
	tempFile, err := os.Open(filePath)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("failed to open JSON file for column detection: %w", err)
	}
	defer tempFile.Close()

	// Read up to 1MB to detect columns
	data := make([]byte, 1024*1024)
	n, _ := tempFile.Read(data)
	data = data[:n]

	// Parse the data to detect columns
	var tempData []map[string]interface{}
	err = json.Unmarshal(data, &tempData)

	// If it's not an array, try a single object
	if err != nil || len(tempData) == 0 {
		var singleObject map[string]interface{}
		err = json.Unmarshal(data, &singleObject)
		if err != nil {
			return nil, nil, nil, fmt.Errorf("failed to parse JSON for column detection: %w", err)
		}
		tempData = []map[string]interface{}{singleObject}
	}

	// Extract columns
	columnSet := make(map[string]bool)
	for _, obj := range tempData {
		for key := range obj {
			columnSet[key] = true
		}
	}

	columns := make([]string, 0, len(columnSet))
	for col := range columnSet {
		columns = append(columns, col)
	}

	return columns, rowsChan, doneChan, nil
}

// processJSONRow processes a JSON object into a DataRow
func processJSONRow(obj map[string]interface{}) common.DataRow {
	row := make(common.DataRow)
	for key, value := range obj {
		if common.IsComplex(value) {
			jsonBytes, err := json.Marshal(value)
			if err == nil {
				row[key] = string(jsonBytes)
			} else {
				row[key] = fmt.Sprintf("%v", value)
			}
		} else {
			row[key] = value
		}
	}
	return row
}

// StreamingCSVLoader implements the StreamingLoader interface for CSV files
type StreamingCSVLoader struct{}

// StreamLoad implements the StreamingLoader interface for CSV files
func (l *StreamingCSVLoader) StreamLoad(filePath string) ([]string, RowChannel, chan struct{}, error) {
	file, err := os.Open(filePath)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("failed to open CSV file: %w", err)
	}

	reader := csv.NewReader(file)

	// Read headers
	headers, err := reader.Read()
	if err != nil {
		file.Close()
		return nil, nil, nil, fmt.Errorf("failed to read CSV headers: %w", err)
	}

	// Trim headers
	for i, header := range headers {
		headers[i] = strings.TrimSpace(header)
	}

	// Create channels
	rowsChan := make(RowChannel, 100) // Buffer size of 100
	doneChan := make(chan struct{})

	// Start a goroutine to read and process the file
	go func() {
		defer file.Close()
		defer close(rowsChan)
		defer close(doneChan)

		for {
			record, err := reader.Read()
			if err == io.EOF {
				break
			}
			if err != nil {
				rowsChan <- nil // Signal error
				return
			}

			row := make(common.DataRow)
			for i, value := range record {
				if i < len(headers) {
					row[headers[i]] = value
				}
			}
			rowsChan <- row
		}
	}()

	return headers, rowsChan, doneChan, nil
}

// GetStreamingLoader returns a StreamingLoader for the given file path
func GetStreamingLoader(filePath string) (StreamingLoader, error) {
	loader, err := GetLoader(filePath)
	if err != nil {
		return nil, err
	}

	switch loader.(type) {
	case *JSONLoader:
		return &StreamingJSONLoader{}, nil
	case *CSVLoader:
		return &StreamingCSVLoader{}, nil
	default:
		// For now, we only support streaming for JSON and CSV
		// Other formats will be added in future phases
		return nil, ErrStreamingNotSupported
	}
}

// ErrStreamingNotSupported is returned when streaming is not supported for a file format
var ErrStreamingNotSupported = errors.NewInputError("streaming not supported for this file format", nil)
