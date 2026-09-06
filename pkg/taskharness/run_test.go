package taskharness

import (
	"context"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/Tnsor-Labs/brokoli/pkg/proctree"
)

func newStart(t *testing.T) StartFrame {
	t.Helper()
	dir := t.TempDir()
	return NewStartFrame(dir+"/invocation", dir+"/result.json", dir)
}

func TestRun_HappyPath(t *testing.T) {
	cmd, env := fakeHarnessCommand("ready|log:info:working|completed")
	var readyCalls int
	var logs []Log
	res, err := Run(context.Background(), newStart(t), Options{Command: cmd, Env: env}, Handlers{
		OnReady: func(r Ready) error { readyCalls++; return nil },
		OnLog:   func(l Log) { logs = append(logs, l) },
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Failure != nil {
		t.Fatalf("expected success, got failure: %+v", res.Failure)
	}
	if readyCalls != 1 {
		t.Errorf("OnReady called %d times, want 1", readyCalls)
	}
	if len(logs) != 1 || logs[0].Message != "working" {
		t.Errorf("logs = %+v, want one 'working' log", logs)
	}
}

func TestRun_FailedFrame(t *testing.T) {
	cmd, env := fakeHarnessCommand("ready|failed:user_code:boom")
	res, err := Run(context.Background(), newStart(t), Options{Command: cmd, Env: env}, Handlers{})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Failure == nil {
		t.Fatal("expected a failure result")
	}
	if res.Failure.Category != FailureUserCode || res.Failure.Message != "boom" {
		t.Errorf("Failure = %+v, want category=user_code message=boom", res.Failure)
	}
}

func TestRun_HandshakeTimeout(t *testing.T) {
	cmd, env := fakeHarnessCommand("sleep:2s|ready|completed")
	res, err := Run(context.Background(), newStart(t), Options{
		Command:          cmd,
		Env:              env,
		HandshakeTimeout: 100 * time.Millisecond,
		TerminationGrace: 200 * time.Millisecond,
	}, Handlers{})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Failure == nil || res.Failure.Code != "handshake_timeout" {
		t.Fatalf("Failure = %+v, want code=handshake_timeout", res.Failure)
	}
}

func TestRun_OutputBeforeReady(t *testing.T) {
	cmd, env := fakeHarnessCommand(`raw:{"type":"log","level":"info","message":"too early"}|ready|completed`)
	res, err := Run(context.Background(), newStart(t), Options{Command: cmd, Env: env}, Handlers{})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Failure == nil || res.Failure.Code != "output_before_ready" {
		t.Fatalf("Failure = %+v, want code=output_before_ready", res.Failure)
	}
}

func TestRun_MalformedJSONIsAFrameDecodeError(t *testing.T) {
	cmd, env := fakeHarnessCommand(`raw:not json at all|ready|completed`)
	res, err := Run(context.Background(), newStart(t), Options{Command: cmd, Env: env}, Handlers{})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Failure == nil || res.Failure.Code != "frame_decode_error" {
		t.Fatalf("Failure = %+v, want code=frame_decode_error", res.Failure)
	}
}

func TestRun_DuplicateKeyIsAFrameDecodeError(t *testing.T) {
	cmd, env := fakeHarnessCommand(`raw:{"type":"ready","type":"ready","protocol":"brokoli.task-runtime/v1","adapter":"a","adapter_version":"1","capabilities":[]}`)
	res, err := Run(context.Background(), newStart(t), Options{Command: cmd, Env: env}, Handlers{})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Failure == nil || res.Failure.Code != "frame_decode_error" {
		t.Fatalf("Failure = %+v, want code=frame_decode_error (duplicate key)", res.Failure)
	}
	if !strings.Contains(res.Failure.Message, "duplicate key") {
		t.Errorf("Message = %q, want it to name the duplicate key", res.Failure.Message)
	}
}

func TestRun_ReadyRejectedByHandler(t *testing.T) {
	cmd, env := fakeHarnessCommand("ready|completed")
	res, err := Run(context.Background(), newStart(t), Options{Command: cmd, Env: env}, Handlers{
		OnReady: func(r Ready) error { return errCapabilityMissing },
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Failure == nil || res.Failure.Code != "ready_rejected" {
		t.Fatalf("Failure = %+v, want code=ready_rejected", res.Failure)
	}
}

var errCapabilityMissing = &testCapabilityError{}

type testCapabilityError struct{}

func (e *testCapabilityError) Error() string { return "missing required capability" }

func TestRun_FrameAfterTerminalOverridesTheResult(t *testing.T) {
	cmd, env := fakeHarnessCommand(`ready|completed|log:info:too late`)
	res, err := Run(context.Background(), newStart(t), Options{Command: cmd, Env: env}, Handlers{})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Failure == nil || res.Failure.Code != "frame_after_terminal" {
		t.Fatalf("Failure = %+v, want code=frame_after_terminal", res.Failure)
	}
}

func TestRun_NonzeroExitAfterCompletedIsAProtocolFailure(t *testing.T) {
	cmd, env := fakeHarnessCommand(`ready|completed|exit:7`)
	res, err := Run(context.Background(), newStart(t), Options{Command: cmd, Env: env}, Handlers{})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Failure == nil || res.Failure.Code != "nonzero_exit_after_completed" {
		t.Fatalf("Failure = %+v, want code=nonzero_exit_after_completed", res.Failure)
	}
}

func TestRun_ExitsBeforeAnyTerminalFrame(t *testing.T) {
	cmd, env := fakeHarnessCommand(`ready|exit:0`)
	res, err := Run(context.Background(), newStart(t), Options{Command: cmd, Env: env}, Handlers{})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Failure == nil || res.Failure.Code != "exited_before_terminal" {
		t.Fatalf("Failure = %+v, want code=exited_before_terminal", res.Failure)
	}
}

func TestRun_CancellationAfterReadyReceivesCancelAck(t *testing.T) {
	cmd, env := fakeHarnessCommand("ready|read_cancel_ack")
	ctx, cancel := context.WithCancel(context.Background())
	res, err := Run(ctx, newStart(t), Options{Command: cmd, Env: env, TerminationGrace: 2 * time.Second}, Handlers{
		OnReady: func(r Ready) error { cancel(); return nil },
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Failure == nil || res.Failure.Category != FailureCancelled || res.Failure.Code != "cancelled" {
		t.Fatalf("Failure = %+v, want category=cancelled code=cancelled", res.Failure)
	}
}

func TestRun_CancellationGraceExpiresEscalatesToKill(t *testing.T) {
	cmd, env := fakeHarnessCommand("ready|sleep:10s") // never acks, never exits on its own
	ctx, cancel := context.WithCancel(context.Background())
	start := time.Now()
	res, err := Run(ctx, newStart(t), Options{Command: cmd, Env: env, TerminationGrace: 300 * time.Millisecond}, Handlers{
		OnReady: func(r Ready) error { cancel(); return nil },
	})
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Failure == nil || res.Failure.Code != "cancel_grace_expired" {
		t.Fatalf("Failure = %+v, want code=cancel_grace_expired", res.Failure)
	}
	if elapsed > 5*time.Second {
		t.Errorf("Run took %s to escalate past a 300ms grace period -- the kill fallback isn't bounding this", elapsed)
	}
}

func TestRun_CancellationBeforeReadyTerminatesDirectly(t *testing.T) {
	cmd, env := fakeHarnessCommand("sleep:10s|ready|completed")
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // already cancelled before Run starts
	res, err := Run(ctx, newStart(t), Options{Command: cmd, Env: env, TerminationGrace: 500 * time.Millisecond}, Handlers{})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Failure == nil {
		t.Fatal("expected a failure result for a harness cancelled before it ever became ready")
	}
}

// TestRun_CPULimitIsEnforcedAndReclassified is the resource-ceiling
// test the ADR-033 Phase 2a plan calls for: a CPU-bound harness under a
// tight rlimit gets killed well before it could otherwise run forever,
// and the generic exit is reclassified as resource_exhausted (mirroring
// engine.signalBreachError's approach for code nodes) rather than left
// as an uninformative exited_before_terminal.
func TestRun_CPULimitIsEnforcedAndReclassified(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("rlimit CPU enforcement is Linux-only (pkg/proctree)")
	}
	cmd, env := fakeHarnessCommand("ready|spin")
	start := time.Now()
	res, err := Run(context.Background(), newStart(t), Options{
		Command: cmd,
		Env:     env,
		Rlimits: proctree.Rlimits{CPUSeconds: 1},
	}, Handlers{})
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Failure == nil || res.Failure.Category != FailureResourceExhausted {
		t.Fatalf("Failure = %+v, want category=resource_exhausted", res.Failure)
	}
	if elapsed > 10*time.Second {
		t.Errorf("CPU-limited harness took %s to be killed -- the rlimit doesn't appear to be applied", elapsed)
	}
}
