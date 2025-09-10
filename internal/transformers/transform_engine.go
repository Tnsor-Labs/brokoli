package transformers

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/hc12r/brokolisql-go/pkg/common"
)

type TransformConfig struct {
	Transformations []Transformation `json:"transformations"`
}

type Transformation struct {
	Type       string                 `json:"type"`
	Parameters map[string]interface{} `json:"parameters,omitempty"`

	Name       string            `json:"name,omitempty"`
	Expression string            `json:"expression,omitempty"`
	Column     string            `json:"column,omitempty"`
	Function   string            `json:"function,omitempty"`
	Columns    []string          `json:"columns,omitempty"`
	Mapping    map[string]string `json:"mapping,omitempty"`
	Condition  string            `json:"condition,omitempty"`
	Ascending  bool              `json:"ascending,omitempty"`
}

type TransformEngine struct {
	config TransformConfig
}

func NewTransformEngine(configFile string) (*TransformEngine, error) {

	data, err := common.SafeReadFile(configFile)
	if err != nil {
		return nil, fmt.Errorf("failed to read transform config file: %w", err)
	}

	var config TransformConfig
	if err := json.Unmarshal(data, &config); err != nil {
		return nil, fmt.Errorf("failed to parse transform config: %w", err)
	}

	return &TransformEngine{
		config: config,
	}, nil
}

func (e *TransformEngine) ApplyTransformations(dataset *common.DataSet) error {
	for _, transform := range e.config.Transformations {
		if err := e.applyTransformation(transform, dataset); err != nil {
			return err
		}
	}
	return nil
}

func (e *TransformEngine) applyTransformation(transform Transformation, dataset *common.DataSet) error {
	switch transform.Type {
	case "rename_columns":
		return e.renameColumns(transform, dataset)
	case "add_column":
		return e.addColumn(transform, dataset)
	case "filter_rows":
		return e.filterRows(transform, dataset)
	case "apply_function":
		return e.applyFunction(transform, dataset)
	case "replace_values":
		return e.replaceValues(transform, dataset)
	case "drop_columns":
		return e.dropColumns(transform, dataset)
	case "sort":
		return e.sortRows(transform, dataset)
	default:
		return fmt.Errorf("unsupported transformation type: %s", transform.Type)
	}
}

func (e *TransformEngine) renameColumns(transform Transformation, dataset *common.DataSet) error {
	if transform.Mapping == nil {
		return fmt.Errorf("rename_columns transformation requires a mapping")
	}

	newColumns := make([]string, len(dataset.Columns))
	copy(newColumns, dataset.Columns)

	for i, col := range dataset.Columns {
		if newName, ok := transform.Mapping[col]; ok {
			newColumns[i] = newName
		}
	}

	for _, row := range dataset.Rows {
		for oldName, newName := range transform.Mapping {
			if val, ok := row[oldName]; ok {
				row[newName] = val
				delete(row, oldName)
			}
		}
	}

	dataset.Columns = newColumns
	return nil
}

