package processing

import (
	"github.com/hc12r/brokolisql-go/internal/dialects"
	"github.com/hc12r/brokolisql-go/pkg/common"
)

type SQLGeneratorOptions struct {
	Dialect          string
	TableName        string
	CreateTable      bool
	BatchSize        int
	NormalizeColumns bool
}

type SQLGenerator struct {
	options     SQLGeneratorOptions
	normalizer  *Normalizer
	typeInferer *TypeInferenceEngine
	dialect     dialects.Dialect
}

func NewSQLGenerator(options SQLGeneratorOptions) (*SQLGenerator, error) {

	if options.TableName == "" {
		options.TableName = "data"
	}

	if options.BatchSize <= 0 {
		options.BatchSize = 100
	}

	dialect, err := dialects.GetDialect(options.Dialect)
	if err != nil {
		return nil, err
	}

	return &SQLGenerator{
		options:     options,
		normalizer:  NewNormalizer(),
		typeInferer: NewTypeInferenceEngine(),
		dialect:     dialect,
	}, nil
}

func (g *SQLGenerator) Generate(dataset *common.DataSet) (string, error) {
	// Check if we need to handle nested objects
	hasNestedObjects := g.hasNestedObjects(dataset)

	if hasNestedObjects {
		// Use the nested JSON processor for nested objects
		processor, err := NewNestedJSONProcessor(g.options)
		if err != nil {
			return "", err
		}
		return processor.ProcessDataSet(dataset)
	}

	// Original implementation for flat data
	columns := dataset.Columns
	if g.options.NormalizeColumns {
		columns = g.normalizer.NormalizeColumnNames(columns)
	}

	if g.options.NormalizeColumns {
		normalizedRows := make([]common.DataRow, len(dataset.Rows))
		for i, row := range dataset.Rows {
			normalizedRow := make(common.DataRow)
			for j, col := range dataset.Columns {
				normalizedCol := columns[j]
				normalizedRow[normalizedCol] = row[col]
			}
			normalizedRows[i] = normalizedRow
		}
		dataset.Rows = normalizedRows
	}

	var sql string

	if g.options.CreateTable {
		columnTypes := g.typeInferer.InferColumnTypes(columns, dataset.Rows)

		columnDefs := make([]dialects.ColumnDef, len(columns))
		for i, col := range columns {
			columnDefs[i] = dialects.ColumnDef{
				Name:     col,
				Type:     columnTypes[col],
				Nullable: true, // Default to nullable
			}
		}

		sql += g.dialect.CreateTable(g.options.TableName, columnDefs)
		sql += "\n"
	}

	values := make([][]interface{}, len(dataset.Rows))
	for i, row := range dataset.Rows {
		rowValues := make([]interface{}, len(columns))
		for j, col := range columns {
			rowValues[j] = row[col]
		}
		values[i] = rowValues
	}

	sql += g.dialect.InsertInto(g.options.TableName, columns, values, g.options.BatchSize)

	return sql, nil
}

// hasNestedObjects checks if the dataset contains nested objects
func (g *SQLGenerator) hasNestedObjects(dataset *common.DataSet) bool {
	// Check each row for nested objects
	for _, row := range dataset.Rows {
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
