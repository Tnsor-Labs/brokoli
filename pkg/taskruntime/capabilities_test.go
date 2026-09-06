package taskruntime

import (
	"os"
	"testing"
)

func TestParseWorkerCapabilities_RealFixtureRoundTrips(t *testing.T) {
	data, err := os.ReadFile("../../docs/schema/fixtures/worker-capabilities-v1/positive/mixed-fleet-worker.json")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	caps, err := ParseWorkerCapabilities(data)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if caps.WorkerID != "worker-17" {
		t.Errorf("WorkerID = %q, want worker-17", caps.WorkerID)
	}
	if !caps.HasProtocol("brokoli.task-runtime/v1") {
		t.Error("expected brokoli.task-runtime/v1 protocol")
	}
	if caps.HasProtocol("brokoli.nonexistent/v9") {
		t.Error("did not expect an unadvertised protocol to match")
	}
	if !caps.HasRuntimeVersion(RuntimeClassPython, "3.11.9") {
		t.Error("expected python 3.11.9 runtime version")
	}
	if caps.HasRuntimeVersion(RuntimeClassPython, "3.10.0") {
		t.Error("did not expect an unadvertised python version to match")
	}
	if !caps.HasIO(IOModeStreamV1) {
		t.Error("expected stream-v1 io mode")
	}
	if !caps.HasIsolation("container") {
		t.Error("expected container isolation")
	}
	if caps.Resources.MemoryBytes != 17179869184 {
		t.Errorf("MemoryBytes = %d, want 17179869184", caps.Resources.MemoryBytes)
	}
}

func TestParseWorkerCapabilities_CodeNodeOnlyWorkerHasNoRuntimes(t *testing.T) {
	data, err := os.ReadFile("../../docs/schema/fixtures/worker-capabilities-v1/positive/code-node-only-worker.json")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	caps, err := ParseWorkerCapabilities(data)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(caps.Runtimes) != 0 {
		t.Errorf("expected no runtime capabilities, got %v", caps.Runtimes)
	}
	if _, ok := caps.Runtime(RuntimeClassPython); ok {
		t.Error("expected no python runtime capability")
	}
}
