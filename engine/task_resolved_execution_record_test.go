package engine

// ADR-033 rollout phase 2d: resolveExecutionRecord's own unit coverage.
// These call it directly (same package) so a scenario can be proven
// deterministically -- e.g. "reuse ignores a newly-added, otherwise-
// preferred payload" isn't observable through a real end-to-end run,
// since a real bundle's manifest never mutates mid-run.

import (
	"strings"
	"testing"

	"github.com/Tnsor-Labs/brokoli/pkg/taskbundlev2"
	"github.com/Tnsor-Labs/brokoli/store"
)

func resolveTestManifest(digest string) *taskbundlev2.Manifest {
	return &taskbundlev2.Manifest{
		Format:          taskbundlev2.Format,
		Name:            "fixture-task",
		InterfaceDigest: digest,
		SourceDigest:    digest,
		Payloads: []taskbundlev2.Payload{{
			ID:            "python-any",
			Runtime:       taskbundlev2.RuntimePython,
			OS:            "any",
			Arch:          "any",
			Entrypoint:    taskbundlev2.Entrypoint{Module: "fixture_task", Symbol: "run"},
			Effects:       taskbundlev2.EffectPure,
			PayloadDigest: digest,
		}},
	}
}

func TestResolveExecutionRecord_FirstCallPinsAndPersists(t *testing.T) {
	skipIfNoPython3(t)
	s := newExpansionTestStore(t, "resolve-record-first")
	real := s.(*store.SQLiteStore)
	digest := "sha256:" + strings.Repeat("0", 62) + "aa"
	manifest := resolveTestManifest(digest)

	payload, record, err := resolveExecutionRecord(real, "run-1", "task", digest, manifest)
	if err != nil {
		t.Fatalf("resolveExecutionRecord: %v", err)
	}
	if payload.ID != "python-any" {
		t.Errorf("payload.ID = %q, want python-any", payload.ID)
	}
	if record == nil || record.PayloadID != "python-any" || record.ExecutionEnvironmentDigest == "" {
		t.Fatalf("record = %+v, want a populated pin", record)
	}

	pinned, err := real.GetResolvedExecutionRecord("run-1", "task")
	if err != nil {
		t.Fatalf("the pin was not persisted: %v", err)
	}
	if *pinned != *record {
		t.Errorf("persisted pin = %+v, want %+v", pinned, record)
	}
}

// TestResolveExecutionRecord_RetryReusesThePinEvenWhenAPreferredPayloadAppears
// is the real determinism proof (ADR-033 section 4 rule 5, "a newer
// interpreter, adapter, or bundle does not silently change an existing
// run"): after the first resolution pins "python-any", a second,
// otherwise-more-eligible payload is added to the front of the SAME
// manifest -- a fresh SelectPythonPayload call would prefer it. The
// second resolveExecutionRecord call must still return the ORIGINALLY
// pinned payload, proving it reused the pin instead of re-selecting.
func TestResolveExecutionRecord_RetryReusesThePinEvenWhenAPreferredPayloadAppears(t *testing.T) {
	skipIfNoPython3(t)
	s := newExpansionTestStore(t, "resolve-record-reuse")
	real := s.(*store.SQLiteStore)
	digest := "sha256:" + strings.Repeat("0", 62) + "aa"
	manifest := resolveTestManifest(digest)

	first, firstRecord, err := resolveExecutionRecord(real, "run-1", "task", digest, manifest)
	if err != nil {
		t.Fatalf("first resolve: %v", err)
	}
	if first.ID != "python-any" {
		t.Fatalf("first.ID = %q, want python-any", first.ID)
	}

	// Simulate the manifest "changing shape" between attempts by
	// prepending a payload SelectPythonPayload would pick first if asked
	// fresh -- a real bundle's manifest can't mutate (content-addressed),
	// but this stands in for "a different payload becomes eligible" the
	// way ADR-033 section 4 rule 5 actually worries about (a worker-fleet
	// change between retries, not the bundle itself changing).
	manifest.Payloads = append([]taskbundlev2.Payload{{
		ID: "python-preferred", Runtime: taskbundlev2.RuntimePython, OS: "any", Arch: "any",
		Entrypoint: taskbundlev2.Entrypoint{Module: "fixture_task", Symbol: "run"},
		Effects:    taskbundlev2.EffectPure, PayloadDigest: digest,
	}}, manifest.Payloads...)

	second, secondRecord, err := resolveExecutionRecord(real, "run-1", "task", digest, manifest)
	if err != nil {
		t.Fatalf("second (retry) resolve: %v", err)
	}
	if second.ID != "python-any" {
		t.Fatalf("retry re-resolved to %q instead of reusing the pinned %q -- the determinism guarantee is broken", second.ID, first.ID)
	}
	if *secondRecord != *firstRecord {
		t.Errorf("second record = %+v, want the identical first record %+v", secondRecord, firstRecord)
	}
}

