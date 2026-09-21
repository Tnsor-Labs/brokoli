package engine

import (
	"strings"
	"testing"

	"github.com/Tnsor-Labs/brokoli/pkg/common"
)

func schemaConfig(mode string, names ...string) map[string]interface{} {
	columns := make([]interface{}, 0, len(names))
	for _, name := range names {
		columns = append(columns, map[string]interface{}{
			"name": name,
			"type": map[string]interface{}{"kind": "unknown"},
		})
	}
	return map[string]interface{}{
		"contract":           "brokoli.dataset-schema/v1",
		"columns":            columns,
		"additional_columns": mode,
	}
}

func nodeSchemaConfig(mode string, names ...string) map[string]interface{} {
	return map[string]interface{}{"schema": schemaConfig(mode, names...)}
}

func TestValidateDatasetSchemaOutputClosedRequiresExactColumns(t *testing.T) {
	dataset := &common.DataSet{Columns: []string{"id", "name"}}
	if err := validateDatasetSchemaOutput(nodeSchemaConfig("closed", "id", "name"), dataset); err != nil {
		t.Fatalf("matching schema rejected: %v", err)
	}

	dataset.Columns = []string{"id", "email"}
	err := validateDatasetSchemaOutput(nodeSchemaConfig("closed", "id", "name"), dataset)
	if err == nil || !strings.Contains(err.Error(), "name") || !strings.Contains(err.Error(), "email") {
		t.Fatalf("expected exact-column error naming both schemas, got %v", err)
	}
}

func TestValidateDatasetSchemaOutputOpenAllowsAdditionalColumns(t *testing.T) {
	dataset := &common.DataSet{Columns: []string{"id", "name", "extra"}}
	if err := validateDatasetSchemaOutput(nodeSchemaConfig("open", "id", "name"), dataset); err != nil {
		t.Fatalf("open schema rejected additional column: %v", err)
	}
}

func TestValidateDatasetSchemaOutputUnknownRequiresDeclaredColumns(t *testing.T) {
	dataset := &common.DataSet{Columns: []string{"id", "extra"}}
	err := validateDatasetSchemaOutput(nodeSchemaConfig("unknown", "id", "name"), dataset)
	if err == nil || !strings.Contains(err.Error(), "name") {
		t.Fatalf("expected missing declared column error, got %v", err)
	}
}

func TestValidateDatasetSchemaColumnsSupportsReferences(t *testing.T) {
	schema := schemaConfig("closed", "id", "name")
	if err := validateDatasetSchemaColumns(schema, []string{"id", "name"}); err != nil {
		t.Fatalf("matching reference columns rejected: %v", err)
	}
	if err := validateDatasetSchemaColumns(schema, []string{"id", "email"}); err == nil {
		t.Fatal("reference columns that violate a closed schema were accepted")
	}
}
