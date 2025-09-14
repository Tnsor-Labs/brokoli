package transformers

import (
	"fmt"
	"strings"

	"github.com/hc12r/brokolisql-go/pkg/common"
)

// StreamingTransformEngine applies transformations to streaming data
type StreamingTransformEngine struct {
	config          TransformConfig
	originalCols    []string
	transformedCols []string
	colMapping      map[string]string            // For column renaming
	droppedCols     map[string]bool              // For dropped columns
	addedCols       map[string]string            // For added columns (name -> expression)
	filterCond      string                       // For filtering rows
	replacements    map[string]map[string]string // For value replacements (column -> old -> new)
	functions       map[string]string            // For function applications (column -> function)
}

// NewStreamingTransformEngine creates a new streaming transform engine
func NewStreamingTransformEngine(configFile string) (*StreamingTransformEngine, error) {
	// Reuse the config loading logic from the regular TransformEngine
	regularEngine, err := NewTransformEngine(configFile)
	if err != nil {
		return nil, err
	}

	engine := &StreamingTransformEngine{
		config:       regularEngine.config,
		colMapping:   make(map[string]string),
		droppedCols:  make(map[string]bool),
		addedCols:    make(map[string]string),
		replacements: make(map[string]map[string]string),
		functions:    make(map[string]string),
	}

	return engine, nil
}

// PrepareTransformations analyzes the transformations and prepares for streaming processing
func (e *StreamingTransformEngine) PrepareTransformations(columns []string) ([]string, error) {
	e.originalCols = columns
	transformedCols := make([]string, len(columns))
	copy(transformedCols, columns)

	// First pass: analyze all transformations to prepare data structures
	for _, transform := range e.config.Transformations {
		switch transform.Type {
		case "rename_columns":
			if transform.Mapping == nil {
				return nil, fmt.Errorf("rename_columns transformation requires a mapping")
			}

			// Update column mapping
			for oldName, newName := range transform.Mapping {
				e.colMapping[oldName] = newName
			}

			// Update transformed column names
			for i, col := range transformedCols {
				if newName, ok := transform.Mapping[col]; ok {
					transformedCols[i] = newName
				}
			}

		case "add_column":
			if transform.Name == "" {
				return nil, fmt.Errorf("add_column transformation requires a name")
			}
			if transform.Expression == "" {
				return nil, fmt.Errorf("add_column transformation requires an expression")
			}

			e.addedCols[transform.Name] = transform.Expression
			transformedCols = append(transformedCols, transform.Name)

		case "filter_rows":
			if transform.Condition == "" {
				return nil, fmt.Errorf("filter_rows transformation requires a condition")
			}

			// Validate the condition format
			if strings.Contains(transform.Condition, " in ") {
				parts := strings.Split(transform.Condition, " in ")
				if len(parts) != 2 {
					return nil, fmt.Errorf("invalid 'in' condition: %s", transform.Condition)
				}

				// Check if the values part is properly formatted
				valuesStr := strings.TrimSpace(parts[1])
				if !strings.HasPrefix(valuesStr, "[") || !strings.HasSuffix(valuesStr, "]") {
					return nil, fmt.Errorf("invalid 'in' condition format: values must be enclosed in []")
				}

				// Check if the column name is not empty
				colName := strings.TrimSpace(parts[0])
				if colName == "" {
					return nil, fmt.Errorf("invalid 'in' condition: column name cannot be empty")
				}
			}

			e.filterCond = transform.Condition

		case "apply_function":
			if transform.Column == "" {
				return nil, fmt.Errorf("apply_function transformation requires a column")
			}
			if transform.Function == "" {
				return nil, fmt.Errorf("apply_function transformation requires a function")
			}

			// Validate function
			switch transform.Function {
			case "lower", "upper", "trim":
				// These are supported
			default:
				return nil, fmt.Errorf("unsupported function: %s", transform.Function)
			}

			e.functions[transform.Column] = transform.Function

		case "replace_values":
			if transform.Column == "" {
				return nil, fmt.Errorf("replace_values transformation requires a column")
			}
			if transform.Mapping == nil {
				return nil, fmt.Errorf("replace_values transformation requires a mapping")
			}

			e.replacements[transform.Column] = transform.Mapping

		case "drop_columns":
			if len(transform.Columns) == 0 {
				return nil, fmt.Errorf("drop_columns transformation requires columns")
			}

			for _, col := range transform.Columns {
				e.droppedCols[col] = true
			}

			// Update transformed column list
			newTransformedCols := make([]string, 0, len(transformedCols))
			for _, col := range transformedCols {
				if !e.droppedCols[col] {
					newTransformedCols = append(newTransformedCols, col)
				}
			}
			transformedCols = newTransformedCols

		case "sort":
			// Sort cannot be applied in streaming mode
			return nil, fmt.Errorf("sort transformation is not supported in streaming mode")

		default:
			return nil, fmt.Errorf("unsupported transformation type: %s", transform.Type)
		}
	}

	e.transformedCols = transformedCols
	return transformedCols, nil
}