func TestResolveExecutionRecord_PlatformMismatchOnReuseFailsClearly(t *testing.T) {
	skipIfNoPython3(t)
	s := newExpansionTestStore(t, "resolve-record-platform")
	real := s.(*store.SQLiteStore)
	digest := "sha256:" + strings.Repeat("0", 62) + "aa"
	manifest := resolveTestManifest(digest)

	if _, _, err := resolveExecutionRecord(real, "run-1", "task", digest, manifest); err != nil {
		t.Fatalf("first resolve: %v", err)
	}

	// The pinned payload's own declared platform changes to something
	// this host can never satisfy -- standing in for "the pinned payload
	// is no longer runnable here" (ADR-033 section 4: "the run waits or
	// fails with platform").
	manifest.Payloads[0].OS = "plan9"
	manifest.Payloads[0].Arch = "mips"

	_, _, err := resolveExecutionRecord(real, "run-1", "task", digest, manifest)
	if err == nil {
		t.Fatal("expected a platform mismatch on reuse to be refused")
	}
	if !strings.Contains(err.Error(), "platform:") {
		t.Errorf("error does not name the platform category: %v", err)
	}
}

func TestResolveExecutionRecord_MissingPinnedPayloadIsRefused(t *testing.T) {
	skipIfNoPython3(t)
	s := newExpansionTestStore(t, "resolve-record-missing-payload")
	real := s.(*store.SQLiteStore)
	digest := "sha256:" + strings.Repeat("0", 62) + "aa"
	manifest := resolveTestManifest(digest)

	if _, _, err := resolveExecutionRecord(real, "run-1", "task", digest, manifest); err != nil {
		t.Fatalf("first resolve: %v", err)
	}
	manifest.Payloads[0].ID = "renamed"

	_, _, err := resolveExecutionRecord(real, "run-1", "task", digest, manifest)
	if err == nil || !strings.Contains(err.Error(), "not in this bundle's manifest") {
		t.Fatalf("expected a clear 'not in manifest' refusal, got: %v", err)
	}
}

func TestResolveExecutionRecord_DegradesGracefullyWithoutTheStoreCapability(t *testing.T) {
	skipIfNoPython3(t)
	digest := "sha256:" + strings.Repeat("0", 62) + "aa"
	manifest := resolveTestManifest(digest)

	// A plain non-ResolvedExecutionRecordStore store (nil here stands in
	// for "any store type that hasn't adopted the capability") must
	// still resolve successfully -- fresh every time, no pin, exactly
	// the pre-phase-2d behavior.
	payload, record, err := resolveExecutionRecord(nil, "run-1", "task", digest, manifest)
	if err != nil {
		t.Fatalf("resolveExecutionRecord without the store capability: %v", err)
	}
	if payload == nil || payload.ID != "python-any" {
		t.Fatalf("payload = %+v, want python-any", payload)
	}
	if record != nil {
		t.Errorf("record = %+v, want nil (nothing to pin without the capability)", record)
	}
}
