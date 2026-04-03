package models

import (
	"regexp"
	"strings"
	"testing"
)

// slugFromName mirrors the slug generation logic in handlers_pipeline.go Create().
func slugFromName(name string) string {
	pid := strings.ToLower(name)
	pid = strings.ReplaceAll(pid, " ", "-")
	pid = regexp.MustCompile(`[^a-z0-9-]`).ReplaceAllString(pid, "")
	pid = regexp.MustCompile(`-+`).ReplaceAllString(pid, "-")
	pid = strings.Trim(pid, "-")
	return pid
}

func TestPipelineID_AutoGenerate(t *testing.T) {
	tests := []struct {
		name     string
		expected string
	}{
		{"Acme Orders ETL", "acme-orders-etl"},
		{"my-pipeline", "my-pipeline"},
		{"Hello World!", "hello-world"},
		{"  Spaced  Out  ", "spaced-out"},        // double spaces become -- then collapsed to -
		{"UPPER CASE", "upper-case"},
		{"special@#$chars", "specialchars"},
		{"multi---dash", "multi-dash"},
		{"trailing-", "trailing"},
		{"-leading", "leading"},
		{"123 numeric start", "123-numeric-start"},
		{"a", "a"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := slugFromName(tt.name)
			if got != tt.expected {
				t.Errorf("slugFromName(%q) = %q, want %q", tt.name, got, tt.expected)
			}
		})
	}
}

func TestPipelineSource_Constants(t *testing.T) {
	if PipelineSourceUI != "ui" {
		t.Errorf("PipelineSourceUI = %q, want %q", PipelineSourceUI, "ui")
	}
	if PipelineSourceGit != "git" {
		t.Errorf("PipelineSourceGit = %q, want %q", PipelineSourceGit, "git")
	}
}

func TestPipeline_Validate_WithPipelineIDAndSource(t *testing.T) {
	p := Pipeline{
		Name:       "Test",
		PipelineID: "test",
		Source:     PipelineSourceGit,
		Nodes:      []Node{},
		Edges:      []Edge{},
	}
	if err := p.Validate(); err != nil {
		t.Errorf("valid pipeline with pipeline_id and source should not error: %v", err)
	}
}
