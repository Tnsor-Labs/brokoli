package processing

import (
	"strings"
	"testing"

	"github.com/hc12r/brokolisql-go/pkg/common"
)

func TestNewSQLGenerator(t *testing.T) {
	tests := []struct {
		name    string
		options SQLGeneratorOptions
		wantErr bool
	}{
		{
			name: "Valid options",
			options: SQLGeneratorOptions{
				Dialect:          "generic",
				TableName:        "users",
				CreateTable:      true,
				BatchSize:        50,
				NormalizeColumns: true,
			},
			wantErr: false,
		},
		{
			name: "Empty table name",
			options: SQLGeneratorOptions{
				Dialect:          "generic",
				TableName:        "",
				CreateTable:      true,
				BatchSize:        50,
				NormalizeColumns: true,
			},
			wantErr: false, // Should use default table name
		},
		{
			name: "Zero batch size",
			options: SQLGeneratorOptions{
				Dialect:          "generic",
				TableName:        "users",
				CreateTable:      true,
				BatchSize:        0,
				NormalizeColumns: true,
			},
			wantErr: false, // Should use default batch size
		},
		{
			name: "Invalid dialect",
			options: SQLGeneratorOptions{
				Dialect:          "invalid",
				TableName:        "users",
				CreateTable:      true,
				BatchSize:        50,
				NormalizeColumns: true,
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			generator, err := NewSQLGenerator(tt.options)

			if (err != nil) != tt.wantErr {
				t.Errorf("NewSQLGenerator() error = %v, wantErr %v", err, tt.wantErr)
				return
			}

			if err != nil {
				return
			}

			// Check that the generator was created with the correct options
			if generator.options.Dialect != tt.options.Dialect {
				t.Errorf("NewSQLGenerator() Dialect = %v, want %v", generator.options.Dialect, tt.options.Dialect)
			}

			// Check default values
			if tt.options.TableName == "" && generator.options.TableName != "data" {
				t.Errorf("NewSQLGenerator() TableName = %v, want %v", generator.options.TableName, "data")
			}

			if tt.options.BatchSize <= 0 && generator.options.BatchSize != 100 {
				t.Errorf("NewSQLGenerator() BatchSize = %v, want %v", generator.options.BatchSize, 100)
			}

			// Check that the normalizer and type inferer were created
			if generator.normalizer == nil {
				t.Errorf("NewSQLGenerator() normalizer is nil")
			}

			if generator.typeInferer == nil {
				t.Errorf("NewSQLGenerator() typeInferer is nil")
			}

			// Check that the dialect was created
			if generator.dialect == nil {
				t.Errorf("NewSQLGenerator() dialect is nil")
			}
		})
	}
}

