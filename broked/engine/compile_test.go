package engine

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func brokoliSDKAvailable() bool {
	cmd := exec.Command("python3", "-c", "from brokoli.pipeline import Pipeline; print('OK')")
	out, err := cmd.Output()
	return err == nil && len(out) > 0
}

func TestCompilePythonPipeline_ValidFile(t *testing.T) {
	if !brokoliSDKAvailable() {
		t.Skip("brokoli SDK not installed")
	}

	dir := t.TempDir()
	pyFile := filepath.Join(dir, "valid_pipeline.py")
	err := os.WriteFile(pyFile, []byte(`from brokoli.pipeline import Pipeline

with Pipeline("test-pipeline", description="A test") as p:
    p._add_node("n1", "source_api", "Fetch", {"url": "https://example.com"})
`), 0644)
	if err != nil {
		t.Fatal(err)
	}

	result, err := CompilePythonPipeline(pyFile)
	if err != nil {
		t.Fatalf("CompilePythonPipeline: %v", err)
	}
	if len(result) != 1 {
		t.Fatalf("expected 1 pipeline, got %d", len(result))
	}
	if result[0].Name != "test-pipeline" {
		t.Errorf("Name = %q, want %q", result[0].Name, "test-pipeline")
	}
	if result[0].PipelineID != "test-pipeline" {
		t.Errorf("PipelineID = %q, want %q", result[0].PipelineID, "test-pipeline")
	}
}

func TestCompilePythonPipeline_InvalidFile(t *testing.T) {
	if !brokoliSDKAvailable() {
		t.Skip("brokoli SDK not installed")
	}

	dir := t.TempDir()
	pyFile := filepath.Join(dir, "bad.py")
	err := os.WriteFile(pyFile, []byte(`def broken(
    # syntax error - unclosed paren
`), 0644)
	if err != nil {
		t.Fatal(err)
	}

	_, err = CompilePythonPipeline(pyFile)
	if err == nil {
		t.Fatal("expected error for invalid Python file, got nil")
	}
}

func TestCompilePythonPipeline_NoPipeline(t *testing.T) {
	if !brokoliSDKAvailable() {
		t.Skip("brokoli SDK not installed")
	}

	dir := t.TempDir()
	pyFile := filepath.Join(dir, "noop.py")
	err := os.WriteFile(pyFile, []byte(`x = 42
y = x + 1
`), 0644)
	if err != nil {
		t.Fatal(err)
	}

	result, err := CompilePythonPipeline(pyFile)
	if err != nil {
		t.Fatalf("CompilePythonPipeline: %v", err)
	}
	if len(result) != 0 {
		t.Errorf("expected 0 pipelines, got %d", len(result))
	}
}

func TestCompilePythonPipeline_MultiplePipelines(t *testing.T) {
	if !brokoliSDKAvailable() {
		t.Skip("brokoli SDK not installed")
	}

	dir := t.TempDir()
	pyFile := filepath.Join(dir, "multi.py")
	err := os.WriteFile(pyFile, []byte(`from brokoli.pipeline import Pipeline

with Pipeline("pipeline-one") as p1:
    p1._add_node("n1", "source_api", "Fetch A", {"url": "https://example.com/a"})

with Pipeline("pipeline-two") as p2:
    p2._add_node("n1", "source_api", "Fetch B", {"url": "https://example.com/b"})
`), 0644)
	if err != nil {
		t.Fatal(err)
	}

	result, err := CompilePythonPipeline(pyFile)
	if err != nil {
		t.Fatalf("CompilePythonPipeline: %v", err)
	}
	if len(result) != 2 {
		t.Fatalf("expected 2 pipelines, got %d", len(result))
	}
	if result[0].PipelineID != "pipeline-one" {
		t.Errorf("result[0].PipelineID = %q, want %q", result[0].PipelineID, "pipeline-one")
	}
	if result[1].PipelineID != "pipeline-two" {
		t.Errorf("result[1].PipelineID = %q, want %q", result[1].PipelineID, "pipeline-two")
	}
}
