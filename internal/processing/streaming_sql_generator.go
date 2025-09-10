package processing

import (
	"brokolisql-go/internal/dialects"
	"brokolisql-go/pkg/common"
	"brokolisql-go/pkg/loaders"
	"fmt"
	"os"
	"sync"
)

// StreamingSQLGeneratorOptions contains options for the streaming SQL generator
type StreamingSQLGeneratorOptions struct {
	SQLGeneratorOptions
	OutputFile string
	BufferSize int
}

// StreamingSQLGenerator generates SQL statements incrementally from streaming data
type StreamingSQLGenerator struct {
	options     StreamingSQLGeneratorOptions
	normalizer  *Normalizer
	typeInferer *TypeInferenceEngine
	dialect     dialects.Dialect
	file        *os.File
	mu          sync.Mutex
	columns     []string
	buffer      [][]interface{}
	rowCount    int
}

// NewStreamingSQLGenerator creates a new streaming SQL generator
func NewStreamingSQLGenerator(options StreamingSQLGeneratorOptions) (*StreamingSQLGenerator, error) {
	if options.TableName == "" {
		options.TableName = "data"
	}

	if options.BatchSize <= 0 {
		options.BatchSize = 100
	}

	if options.BufferSize <= 0 {
		options.BufferSize = options.BatchSize
	}

	dialect, err := dialects.GetDialect(options.Dialect)
	if err != nil {
		return nil, err
	}

	// Open output file
	file, err := os.Create(options.OutputFile)
	if err != nil {
		return nil, fmt.Errorf("failed to create output file: %w", err)
	}

	return &StreamingSQLGenerator{
		options:     options,
		normalizer:  NewNormalizer(),
		typeInferer: NewTypeInferenceEngine(),
		dialect:     dialect,
		file:        file,
		buffer:      make([][]interface{}, 0, options.BufferSize),
	}, nil
}

