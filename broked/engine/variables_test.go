package engine

import (
	"os"
	"strings"
	"testing"
	"time"
)

func TestResolve_EnvVar(t *testing.T) {
	os.Setenv("BROKED_TEST_VAR", "hello")
	defer os.Unsetenv("BROKED_TEST_VAR")

	vc := NewVariableContext(nil, "run-1", time.Now())
	result := vc.Resolve("value is ${env.BROKED_TEST_VAR}")
	if result != "value is hello" {
		t.Errorf("expected 'value is hello', got %q", result)
	}
}

func TestResolve_Param(t *testing.T) {
	vc := NewVariableContext(map[string]string{"date": "2024-01-15"}, "run-1", time.Now())
	result := vc.Resolve("process ${param.date}")
	if result != "process 2024-01-15" {
		t.Errorf("expected 'process 2024-01-15', got %q", result)
	}
}

func TestResolve_RunID(t *testing.T) {
	vc := NewVariableContext(nil, "abc-123", time.Now())
	result := vc.Resolve("run ${run.id}")
	if result != "run abc-123" {
		t.Errorf("expected 'run abc-123', got %q", result)
	}
}

func TestResolve_RunDate(t *testing.T) {
	t0 := time.Date(2024, 3, 15, 14, 30, 0, 0, time.UTC)
	vc := NewVariableContext(nil, "run-1", t0)
	result := vc.Resolve("date: ${run.date}")
	if result != "date: 2024-03-15" {
		t.Errorf("expected 'date: 2024-03-15', got %q", result)
	}
}

func TestResolve_Secret(t *testing.T) {
	os.Setenv("BROKED_SECRET_DB_PASSWORD", "s3cret")
	defer os.Unsetenv("BROKED_SECRET_DB_PASSWORD")

	vc := NewVariableContext(nil, "run-1", time.Now())
	result := vc.Resolve("postgres://user:${secret.db_password}@host/db")
	if result != "postgres://user:s3cret@host/db" {
		t.Errorf("expected resolved secret, got %q", result)
	}
}

func TestResolve_Multiple(t *testing.T) {
	vc := NewVariableContext(map[string]string{"table": "users"}, "run-1", time.Now())
	result := vc.Resolve("SELECT * FROM ${param.table} WHERE run = '${run.id}'")
	if !strings.Contains(result, "users") || !strings.Contains(result, "run-1") {
		t.Errorf("expected resolved vars, got %q", result)
	}
}

func TestResolve_NoVars(t *testing.T) {
	vc := NewVariableContext(nil, "run-1", time.Now())
	result := vc.Resolve("no variables here")
	if result != "no variables here" {
		t.Errorf("should pass through, got %q", result)
	}
}

func TestResolve_UnknownVar(t *testing.T) {
	vc := NewVariableContext(nil, "run-1", time.Now())
	result := vc.Resolve("${unknown.thing}")
	if result != "${unknown.thing}" {
		t.Errorf("should keep unresolved var, got %q", result)
	}
}

func TestResolveConfig(t *testing.T) {
	vc := NewVariableContext(map[string]string{"path": "/data"}, "run-1", time.Now())
	config := map[string]interface{}{
		"path":  "${param.path}/input.csv",
		"table": "users",
		"nested": map[string]interface{}{
			"key": "${run.id}",
		},
	}
	resolved := vc.ResolveConfig(config)
	if resolved["path"] != "/data/input.csv" {
		t.Errorf("path = %q", resolved["path"])
	}
	nested := resolved["nested"].(map[string]interface{})
	if nested["key"] != "run-1" {
		t.Errorf("nested key = %q", nested["key"])
	}
}