func TestSQLGenerator_Generate(t *testing.T) {
	// Create a test dataset
	dataset := &common.DataSet{
		Columns: []string{"id", "name", "age", "is_active"},
		Rows: []common.DataRow{
			{"id": 1, "name": "John Doe", "age": 30, "is_active": true},
			{"id": 2, "name": "Jane Smith", "age": 25, "is_active": false},
		},
	}

	tests := []struct {
		name    string
		options SQLGeneratorOptions
		wantErr bool
		checkFn func(*testing.T, string)
	}{
		{
			name: "Generate with CREATE TABLE",
			options: SQLGeneratorOptions{
				Dialect:          "generic",
				TableName:        "users",
				CreateTable:      true,
				BatchSize:        50,
				NormalizeColumns: true,
			},
			wantErr: false,
			checkFn: func(t *testing.T, sql string) {
				// Check that the SQL contains CREATE TABLE
				if !strings.Contains(sql, "CREATE TABLE") {
					t.Errorf("Generate() SQL does not contain CREATE TABLE")
				}

				// Check that the SQL contains INSERT INTO
				if !strings.Contains(sql, "INSERT INTO") {
					t.Errorf("Generate() SQL does not contain INSERT INTO")
				}

				// Check that column names are normalized
				if !strings.Contains(sql, "\"ID\"") {
					t.Errorf("Generate() SQL does not contain normalized column name \"ID\"")
				}

				// Check that the table name is correct
				if !strings.Contains(sql, "\"users\"") {
					t.Errorf("Generate() SQL does not contain table name \"users\"")
				}

				// Check that the SQL contains the correct number of rows in the INSERT statement
				// Find the INSERT INTO statement
				insertPos := strings.Index(sql, "INSERT INTO")
				if insertPos == -1 {
					t.Errorf("Generate() SQL does not contain INSERT INTO")
					return
				}

				// Count only the parentheses in the INSERT statement
				insertSQL := sql[insertPos:]
				rowCount := strings.Count(insertSQL, "(")

				// Subtract the opening parenthesis for the column list
				rowCount--

				if rowCount != 2 {
					t.Errorf("Generate() SQL contains %d rows, want %d", rowCount, 2)
				}
			},
		},
		{
			name: "Generate without CREATE TABLE",
			options: SQLGeneratorOptions{
				Dialect:          "generic",
				TableName:        "users",
				CreateTable:      false,
				BatchSize:        50,
				NormalizeColumns: true,
			},
			wantErr: false,
			checkFn: func(t *testing.T, sql string) {
				// Check that the SQL does not contain CREATE TABLE
				if strings.Contains(sql, "CREATE TABLE") {
					t.Errorf("Generate() SQL contains CREATE TABLE")
				}

				// Check that the SQL contains INSERT INTO
				if !strings.Contains(sql, "INSERT INTO") {
					t.Errorf("Generate() SQL does not contain INSERT INTO")
				}
			},
		},
		{
			name: "Generate without normalizing columns",
			options: SQLGeneratorOptions{
				Dialect:          "generic",
				TableName:        "users",
				CreateTable:      true,
				BatchSize:        50,
				NormalizeColumns: false,
			},
			wantErr: false,
			checkFn: func(t *testing.T, sql string) {
				// Check that column names are not normalized
				if !strings.Contains(sql, "\"id\"") {
					t.Errorf("Generate() SQL does not contain original column name \"id\"")
				}
			},
		},
		{
			name: "Generate with batch size",
			options: SQLGeneratorOptions{
				Dialect:          "generic",
				TableName:        "users",
				CreateTable:      false,
				BatchSize:        1, // One row per batch
				NormalizeColumns: true,
			},
			wantErr: false,
			checkFn: func(t *testing.T, sql string) {
				// Check that the SQL contains multiple INSERT statements
				insertCount := strings.Count(sql, "INSERT INTO")
				if insertCount != 2 {
					t.Errorf("Generate() SQL contains %d INSERT statements, want %d", insertCount, 2)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			generator, err := NewSQLGenerator(tt.options)
			if err != nil {
				t.Fatalf("Failed to create SQL generator: %v", err)
			}

			sql, err := generator.Generate(dataset)
			if (err != nil) != tt.wantErr {
				t.Errorf("Generate() error = %v, wantErr %v", err, tt.wantErr)
				return
			}

			if err != nil {
				return
			}

			if tt.checkFn != nil {
				tt.checkFn(t, sql)
			}
		})
	}
}

func TestSQLGenerator_Generate_EmptyDataset(t *testing.T) {
	// Create an empty dataset
	dataset := &common.DataSet{
		Columns: []string{"id", "name", "age"},
		Rows:    []common.DataRow{},
	}

	options := SQLGeneratorOptions{
		Dialect:          "generic",
		TableName:        "users",
		CreateTable:      true,
		BatchSize:        50,
		NormalizeColumns: true,
	}

	generator, err := NewSQLGenerator(options)
	if err != nil {
		t.Fatalf("Failed to create SQL generator: %v", err)
	}

	sql, err := generator.Generate(dataset)
	if err != nil {
		t.Errorf("Generate() error = %v", err)
		return
	}

	// Check that the SQL contains CREATE TABLE
	if !strings.Contains(sql, "CREATE TABLE") {
		t.Errorf("Generate() SQL does not contain CREATE TABLE")
	}

	// Check that the SQL does not contain INSERT INTO (no rows)
	if strings.Contains(sql, "VALUES") {
		t.Errorf("Generate() SQL contains VALUES for empty dataset")
	}
}

func TestSQLGenerator_Generate_DifferentDialects(t *testing.T) {
	// Create a test dataset
	dataset := &common.DataSet{
		Columns: []string{"id", "name"},
		Rows: []common.DataRow{
			{"id": 1, "name": "John"},
		},
	}

	dialects := []string{"generic", "postgres", "mysql", "sqlite", "sqlserver", "oracle"}

	for _, dialect := range dialects {
		t.Run(dialect, func(t *testing.T) {
			options := SQLGeneratorOptions{
				Dialect:          dialect,
				TableName:        "users",
				CreateTable:      true,
				BatchSize:        50,
				NormalizeColumns: true,
			}

			generator, err := NewSQLGenerator(options)
			if err != nil {
				t.Fatalf("Failed to create SQL generator for dialect %s: %v", dialect, err)
			}

			sql, err := generator.Generate(dataset)
			if err != nil {
				t.Errorf("Generate() error = %v", err)
				return
			}

			// Check that the SQL contains CREATE TABLE
			if !strings.Contains(sql, "CREATE TABLE") {
				t.Errorf("Generate() SQL does not contain CREATE TABLE")
			}

			// Check that the SQL contains INSERT INTO
			if !strings.Contains(sql, "INSERT INTO") {
				t.Errorf("Generate() SQL does not contain INSERT INTO")
			}
		})
	}
}
