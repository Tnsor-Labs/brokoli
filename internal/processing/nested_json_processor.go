package processing

import (
	"github.com/hc12r/brokolisql-go/pkg/common"
)

// NestedJSONProcessorOptions contains options for the nested JSON processor
type NestedJSONProcessorOptions struct {
	SQLGeneratorOptions
	NamingConvention NamingConvention
	TablePrefix      string
	PluralizeTable   bool
}

// NestedJSONProcessor processes JSON data with nested objects
type NestedJSONProcessor struct {
	analyzer  *JSONAnalyzer
	generator *MultiTableGenerator
	options   NestedJSONProcessorOptions
}

// NewNestedJSONProcessor creates a new nested JSON processor with default options
func NewNestedJSONProcessor(options SQLGeneratorOptions) (*NestedJSONProcessor, error) {
	return NewNestedJSONProcessorWithOptions(NestedJSONProcessorOptions{
		SQLGeneratorOptions: options,
		NamingConvention:    SnakeCase,
		TablePrefix:         "",
		PluralizeTable:      true,
	})
}

// NewNestedJSONProcessorWithOptions creates a new nested JSON processor with custom options
func NewNestedJSONProcessorWithOptions(options NestedJSONProcessorOptions) (*NestedJSONProcessor, error) {
	generator, err := NewMultiTableGenerator(options.SQLGeneratorOptions)
	if err != nil {
		return nil, err
	}

	analyzer := NewJSONAnalyzer()

	// Configure the name generator
	nameGen := NewNameGenerator()
	nameGen = nameGen.WithConvention(options.NamingConvention)
	nameGen = nameGen.WithTablePrefix(options.TablePrefix)
	nameGen = nameGen.WithPluralTables(options.PluralizeTable)
	analyzer.registry.NameGenerator = nameGen

	return &NestedJSONProcessor{
		analyzer:  analyzer,
		generator: generator,
		options:   options,
	}, nil
}

// ProcessNestedJSON processes JSON data with nested objects and generates SQL
func (p *NestedJSONProcessor) ProcessNestedJSON(data []map[string]interface{}) (string, error) {
	// Analyze the JSON structure
	registry, err := p.analyzer.AnalyzeJSON(data, p.options.TableName)
	if err != nil {
		return "", err
	}

	// Extract data for all tables
	tableData := p.analyzer.ExtractNestedData(data)

	// Generate SQL for all tables
	sql, err := p.generator.GenerateFromRegistry(registry, tableData)
	if err != nil {
		return "", err
	}

	return sql, nil
}

// ProcessDataSet processes a DataSet with nested objects and generates SQL
func (p *NestedJSONProcessor) ProcessDataSet(dataset *common.DataSet) (string, error) {
	// Convert DataSet to []map[string]interface{}
	data := make([]map[string]interface{}, len(dataset.Rows))
	for i, row := range dataset.Rows {
		data[i] = make(map[string]interface{})
		for key, value := range row {
			data[i][key] = value
		}
	}

	// Process the data
	return p.ProcessNestedJSON(data)
}