// ProcessStream processes a stream of data and generates SQL incrementally
func (g *StreamingSQLGenerator) ProcessStream(filePath string) error {
	// Get streaming loader
	loader, err := loaders.GetStreamingLoader(filePath)
	if err != nil {
		return fmt.Errorf("failed to get streaming loader: %w", err)
	}

	// Start streaming
	columns, rowsChan, doneChan, err := loader.StreamLoad(filePath)
	if err != nil {
		return fmt.Errorf("failed to start streaming: %w", err)
	}

	// Normalize column names if needed
	if g.options.NormalizeColumns {
		g.columns = g.normalizer.NormalizeColumnNames(columns)
	} else {
		g.columns = columns
	}

	// Collect a sample of rows for type inference and nested object detection
	sampleRows := make([]common.DataRow, 0, 100)
	sampleCount := 0

	// Create a separate channel for sampling
	sampleDone := make(chan struct{})

	go func() {
		defer close(sampleDone)

		for row := range rowsChan {
			if row == nil {
				continue
			}

			// Process the row
			if g.options.NormalizeColumns {
				normalizedRow := make(common.DataRow)
				for i, col := range columns {
					normalizedCol := g.columns[i]
					normalizedRow[normalizedCol] = row[col]
				}
				sampleRows = append(sampleRows, normalizedRow)
			} else {
				sampleRows = append(sampleRows, row)
			}

			sampleCount++
			if sampleCount >= 100 {
				break
			}
		}
	}()

	// Wait for sampling to complete
	<-sampleDone

	// Check if we have nested objects in the sample
	hasNestedObjects := g.hasNestedObjects(sampleRows)

	// If we have nested objects, use the NestedJSONProcessor for the entire file
	if hasNestedObjects {
		// Close the current streaming channels
		<-doneChan

		// Close the output file since we'll reopen it
		g.file.Close()

		// Load the entire file using the regular loader
		jsonLoader := &loaders.JSONLoader{}
		dataset, err := jsonLoader.Load(filePath)
		if err != nil {
			return fmt.Errorf("failed to load file for nested object processing: %w", err)
		}

		// Use the nested JSON processor
		processor, err := NewNestedJSONProcessor(g.options.SQLGeneratorOptions)
		if err != nil {
			return fmt.Errorf("failed to create nested JSON processor: %w", err)
		}

		// Process the dataset
		sql, err := processor.ProcessDataSet(dataset)
		if err != nil {
			return fmt.Errorf("failed to process nested JSON: %w", err)
		}

		// Write the SQL to the output file
		file, err := os.Create(g.options.OutputFile)
		if err != nil {
			return fmt.Errorf("failed to create output file: %w", err)
		}
		defer file.Close()

		if _, err := file.WriteString(sql); err != nil {
			return fmt.Errorf("failed to write SQL to output file: %w", err)
		}

		return nil
	}

	// Continue with regular streaming for flat data
	// Write CREATE TABLE statement if needed
	if g.options.CreateTable {
		// Infer column types from sample
		columnTypes := g.typeInferer.InferColumnTypes(g.columns, sampleRows)

		// Create column definitions
		columnDefs := make([]dialects.ColumnDef, len(g.columns))
		for i, col := range g.columns {
			columnDefs[i] = dialects.ColumnDef{
				Name:     col,
				Type:     columnTypes[col],
				Nullable: true, // Default to nullable
			}
		}

		// Generate CREATE TABLE statement
		createTableSQL := g.dialect.CreateTable(g.options.TableName, columnDefs)

		// Write to file
		if _, err := g.file.WriteString(createTableSQL + "\n"); err != nil {
			return fmt.Errorf("failed to write CREATE TABLE statement: %w", err)
		}
	}

	// Restart streaming since we consumed some rows for sampling
	// This is needed regardless of whether we're creating a table or not
	columns, rowsChan, doneChan, err = loader.StreamLoad(filePath)
	if err != nil {
		return fmt.Errorf("failed to restart streaming: %w", err)
	}

	// Process rows
	for row := range rowsChan {
		if row == nil {
			continue
		}

		// Process the row
		if g.options.NormalizeColumns {
			normalizedRow := make(common.DataRow)
			for i, col := range columns {
				normalizedCol := g.columns[i]
				normalizedRow[normalizedCol] = row[col]
			}
			row = normalizedRow
		}

		// Extract values in column order
		values := make([]interface{}, len(g.columns))
		for i, col := range g.columns {
			values[i] = row[col]
		}

		// Add to buffer
		g.buffer = append(g.buffer, values)
		g.rowCount++

		// Flush buffer if it's full
		if len(g.buffer) >= g.options.BufferSize {
			if err := g.flushBuffer(); err != nil {
				return fmt.Errorf("failed to flush buffer: %w", err)
			}
		}
	}

	// Wait for done signal
	<-doneChan

	// Flush any remaining rows
	if len(g.buffer) > 0 {
		if err := g.flushBuffer(); err != nil {
			return fmt.Errorf("failed to flush final buffer: %w", err)
		}
	}

	// Close output file
	if err := g.file.Close(); err != nil {
		return fmt.Errorf("failed to close output file: %w", err)
	}

	return nil
}

// flushBuffer generates SQL for the buffered rows and writes it to the output file
func (g *StreamingSQLGenerator) flushBuffer() error {
	g.mu.Lock()
	defer g.mu.Unlock()

	// Generate SQL for the buffered rows
	sql := g.dialect.InsertInto(g.options.TableName, g.columns, g.buffer, g.options.BatchSize)

	// Write to file
	if _, err := g.file.WriteString(sql); err != nil {
		return err
	}

	// Clear buffer
	g.buffer = g.buffer[:0]

	return nil
}

// hasNestedObjects checks if the dataset contains nested objects
func (g *StreamingSQLGenerator) hasNestedObjects(rows []common.DataRow) bool {
	// Check each row for nested objects
	for _, row := range rows {
		for _, value := range row {
			// Check if it's a map
			if _, ok := value.(map[string]interface{}); ok {
				return true
			}

			// Check if it's a JSON string that contains an object
			if strValue, ok := value.(string); ok {
				// If it starts with { and ends with }, it might be a JSON object
				if len(strValue) > 1 && strValue[0] == '{' && strValue[len(strValue)-1] == '}' {
					return true
				}
			}
		}
	}

	return false
}

// Close closes the SQL generator and any open resources
func (g *StreamingSQLGenerator) Close() error {
	if g.file != nil {
		return g.file.Close()
	}
	return nil
}
