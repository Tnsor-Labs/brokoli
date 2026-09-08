package nodeharness

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Tnsor-Labs/brokoli/pkg/taskharness"
	"github.com/Tnsor-Labs/brokoli/pkg/taskharness/conformance"
)

// The shared behaviour suite every reference adapter must satisfy
// (ADR-033 section 19). Cases live in pkg/taskharness/conformance; only
// the plumbing to ask this adapter is here.
func TestConformance(t *testing.T) {
	node := nodeBinary(t)
	conformance.Run(t, conformance.Node, func(t *testing.T, c conformance.Case) (taskharness.Result, []byte) {
		t.Helper()
		root, module := buildFixtureBundle(t, c.Source[conformance.Node])
		harnessDir := t.TempDir()
		harnessPath, err := Materialize(harnessDir)
		if err != nil {
			t.Fatalf("Materialize: %v", err)
		}
		attemptDir := t.TempDir()
		resultPath := filepath.Join(attemptDir, "result.json")
		invocationPath := filepath.Join(attemptDir, "invocation.json")

		inv := Invocation{
			ModuleRoots:     []string{root},
			Module:          module,
			Symbol:          "run",
			InterfaceDigest: "sha256:" + strings.Repeat("0", 62) + "aa",
			OutputKind:      c.OutputKind,
			OutputMediaType: c.OutputMediaType,
		}
		if c.InputNDJSON != "" {
			p := filepath.Join(attemptDir, "input.ndjson")
			if err := os.WriteFile(p, []byte(c.InputNDJSON), 0o600); err != nil {
				t.Fatalf("stage input: %v", err)
			}
			inv.InputPath, inv.InputCodec = p, "ndjson/v1"
		}
		if err := WriteInvocation(invocationPath, inv); err != nil {
			t.Fatalf("WriteInvocation: %v", err)
		}

		start := taskharness.NewStartFrame(invocationPath, resultPath, filepath.Join(attemptDir, "out"))
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		res, err := taskharness.Run(ctx, start, taskharness.Options{
			Command: Command(node, harnessPath, 0),
		}, taskharness.Handlers{})
		if err != nil {
			t.Fatalf("harness run: %v", err)
		}
		raw, _ := os.ReadFile(resultPath)
		return res, raw
	})
}
