package common

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"
)

// TestSLogCorrelationFields verifies the run/node/attempt correlation
// helpers attach a consistently-spelled key, which is what actually lets an
// operator filter logs by run_id/node_id/attempt across API, scheduler,
// executor, and recovery output (Tnsor-Labs/brokoli#11's acceptance
// criterion).
func TestSLogCorrelationFields(t *testing.T) {
	var buf bytes.Buffer
	testLogger := slog.New(slog.NewJSONHandler(&buf, nil))
	orig := SLog()
	SetStructuredLogger(testLogger)
	defer SetStructuredLogger(orig)

	SLog().Info("node attempt started",
		RunAttr("run-123"), NodeAttr("node-abc"), AttemptAttr(2),
		PipelineAttr("pipe-xyz"), TraceAttr("trace-1"))

	var decoded map[string]any
	if err := json.Unmarshal(buf.Bytes(), &decoded); err != nil {
		t.Fatalf("decode json log line: %v (raw: %s)", err, buf.String())
	}

	checks := map[string]any{
		"run_id":      "run-123",
		"node_id":     "node-abc",
		"attempt":     float64(2), // JSON numbers decode as float64
		"pipeline_id": "pipe-xyz",
		"trace_id":    "trace-1",
	}
	for key, want := range checks {
		got, ok := decoded[key]
		if !ok {
			t.Errorf("expected key %q in log output, got: %v", key, decoded)
			continue
		}
		if got != want {
			t.Errorf("key %q = %v, want %v", key, got, want)
		}
	}
	if decoded["msg"] != "node attempt started" {
		t.Errorf("msg = %v, want %q", decoded["msg"], "node attempt started")
	}
}

// TestHolderAndGenerationAttr covers the leader-election correlation
// helpers used by store/postgres_leader.go.
func TestHolderAndGenerationAttr(t *testing.T) {
	var buf bytes.Buffer
	testLogger := slog.New(slog.NewTextHandler(&buf, nil))
	orig := SLog()
	SetStructuredLogger(testLogger)
	defer SetStructuredLogger(orig)

	SLog().Info("leader: acquired leadership", HolderAttr("host-1:123:abcd"), GenerationAttr(7))

	out := buf.String()
	if !strings.Contains(out, `holder=host-1:123:abcd`) {
		t.Errorf("expected holder field in output, got: %s", out)
	}
	if !strings.Contains(out, `generation=7`) {
		t.Errorf("expected generation field in output, got: %s", out)
	}
}

// TestStructuredLevelFromEnv exercises the BROKOLI_LOG_LEVEL parsing table
// directly (avoids depending on process env in the rest of the suite).
func TestStructuredLevelFromEnv(t *testing.T) {
	cases := []struct {
		env  string
		want slog.Level
	}{
		{"DEBUG", slog.LevelDebug},
		{"debug", slog.LevelDebug},
		{"WARN", slog.LevelWarn},
		{"WARNING", slog.LevelWarn},
		{"ERROR", slog.LevelError},
		{"INFO", slog.LevelInfo},
		{"", slog.LevelInfo},
		{"bogus", slog.LevelInfo},
	}
	for _, tc := range cases {
		t.Setenv("BROKOLI_LOG_LEVEL", tc.env)
		if got := structuredLevelFromEnv(); got != tc.want {
			t.Errorf("structuredLevelFromEnv() with BROKOLI_LOG_LEVEL=%q = %v, want %v", tc.env, got, tc.want)
		}
	}
}
