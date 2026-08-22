package cmd

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// fakeCgroup points the readers at a temp dir describing a container with
// the given limit and current usage.
func fakeCgroup(t *testing.T, limit, current int64) {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "memory.max"), []byte(itoa(limit)+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "memory.current"), []byte(itoa(current)+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	prev := cgroupMemoryV2Dir
	cgroupMemoryV2Dir = dir
	t.Cleanup(func() { cgroupMemoryV2Dir = prev })
}

func itoa(v int64) string {
	if v == 0 {
		return "0"
	}
	neg := v < 0
	if neg {
		v = -v
	}
	var b []byte
	for v > 0 {
		b = append([]byte{byte('0' + v%10)}, b...)
		v /= 10
	}
	if neg {
		return "-" + string(b)
	}
	return string(b)
}

// With most of the limit free, a worker admits back-to-back. The settle
// delay used to apply here too, capping throughput at one run per three
// seconds per worker no matter how small the runs were.
func TestAdmissionImmediateWhenMemoryIsComfortable(t *testing.T) {
	fakeCgroup(t, 1<<30, 100<<20) // 1 GiB limit, 100 MiB used

	admit, wait := admissionDecision(time.Now())
	if !admit {
		t.Fatalf("expected immediate admission with abundant headroom, waited %s", wait)
	}
}

// Near the ceiling the settle delay still applies: a cgroup reading
// cannot see a job admitted a moment ago.
func TestAdmissionWaitsOutSettleDelayWhenClose(t *testing.T) {
	limit := int64(1 << 30)
	margin := int64(float64(limit) * memoryBackpressureSafetyMarginFraction) // 204 MiB
	// Headroom just above the margin but below the comfort multiple.
	fakeCgroup(t, limit, limit-(margin+(10<<20)))

	admit, wait := admissionDecision(time.Now())
	if admit {
		t.Fatal("expected the settle delay to hold admission near the ceiling")
	}
	if wait <= 0 || wait > memoryBackpressureSettleDelay {
		t.Fatalf("expected a wait within the settle delay, got %s", wait)
	}
}

// Once the delay has elapsed, the same tight-but-not-critical worker
// admits again.
func TestAdmissionResumesAfterSettleDelayElapses(t *testing.T) {
	limit := int64(1 << 30)
	margin := int64(float64(limit) * memoryBackpressureSafetyMarginFraction)
	fakeCgroup(t, limit, limit-(margin+(10<<20)))

	admit, _ := admissionDecision(time.Now().Add(-2 * memoryBackpressureSettleDelay))
	if !admit {
		t.Fatal("expected admission once the settle delay had passed")
	}
}

// Genuinely out of headroom: refuse and re-check on the interval, which
// is the behaviour that protects the pod from the OOM killer.
func TestAdmissionRefusedWhenOutOfHeadroom(t *testing.T) {
	fakeCgroup(t, 1<<30, (1<<30)-(1<<20)) // 1 MiB free

	admit, wait := admissionDecision(time.Now().Add(-time.Hour))
	if admit {
		t.Fatal("expected admission to be refused with no headroom")
	}
	if wait != memoryBackpressureRecheckInterval {
		t.Fatalf("expected the re-check interval, got %s", wait)
	}
}

// The waited time is the remainder of the delay, not a flat interval —
// rounding a 3s delay up to the 2s re-check grid cost a full extra second
// per admission.
func TestAdmissionWaitIsTheRemainingDelayNotTheRecheckGrid(t *testing.T) {
	limit := int64(1 << 30)
	margin := int64(float64(limit) * memoryBackpressureSafetyMarginFraction)
	fakeCgroup(t, limit, limit-(margin+(10<<20)))

	elapsed := memoryBackpressureSettleDelay - 500*time.Millisecond
	_, wait := admissionDecision(time.Now().Add(-elapsed))
	if wait > 600*time.Millisecond {
		t.Fatalf("expected roughly the remaining 500ms, got %s", wait)
	}
}

// An environment without a readable cgroup behaves exactly as before this
// gate existed.
func TestAdmissionUnrestrictedWithoutCgroup(t *testing.T) {
	prev := cgroupMemoryV2Dir
	cgroupMemoryV2Dir = filepath.Join(t.TempDir(), "absent")
	t.Cleanup(func() { cgroupMemoryV2Dir = prev })

	prevV1 := cgroupMemoryV1Dir
	cgroupMemoryV1Dir = filepath.Join(t.TempDir(), "absent")
	t.Cleanup(func() { cgroupMemoryV1Dir = prevV1 })

	if admit, _ := admissionDecision(time.Now()); !admit {
		t.Fatal("expected admission where memory cannot be measured")
	}
}
