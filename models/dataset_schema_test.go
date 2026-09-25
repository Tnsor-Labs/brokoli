package models_test

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

const datasetSchemaPath = "../docs/schema/dataset-schema-v1.json"
const datasetSchemaTaskInterfaceURL = "https://github.com/Tnsor-Labs/brokoli/docs/schema/task-interface-v1.json"

func compileDatasetSchema(t *testing.T) *jsonschema.Schema {
	t.Helper()
	c := jsonschema.NewCompiler()
	taskSchemaData, err := os.ReadFile("../docs/schema/task-interface-v1.json")
	if err != nil {
		t.Fatalf("read task-interface-v1.json: %v", err)
	}
	taskSchema, err := jsonschema.UnmarshalJSON(bytes.NewReader(taskSchemaData))
	if err != nil {
		t.Fatalf("parse task-interface-v1.json: %v", err)
	}
	if err := c.AddResource(datasetSchemaTaskInterfaceURL, taskSchema); err != nil {
		t.Fatalf("register task-interface-v1.json: %v", err)
	}
	sch, err := c.Compile(datasetSchemaPath)
	if err != nil {
		t.Fatalf("compile dataset-schema-v1.json: %v", err)
	}
	return sch
}

func validateDatasetFixture(t *testing.T, sch *jsonschema.Schema, path string, negative bool) error {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	if negative {
		var document map[string]interface{}
		if err := json.Unmarshal(data, &document); err != nil {
			t.Fatalf("%s: unmarshal negative fixture: %v", path, err)
		}
		if _, ok := document["_violation"]; !ok {
			t.Fatalf("%s: negative fixture is missing _violation", path)
		}
		delete(document, "_violation")
		data, err = json.Marshal(document)
		if err != nil {
			t.Fatalf("%s: remarshal negative fixture: %v", path, err)
		}
	}
	instance, err := jsonschema.UnmarshalJSON(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("%s: parse fixture: %v", path, err)
	}
	return sch.Validate(instance)
}

func TestDatasetSchemaPositiveFixturesValidate(t *testing.T) {
	sch := compileDatasetSchema(t)
	dir := "../docs/schema/fixtures/dataset-schema-v1/positive"
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read %s: %v", dir, err)
	}
	validated := 0
	for _, entry := range entries {
		if filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		path := filepath.Join(dir, entry.Name())
		if err := validateDatasetFixture(t, sch, path, false); err != nil {
			t.Errorf("positive fixture %s rejected:\n%v", entry.Name(), err)
		}
		validated++
	}
	if validated == 0 {
		t.Fatal("no positive dataset-schema fixtures were validated")
	}
}

func TestDatasetSchemaNegativeFixturesReject(t *testing.T) {
	sch := compileDatasetSchema(t)
	dir := "../docs/schema/fixtures/dataset-schema-v1/negative"
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read %s: %v", dir, err)
	}
	validated := 0
	for _, entry := range entries {
		if filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		path := filepath.Join(dir, entry.Name())
		if err := validateDatasetFixture(t, sch, path, true); err == nil {
			t.Errorf("negative fixture %s was accepted", entry.Name())
		}
		validated++
	}
	if validated == 0 {
		t.Fatal("no negative dataset-schema fixtures were validated")
	}
}