func (e *TransformEngine) addColumn(transform Transformation, dataset *common.DataSet) error {
	if transform.Name == "" {
		return fmt.Errorf("add_column transformation requires a name")
	}
	if transform.Expression == "" {
		return fmt.Errorf("add_column transformation requires an expression")
	}

	dataset.Columns = append(dataset.Columns, transform.Name)

	for _, row := range dataset.Rows {

		if strings.Contains(transform.Expression, "+") {

			parts := strings.Split(transform.Expression, "+")
			result := ""
			for _, part := range parts {
				part = strings.TrimSpace(part)
				if val, ok := row[part]; ok {
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
			row[transform.Name] = result
		} else {

			row[transform.Name] = transform.Expression
		}
	}

	return nil
}

func (e *TransformEngine) filterRows(transform Transformation, dataset *common.DataSet) error {
	if transform.Condition == "" {
		return fmt.Errorf("filter_rows transformation requires a condition")
	}

	// Validate the condition format
	if strings.Contains(transform.Condition, " in ") {
		parts := strings.Split(transform.Condition, " in ")
		if len(parts) != 2 {
			return fmt.Errorf("invalid 'in' condition: %s", transform.Condition)
		}

		// Check if the values part is properly formatted
		valuesStr := strings.TrimSpace(parts[1])
		if !strings.HasPrefix(valuesStr, "[") || !strings.HasSuffix(valuesStr, "]") {
			return fmt.Errorf("invalid 'in' condition format: values must be enclosed in []")
		}

		// Check if the column name is not empty
		colName := strings.TrimSpace(parts[0])
		if colName == "" {
			return fmt.Errorf("invalid 'in' condition: column name cannot be empty")
		}
	}

	var filteredRows []common.DataRow
	for _, row := range dataset.Rows {

		if strings.Contains(transform.Condition, " in ") {
			parts := strings.Split(transform.Condition, " in ")
			// We already validated the format above, so we can safely proceed
			colName := strings.TrimSpace(parts[0])
			valuesStr := strings.TrimSpace(parts[1])
			valuesStr = strings.Trim(valuesStr, "[]")
			values := strings.Split(valuesStr, ",")

			if colVal, ok := row[colName]; ok {
				for _, val := range values {
					val = strings.Trim(val, " '\"")
					if fmt.Sprintf("%v", colVal) == val {
						filteredRows = append(filteredRows, row)
						break
					}
				}
			}
		} else {
			// For other conditions, keep all rows for now
			filteredRows = append(filteredRows, row)
		}
	}

	dataset.Rows = filteredRows
	return nil
}

func (e *TransformEngine) applyFunction(transform Transformation, dataset *common.DataSet) error {
	if transform.Column == "" {
		return fmt.Errorf("apply_function transformation requires a column")
	}
	if transform.Function == "" {
		return fmt.Errorf("apply_function transformation requires a function")
	}

	for _, row := range dataset.Rows {
		if val, ok := row[transform.Column]; ok {
			switch transform.Function {
			case "lower":
				if str, ok := val.(string); ok {
					row[transform.Column] = strings.ToLower(str)
				}
			case "upper":
				if str, ok := val.(string); ok {
					row[transform.Column] = strings.ToUpper(str)
				}
			case "trim":
				if str, ok := val.(string); ok {
					row[transform.Column] = strings.TrimSpace(str)
				}
			default:
				return fmt.Errorf("unsupported function: %s", transform.Function)
			}
		}
	}

	return nil
}

func (e *TransformEngine) replaceValues(transform Transformation, dataset *common.DataSet) error {
	if transform.Column == "" {
		return fmt.Errorf("replace_values transformation requires a column")
	}
	if transform.Mapping == nil {
		return fmt.Errorf("replace_values transformation requires a mapping")
	}

	for _, row := range dataset.Rows {
		if val, ok := row[transform.Column]; ok {
			strVal := fmt.Sprintf("%v", val)
			if newVal, ok := transform.Mapping[strVal]; ok {
				row[transform.Column] = newVal
			}
		}
	}

	return nil
}

func (e *TransformEngine) dropColumns(transform Transformation, dataset *common.DataSet) error {
	if len(transform.Columns) == 0 {
		return fmt.Errorf("drop_columns transformation requires columns")
	}

	dropSet := make(map[string]bool)
	for _, col := range transform.Columns {
		dropSet[col] = true
	}

	var newColumns []string
	for _, col := range dataset.Columns {
		if !dropSet[col] {
			newColumns = append(newColumns, col)
		}
	}

	for _, row := range dataset.Rows {
		for col := range dropSet {
			delete(row, col)
		}
	}

	dataset.Columns = newColumns
	return nil
}

func (e *TransformEngine) sortRows(transform Transformation, dataset *common.DataSet) error {
	if len(transform.Columns) == 0 {
		return fmt.Errorf("sort transformation requires columns")
	}

	sort.SliceStable(dataset.Rows, func(i, j int) bool {
		for _, col := range transform.Columns {
			valI, okI := dataset.Rows[i][col]
			valJ, okJ := dataset.Rows[j][col]

			if !okI && !okJ {
				continue
			}
			if !okI {
				return !transform.Ascending
			}
			if !okJ {
				return transform.Ascending
			}

			strI := fmt.Sprintf("%v", valI)
			strJ := fmt.Sprintf("%v", valJ)
			if strI != strJ {
				if transform.Ascending {
					return strI < strJ
				}
				return strI > strJ
			}
		}
		return false
	})

	return nil
}
