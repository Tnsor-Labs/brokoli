package engine

import (
	"fmt"
	"strings"

	"github.com/Tnsor-Labs/brokoli/pkg/common"
)

// validateDatasetSchemaOutput checks the column-level part of a declared
// dataset schema against a materialized output. BPTD value typing is validated
// at admission; this boundary verifies the fact the engine can observe: the
// emitted column set and its stable order.
func validateDatasetSchemaOutput(config map[string]interface{}, dataset *common.DataSet) error {
	rawSchema, present := config["schema"]
	if !present || rawSchema == nil {
		return nil
	}
	if dataset == nil {
		return nil
	}
	schema, ok := rawSchema.(map[string]interface{})
	if !ok {
		return fmt.Errorf("dataset schema must be an object")
	}
	return validateDatasetSchemaColumns(schema, dataset.Columns)
}

func validateDatasetSchemaColumnsFromConfig(config map[string]interface{}, actual []string) error {
	rawSchema, present := config["schema"]
	if !present || rawSchema == nil {
		return nil
	}
	schema, ok := rawSchema.(map[string]interface{})
	if !ok {
		return fmt.Errorf("dataset schema must be an object")
	}
	return validateDatasetSchemaColumns(schema, actual)
}

func validateDatasetSchemaColumns(schema map[string]interface{}, actual []string) error {
	rawColumns, ok := schema["columns"].([]interface{})
	if !ok {
		return fmt.Errorf("dataset schema columns must be an array")
	}
	declared := make([]string, 0, len(rawColumns))
	declaredSet := make(map[string]bool, len(rawColumns))
	for index, rawColumn := range rawColumns {
		column, ok := rawColumn.(map[string]interface{})
		if !ok {
			return fmt.Errorf("dataset schema column %d must be an object", index)
		}
		name, ok := column["name"].(string)
		if !ok || name == "" {
			return fmt.Errorf("dataset schema column %d has no name", index)
		}
		if declaredSet[name] {
			return fmt.Errorf("dataset schema contains duplicate column %q", name)
		}
		declaredSet[name] = true
		declared = append(declared, name)
	}

	actualSet := make(map[string]bool, len(actual))
	for _, name := range actual {
		if actualSet[name] {
			return fmt.Errorf("dataset output contains duplicate column %q", name)
		}
		actualSet[name] = true
	}
	var missing, extra []string
	for _, name := range declared {
		if !actualSet[name] {
			missing = append(missing, name)
		}
	}
	for _, name := range actual {
		if !declaredSet[name] {
			extra = append(extra, name)
		}
	}
	mode, _ := schema["additional_columns"].(string)
	if len(missing) > 0 {
		if len(extra) > 0 {
			return fmt.Errorf("dataset output columns differ from declared schema: missing %s, extra %s", strings.Join(missing, ", "), strings.Join(extra, ", "))
		}
		return fmt.Errorf("dataset output is missing declared columns: %s", strings.Join(missing, ", "))
	}
	if mode == "closed" {
		if len(extra) > 0 {
			return fmt.Errorf("dataset output has undeclared columns in closed schema: %s", strings.Join(extra, ", "))
		}
		if len(actual) != len(declared) {
			return fmt.Errorf("dataset output column order differs from closed schema: declared %v, actual %v", declared, actual)
		}
		for i := range declared {
			if declared[i] != actual[i] {
				return fmt.Errorf("dataset output column order differs from closed schema: declared %v, actual %v", declared, actual)
			}
		}
	}
	return nil
}
