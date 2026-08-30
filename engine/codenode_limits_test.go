//go:build unix

package engine

import (
	"strings"
	"testing"

	"github.com/Tnsor-Labs/brokoli/pkg/common"
)

// ADR-029 P0: resource ceilings on the code-node subprocess, live
// against real python3 like the rest of the code-node suite.

func TestCodeNode_MemoryLimitBreachNamesTheLimit(t *testing.T) {
	// Allocate far past the ceiling in modest chunks; RLIMIT_AS turns
	// the allocation into MemoryError wherever it lands (user script or,
	// under a tight limit, a library import — either maps the same).
	script := `
chunks = []
for _ in range(64):
    chunks.append(bytearray(64 * 1024 * 1024))
`
	input := &common.DataSet{Columns: []string{"a"}, Rows: []common.DataRow{{"a": 1}}}
	_, _, err := ExecuteCodeNode(script, input, map[string]interface{}{"max_memory_mb": float64(384)}, nil, 60)
	if err == nil {
		t.Fatal("expected a memory-limit failure")
	}
	if !strings.Contains(err.Error(), "memory limit (384 MiB") {
		t.Fatalf("error does not name the memory limit: %v", err)
	}
}

func TestCodeNode_CPULimitBreachNamesTheLimit(t *testing.T) {
	script := `
while True:
    pass
`
	input := &common.DataSet{Columns: []string{"a"}, Rows: []common.DataRow{{"a": 1}}}
	// Wall timeout far above the CPU limit so the CPU ceiling wins.
	_, _, err := ExecuteCodeNode(script, input, map[string]interface{}{"max_cpu_seconds": float64(1)}, nil, 30)
	if err == nil {
		t.Fatal("expected a CPU-limit failure")
	}
	if !strings.Contains(err.Error(), "CPU limit (1 s)") {
		t.Fatalf("error does not name the CPU limit: %v", err)
	}
}

func TestCodeNode_RunsNormallyUnderLimits(t *testing.T) {
	script := `output_data = {"columns": columns, "rows": [{"a": r["a"] * 2} for r in rows]}`
	input := &common.DataSet{Columns: []string{"a"}, Rows: []common.DataRow{{"a": float64(21)}}}
	ds, _, err := ExecuteCodeNode(script, input, map[string]interface{}{
		"max_memory_mb":   float64(512),
		"max_cpu_seconds": float64(30),
	}, nil, 30)
	if err != nil {
		t.Fatalf("script under limits failed: %v", err)
	}
	if len(ds.Rows) != 1 {
		t.Fatalf("unexpected output: %+v", ds)
	}
}

func TestCodeNode_UnlimitedByDefaultKeepsWorking(t *testing.T) {
	// No limits configured: the preamble must be a no-op, not a floor.
	script := `output_data = {"columns": columns, "rows": rows}`
	input := &common.DataSet{Columns: []string{"a"}, Rows: []common.DataRow{{"a": float64(1)}}}
	if _, _, err := ExecuteCodeNode(script, input, map[string]interface{}{}, nil, 30); err != nil {
		t.Fatalf("passthrough with no limits failed: %v", err)
	}
}
