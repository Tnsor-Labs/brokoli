package taskruntime

import (
	"os"
	"testing"
)

func TestParseResolvedExecutionRecord_RealFixtureRoundTrips(t *testing.T) {
	data, err := os.ReadFile("../../docs/schema/fixtures/resolved-execution-record-v1/positive/standard-python.json")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	rec, err := ParseResolvedExecutionRecord(data)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if rec.RuntimeProtocol != "brokoli.task-runtime/v1" {
		t.Errorf("RuntimeProtocol = %q, want brokoli.task-runtime/v1", rec.RuntimeProtocol)
	}
	if rec.PayloadID != "python-any" {
		t.Errorf("PayloadID = %q, want python-any", rec.PayloadID)
	}
	if rec.ExecutionProfile != "standard@7" {
		t.Errorf("ExecutionProfile = %q, want standard@7", rec.ExecutionProfile)
	}
}
