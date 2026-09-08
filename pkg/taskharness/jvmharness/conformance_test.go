package jvmharness

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
// (ADR-033 section 19). The cases live in pkg/taskharness/conformance;
// only the plumbing to ask this adapter is here, because the invocation
// descriptor is deliberately per-adapter.
func TestConformance(t *testing.T) {
	tc := toolchain(t)
	conformance.Run(t, conformance.JVM, func(t *testing.T, c conformance.Case) (taskharness.Result, []byte) {
		t.Helper()
		cp := buildTaskClass(t, tc, c.Source[conformance.JVM])
		classDir, err := Materialize(t.TempDir(), tc)
		if err != nil {
			t.Fatalf("Materialize: %v", err)
		}
		attemptDir := t.TempDir()
		resultPath := filepath.Join(attemptDir, "result.json")
		invocationPath := filepath.Join(attemptDir, "invocation.json")

		inv := Invocation{
			Classpath:       []string{cp},
			ClassName:       "FixtureTask",
			MethodName:      "run",
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
			Command: Command(tc.Java, classDir, 0),
		}, taskharness.Handlers{})
		if err != nil {
			t.Fatalf("harness run: %v", err)
		}
		raw, _ := os.ReadFile(resultPath)
		return res, raw
	})
}
