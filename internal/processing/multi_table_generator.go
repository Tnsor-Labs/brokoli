package processing

import (
	"strings"

	"github.com/hc12r/brokolisql-go/internal/dialects"
)

// MultiTableGenerator generates SQL for multiple related tables
type MultiTableGenerator struct {
	options     SQLGeneratorOptions
	normalizer  *Normalizer
	typeInferer *TypeInferenceEngine
	dialect     dialects.Dialect
}

// NewMultiTableGenerator creates a new multi-table SQL generator
func NewMultiTableGenerator(options SQLGeneratorOptions) (*MultiTableGenerator, error) {
	dialect, err := dialects.GetDialect(options.Dialect)
	if err != nil {
		return nil, err
	}

	return &MultiTableGenerator{
		options:     options,
		normalizer:  NewNormalizer(),
		typeInferer: NewTypeInferenceEngine(),
		dialect:     dialect,
	}, nil
}

// GenerateFromRegistry generates SQL for all tables in the registry
func (g *MultiTableGenerator) GenerateFromRegistry(registry *SchemaRegistry, tableData map[string][]map[string]interface{}) (string, error) {
	var sb strings.Builder

	// Generate CREATE TABLE statements in dependency order
	if g.options.CreateTable {
		for _, tableName := range registry.TableOrder {
			table := registry.GetTable(tableName)
			if table == nil {
				continue
			}

			// Generate CREATE TABLE statement
			createTableSQL := g.generateCreateTable(table)
			sb.WriteString(createTableSQL)
			sb.WriteString("\n")
		}
	}

	// Generate INSERT statements in dependency order
	for _, tableName := range registry.TableOrder {
		table := registry.GetTable(tableName)
		if table == nil {
			continue
		}

		// Get data for this table
		data, ok := tableData[tableName]
		if !ok || len(data) == 0 {
			continue
		}

		// Generate INSERT statements
		insertSQL := g.generateInsertStatements(table, data)
		sb.WriteString(insertSQL)
		sb.WriteString("\n")
	}

	return sb.String(), nil
}

// generateCreateTable generates a CREATE TABLE statement for a table
func (g *MultiTableGenerator) generateCreateTable(table *TableSchema) string {
	var sb strings.Builder

	// Start CREATE TABLE statement
	sb.WriteString("CREATE TABLE ")
	sb.WriteString(g.dialect.QuoteIdentifier(table.Name))
	sb.WriteString(" (\n")

	// Add columns
	for i, col := range table.Columns {
		if i > 0 {
			sb.WriteString(",\n")
		}

		sb.WriteString("  ")
		sb.WriteString(g.dialect.QuoteIdentifier(col.Name))
		sb.WriteString(" ")
		sb.WriteString(string(col.Type))

		if col.Name == table.PrimaryKey {
			sb.WriteString(" PRIMARY KEY")
		} else if !col.Nullable {
			sb.WriteString(" NOT NULL")
		}
	}

	// Add foreign key constraints
	if len(table.ForeignKeys) > 0 {
		for _, fk := range table.ForeignKeys {
			sb.WriteString(",\n  FOREIGN KEY (")
			sb.WriteString(g.dialect.QuoteIdentifier(fk.Column))
			sb.WriteString(") REFERENCES ")
			sb.WriteString(g.dialect.QuoteIdentifier(fk.RefTable))
			sb.WriteString(" (")
			sb.WriteString(g.dialect.QuoteIdentifier(fk.RefColumn))
			sb.WriteString(")")

			// Add ON DELETE CASCADE for nested child relationships
			if fk.IsNestedChild {
				sb.WriteString(" ON DELETE CASCADE")
			}
		}
	}

	// End CREATE TABLE statement
	sb.WriteString("\n);\n")

	return sb.String()
}

// generateInsertStatements generates INSERT statements for a table
func (g *MultiTableGenerator) generateInsertStatements(table *TableSchema, data []map[string]interface{}) string {
	var sb strings.Builder

	// Get column names
	var columns []string
	for _, col := range table.Columns {
		columns = append(columns, col.Name)
	}

	// Prepare values
	values := make([][]interface{}, len(data))
	for i, row := range data {
		rowValues := make([]interface{}, len(columns))
		for j, col := range columns {
			rowValues[j] = row[col]
		}
		values[i] = rowValues
	}

	// Generate INSERT statements
	insertSQL := g.dialect.InsertInto(table.Name, columns, values, g.options.BatchSize)
	sb.WriteString(insertSQL)

	return sb.String()
}
