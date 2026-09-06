package store

import (
	"testing"

	"github.com/Tnsor-Labs/brokoli/pkg/taskruntime"
)

// ResolvedExecutionRecordStore's whole contract (ADR-033 section 4): a
// pin is scoped to (run_id, node_id), immutable once set, and never
// crosses runs or nodes.

func sampleRecord(profile string) *taskruntime.ResolvedExecutionRecord {
	return &taskruntime.ResolvedExecutionRecord{
		RuntimeProtocol:            "brokoli.task-runtime/v1",
		BundleDigest:               "sha256:" + repeatHex("a"),
		PayloadID:                  "python-any",
		PayloadDigest:              "sha256:" + repeatHex("b"),
		ExecutionEnvironmentDigest: "sha256:" + repeatHex("c"),
		InterfaceDigest:            "sha256:" + repeatHex("d"),
		ExecutionProfile:           profile,
	}
}

func repeatHex(s string) string {
	out := ""
	for i := 0; i < 64; i++ {
		out += s
	}
	return out
}

func TestResolvedExecutionRecordStore_FirstPinIsCreated(t *testing.T) {
	s := newTestStore(t)
	record := sampleRecord("standard@1")
	created, err := s.PutResolvedExecutionRecord("run-1", "task", record)
	if err != nil {
		t.Fatalf("PutResolvedExecutionRecord: %v", err)
	}
	if !created {
		t.Fatal("first pin must report created=true")
	}
	got, err := s.GetResolvedExecutionRecord("run-1", "task")
	if err != nil {
		t.Fatalf("GetResolvedExecutionRecord: %v", err)
	}
	if *got != *record {
		t.Errorf("got %+v, want %+v", got, record)
	}
}

func TestResolvedExecutionRecordStore_IdenticalRepinIsNoop(t *testing.T) {
	s := newTestStore(t)
	record := sampleRecord("standard@1")
	if _, err := s.PutResolvedExecutionRecord("run-1", "task", record); err != nil {
		t.Fatal(err)
	}
	// A second attempt (retry) resolving to the SAME record must be a
	// no-op, not an error -- this is exactly the "retry reuses the pin"
	// path in practice: the caller re-resolves, gets the same answer
	// (nothing changed), and pins it again idempotently.
	created, err := s.PutResolvedExecutionRecord("run-1", "task", sampleRecord("standard@1"))
	if err != nil {
		t.Fatalf("re-pin identical record: %v", err)
	}
	if created {
		t.Fatal("identical re-pin must report created=false")
	}
}

func TestResolvedExecutionRecordStore_DifferentRecordIsConflict(t *testing.T) {
	s := newTestStore(t)
	if _, err := s.PutResolvedExecutionRecord("run-1", "task", sampleRecord("standard@1")); err != nil {
		t.Fatal(err)
	}
	// A DIFFERENT resolution for the same (run, node) -- e.g. a bug that
	// re-resolved against a different host -- must be refused loudly,
	// never silently overwrite the pin.
	_, err := s.PutResolvedExecutionRecord("run-1", "task", sampleRecord("standard@2"))
	if err != ErrResolvedExecutionRecordConflict {
		t.Fatalf("got %v, want ErrResolvedExecutionRecordConflict", err)
	}
}

func TestResolvedExecutionRecordStore_ScopedPerRunAndNode(t *testing.T) {
	s := newTestStore(t)
	if _, err := s.PutResolvedExecutionRecord("run-1", "task-a", sampleRecord("standard@1")); err != nil {
		t.Fatal(err)
	}
	// A different node in the SAME run is a separate key.
	if _, err := s.PutResolvedExecutionRecord("run-1", "task-b", sampleRecord("standard@2")); err != nil {
		t.Fatal(err)
	}
	// The SAME node ID in a DIFFERENT run is also a separate key (a
	// retried run, or two runs of the same pipeline, must resolve
	// independently -- one run's pin never leaks into another's).
	if _, err := s.PutResolvedExecutionRecord("run-2", "task-a", sampleRecord("standard@3")); err != nil {
		t.Fatal(err)
	}
	a, err := s.GetResolvedExecutionRecord("run-1", "task-a")
	if err != nil || a.ExecutionProfile != "standard@1" {
		t.Fatalf("run-1/task-a = %+v, %v", a, err)
	}
	b, err := s.GetResolvedExecutionRecord("run-1", "task-b")
	if err != nil || b.ExecutionProfile != "standard@2" {
		t.Fatalf("run-1/task-b = %+v, %v", b, err)
	}
	c, err := s.GetResolvedExecutionRecord("run-2", "task-a")
	if err != nil || c.ExecutionProfile != "standard@3" {
		t.Fatalf("run-2/task-a = %+v, %v", c, err)
	}
}

func TestResolvedExecutionRecordStore_GetMissingIsNotFound(t *testing.T) {
	s := newTestStore(t)
	if _, err := s.GetResolvedExecutionRecord("run-1", "task"); err != ErrResolvedExecutionRecordNotFound {
		t.Fatalf("got %v, want ErrResolvedExecutionRecordNotFound", err)
	}
}

func TestResolvedExecutionRecordStore_StaticCapabilityInterface(t *testing.T) {
	s := newTestStore(t)
	var storeIntf Store = s
	if _, ok := storeIntf.(ResolvedExecutionRecordStore); !ok {
		t.Fatal("SQLite-backed TestStore is not reachable as ResolvedExecutionRecordStore; task-node retries will re-resolve fresh every attempt")
	}
}
