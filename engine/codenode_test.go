package engine

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Tnsor-Labs/brokoli/pkg/codeexec"
	"github.com/Tnsor-Labs/brokoli/pkg/common"
)

func TestCodeNode_TypeScript(t *testing.T) {
	if !codeexec.PoolEnabled() {
		t.Skip("TypeScript requires the worker pool")
	}
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is not installed")
	}
	ds := &common.DataSet{Columns: []string{"id"}, Rows: []common.DataRow{{"id": float64(2)}}}
	result, stderr, err := ExecuteCodeNode(`
    console.log("typescript ran");
    await sleep(1);
    output_data = { columns: ["id"], rows: rows.map(row => ({ id: row.id * config.multiplier })) };
  `, ds, map[string]interface{}{"language": "typescript", "node_path": node, "multiplier": float64(3)}, nil, 10)
	if err != nil {
		t.Fatalf("TypeScript execution failed: %v\nstderr: %s", err, stderr)
	}
	if len(result.Rows) != 1 || result.Rows[0]["id"] != float64(6) || !strings.Contains(stderr, "typescript ran") {
		t.Fatalf("TypeScript result wrong: result=%+v stderr=%q", result, stderr)
	}
}

func TestCodeNode_TypeScriptCancellationKillsBusyWorker(t *testing.T) {
	if !codeexec.PoolEnabled() {
		t.Skip("TypeScript requires the worker pool")
	}
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is not installed")
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, _, err := ExecuteCodeNodeContext(ctx, `await sleep(60000);`, nil,
			map[string]interface{}{"language": "typescript", "node_path": node}, nil, 120)
		done <- err
	}()
	time.Sleep(100 * time.Millisecond)
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancellation error = %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("busy TypeScript worker ignored cancellation")
	}
}

func TestCodeNode_TypeScriptStreamed(t *testing.T) {
	if !codeexec.PoolEnabled() {
		t.Skip("TypeScript requires the worker pool")
	}
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is not installed")
	}
	input := filepath.Join(t.TempDir(), "input.ndjson")
	if err := os.WriteFile(input, []byte("{\"id\":1}\n{\"id\":2}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	result, err := executeCodeNodeStreamed(context.Background(), `
    begin_emit(["id"]);
    for await (const row of rowsStream()) emit({ id: row.id * 2 });
  `, input, []string{"id"}, map[string]interface{}{"language": "typescript", "node_path": node}, nil, 10, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Remove(result.outputPath) })
	got, err := os.ReadFile(result.outputPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "{\"id\":2}\n{\"id\":4}\n" {
		t.Fatalf("streamed TypeScript output = %q", got)
	}
}

func TestCodeNode_LegacyStreamedCancellation(t *testing.T) {
	t.Setenv("BROKOLI_CODE_POOL", "0")
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := executeCodeNodeStreamed(ctx, "import time\ntime.sleep(60)", "", nil, nil, nil, 120, nil)
		done <- err
	}()
	time.Sleep(100 * time.Millisecond)
	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("legacy streamed execution returned no cancellation error")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("legacy streamed execution ignored cancellation")
	}
}

func TestCodeNode_Passthrough(t *testing.T) {
	ds := &common.DataSet{
		Columns: []string{"id", "name"},
		Rows: []common.DataRow{
			{"id": "1", "name": "Alice"},
			{"id": "2", "name": "Bob"},
		},
	}

	// Script that just passes through
	script := `# passthrough - output_data is already set to input`
	result, stderr, err := ExecuteCodeNode(script, ds, nil, nil, 10)
	if err != nil {
		t.Fatalf("unexpected error: %v\nstderr: %s", err, stderr)
	}
	if len(result.Rows) != 2 {
		t.Errorf("expected 2 rows, got %d", len(result.Rows))
	}
}

func TestCodeNode_FilterAndTransform(t *testing.T) {
	ds := &common.DataSet{
		Columns: []string{"id", "amount"},
		Rows: []common.DataRow{
			{"id": "1", "amount": "100"},
			{"id": "2", "amount": "-50"},
			{"id": "3", "amount": "200"},
		},
	}

	script := `
filtered = [r for r in rows if float(r.get("amount", 0)) > 0]
for r in filtered:
    r["doubled"] = str(float(r["amount"]) * 2)
output_data = {"columns": columns + ["doubled"], "rows": filtered}
`
	result, _, err := ExecuteCodeNode(script, ds, nil, nil, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Rows) != 2 {
		t.Errorf("expected 2 rows after filter, got %d", len(result.Rows))
	}
	if len(result.Columns) != 3 {
		t.Errorf("expected 3 columns, got %d", len(result.Columns))
	}
}

func TestCodeNode_AccessParams(t *testing.T) {
	ds := &common.DataSet{
		Columns: []string{"id"},
		Rows:    []common.DataRow{{"id": "1"}},
	}

	script := `
for r in rows:
    r["source"] = params.get("source_name", "unknown")
output_data = {"columns": columns + ["source"], "rows": rows}
`
	result, _, err := ExecuteCodeNode(script, ds, nil, map[string]string{"source_name": "test_run"}, 10)
	if err != nil {
		t.Fatal(err)
	}
	if result.Rows[0]["source"] != "test_run" {
		t.Errorf("expected 'test_run', got %v", result.Rows[0]["source"])
	}
}

func TestCodeNode_Pandas(t *testing.T) {
	ds := &common.DataSet{
		Columns: []string{"name", "score"},
		Rows: []common.DataRow{
			{"name": "Alice", "score": "90"},
			{"name": "Bob", "score": "85"},
			{"name": "Alice", "score": "95"},
		},
	}

	// Try pandas — skip test if not installed
	script := `
try:
    import pandas as pd
    df = pd.DataFrame(rows)
    df["score"] = pd.to_numeric(df["score"])
    result = df.groupby("name")["score"].mean().reset_index()
    result.columns = ["name", "avg_score"]
    output_data = {"columns": list(result.columns), "rows": result.to_dict("records")}
except ImportError:
    # pandas not available — just pass through
    output_data = {"columns": columns, "rows": rows}
`
	result, _, err := ExecuteCodeNode(script, ds, nil, nil, 15)
	if err != nil {
		t.Fatal(err)
	}
	// Should work whether pandas is installed or not
	if len(result.Rows) < 1 {
		t.Error("expected at least 1 row")
	}
}

func TestCodeNode_ScriptError(t *testing.T) {
	ds := &common.DataSet{Columns: []string{"id"}, Rows: []common.DataRow{{"id": "1"}}}

	script := `raise ValueError("intentional error")`
	_, stderr, err := ExecuteCodeNode(script, ds, nil, nil, 10)
	if err == nil {
		t.Error("expected error from failing script")
	}
	_ = stderr // should contain the traceback
}

func TestCodeNode_EmptyScript(t *testing.T) {
	ds := &common.DataSet{Columns: []string{"id"}, Rows: []common.DataRow{{"id": "1"}}}
	_, _, err := ExecuteCodeNode("", ds, nil, nil, 10)
	if err == nil {
		t.Error("expected error for empty script")
	}
}

func TestCodeNode_Stderr(t *testing.T) {
	ds := &common.DataSet{Columns: []string{"id"}, Rows: []common.DataRow{{"id": "1"}}}

	script := `
import sys
print("this is a warning", file=sys.stderr)
# output_data stays as default passthrough
`
	result, stderr, err := ExecuteCodeNode(script, ds, nil, nil, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Rows) != 1 {
		t.Error("should still produce output")
	}
	if stderr == "" {
		t.Error("expected stderr output")
	}
}
