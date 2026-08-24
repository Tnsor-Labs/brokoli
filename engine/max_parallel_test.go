package engine

import (
	"math"
	"runtime"
	"runtime/debug"
	"testing"
)

// The old behaviour was the constant 4 regardless of the machine. On any
// realistic worker the default should be well above that — four
// independent extracts leaving the rest queued is not a production
// setting.
func TestDefaultMaxParallelNodesIsNotFour(t *testing.T) {
	prev := debug.SetMemoryLimit(-1)
	debug.SetMemoryLimit(8 << 30) // 8 GiB: a normally-sized worker
	defer debug.SetMemoryLimit(prev)

	got := defaultMaxParallelNodes(nil)
	if got <= 4 {
		t.Fatalf("expected a production-scale default, got %d", got)
	}
	if want := 4 * runtime.GOMAXPROCS(0); got != clampInt(want, minMaxParallelNodes, maxMaxParallelNodes) {
		t.Fatalf("expected the CPU-derived value %d, got %d", clampInt(want, minMaxParallelNodes, maxMaxParallelNodes), got)
	}
}

// A small memory budget still wins, because concurrent nodes hold their
// datasets at the same time.
func TestDefaultMaxParallelNodesClampedByMemory(t *testing.T) {
	prev := debug.SetMemoryLimit(-1)
	debug.SetMemoryLimit(256 << 20) // 256 MiB
	defer debug.SetMemoryLimit(prev)

	got := defaultMaxParallelNodes(nil)
	if got > int((256<<20)/int64(DefaultSpillThresholdBytes)) {
		t.Fatalf("memory clamp not applied: got %d", got)
	}
	if got < 1 {
		t.Fatalf("clamp must never reach zero, got %d", got)
	}
}

// Never zero, however tight the budget.
func TestDefaultMaxParallelNodesNeverZero(t *testing.T) {
	prev := debug.SetMemoryLimit(-1)
	debug.SetMemoryLimit(1 << 20) // 1 MiB
	defer debug.SetMemoryLimit(prev)

	if got := defaultMaxParallelNodes(nil); got < 1 {
		t.Fatalf("got %d", got)
	}
}

// The override is absolute — an operator who has measured their workload
// gets the number they asked for.
func TestMaxParallelNodesEnvOverride(t *testing.T) {
	t.Setenv("BROKOLI_MAX_PARALLEL_NODES", "37")
	if got := defaultMaxParallelNodes(nil); got != 37 {
		t.Fatalf("expected 37, got %d", got)
	}
}

// A nonsense override falls back to the derived default rather than
// silently serialising the run.
func TestMaxParallelNodesIgnoresInvalidOverride(t *testing.T) {
	prev := debug.SetMemoryLimit(-1)
	debug.SetMemoryLimit(8 << 30)
	defer debug.SetMemoryLimit(prev)

	for _, bad := range []string{"0", "-3", "lots"} {
		t.Setenv("BROKOLI_MAX_PARALLEL_NODES", bad)
		if got := defaultMaxParallelNodes(nil); got <= 4 {
			t.Fatalf("override %q collapsed the default to %d", bad, got)
		}
	}
}

// With no memory limit set the CPU-derived value stands.
func TestDefaultMaxParallelNodesWithoutMemoryLimit(t *testing.T) {
	prev := debug.SetMemoryLimit(-1)
	debug.SetMemoryLimit(math.MaxInt64)
	defer debug.SetMemoryLimit(prev)

	got := defaultMaxParallelNodes(nil)
	if got < minMaxParallelNodes || got > maxMaxParallelNodes {
		t.Fatalf("expected a value inside the bounds, got %d", got)
	}
}

func clampInt(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}