// TransformRow applies all transformations to a single row
func (e *StreamingTransformEngine) TransformRow(row common.DataRow) (common.DataRow, bool) {
	// Skip this row if it doesn't pass the filter
	if e.filterCond != "" && !e.rowPassesFilter(row) {
		return nil, false
	}

	// Create a new row with the transformed data
	transformedRow := make(common.DataRow)

	// Copy original data, applying column renames
	for col, val := range row {
		if newName, ok := e.colMapping[col]; ok {
			transformedRow[newName] = val
		} else if !e.droppedCols[col] {
			transformedRow[col] = val
		}
	}

	// Apply functions
	for col, funcName := range e.functions {
		// Check if the column exists in the transformed row
		// It might have been renamed
		var colName string
		if newName, ok := e.colMapping[col]; ok {
			colName = newName
		} else {
			colName = col
		}

		if val, ok := transformedRow[colName]; ok {
			switch funcName {
			case "lower":
				if str, ok := val.(string); ok {
					transformedRow[colName] = strings.ToLower(str)
				}
			case "upper":
				if str, ok := val.(string); ok {
					transformedRow[colName] = strings.ToUpper(str)
				}
			case "trim":
				if str, ok := val.(string); ok {
					transformedRow[colName] = strings.TrimSpace(str)
				}
			}
		}
	}

	// Apply value replacements
	for col, mapping := range e.replacements {
		// Check if the column exists in the transformed row
		// It might have been renamed
		var colName string
		if newName, ok := e.colMapping[col]; ok {
			colName = newName
		} else {
			colName = col
		}

		if val, ok := transformedRow[colName]; ok {
			strVal := fmt.Sprintf("%v", val)
			if newVal, ok := mapping[strVal]; ok {
				transformedRow[colName] = newVal
			}
		}
	}

	// Add new columns
	for colName, expr := range e.addedCols {
		if strings.Contains(expr, "+") {
			parts := strings.Split(expr, "+")
			result := ""
			for _, part := range parts {
				part = strings.TrimSpace(part)
				if val, ok := transformedRow[part]; ok {
					result += fmt.Sprintf("%v", val)
				} else {
					// Check if this is a string literal (enclosed in quotes)
					if (strings.HasPrefix(part, "'") && strings.HasSuffix(part, "'")) ||
						(strings.HasPrefix(part, "\"") && strings.HasSuffix(part, "\"")) {
						// Remove the quotes and add the literal string
						result += part[1 : len(part)-1]
					} else {
						result += part
					}
				}
			}
			transformedRow[colName] = result
		} else {
			transformedRow[colName] = expr
		}
	}

	return transformedRow, true
}

// rowPassesFilter checks if a row passes the filter condition
func (e *StreamingTransformEngine) rowPassesFilter(row common.DataRow) bool {
	if strings.Contains(e.filterCond, " in ") {
		parts := strings.Split(e.filterCond, " in ")
		colName := strings.TrimSpace(parts[0])
		valuesStr := strings.TrimSpace(parts[1])
		valuesStr = strings.Trim(valuesStr, "[]")
		values := strings.Split(valuesStr, ",")

		if colVal, ok := row[colName]; ok {
			for _, val := range values {
				val = strings.Trim(val, " '\"")
				if fmt.Sprintf("%v", colVal) == val {
					return true
				}
			}
			return false
		}
	}

	// If we can't evaluate the condition, let the row pass
	return true
}

// GetTransformedColumns returns the list of columns after all transformations
func (e *StreamingTransformEngine) GetTransformedColumns() []string {
	return e.transformedCols
}
