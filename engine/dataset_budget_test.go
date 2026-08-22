package engine

import (
	"math"
	"os"
	"runtime/debug"
	"strings"
	"testing"

	"github.com/Tnsor-Labs/brokoli/pkg/common"
)

// No memory limit means no guard: a bare host behaves exactly as it did
// before this existed.
func TestDatasetBudgetUnlimitedWithoutMemoryLimit(t *testing.T) {
	prev := debug.SetMemoryLimit(-1)
	debug.SetMemoryLimit(math.MaxInt64)
	defer debug.SetMemoryLimit(prev)
	os.Unsetenv("BROKOLI_DATASET_MEMORY_BUDGET")

	if got := datasetMemoryBudget(); got != 0 {
		t.Fatalf("expected no budget, got %d", got)
	}
}

// Inside a container the budget is a conservative share of the limit —
// a node holds its input while building its output, and the sink encodes
// another copy.
func TestDatasetBudgetIsAFractionOfTheMemoryLimit(t *testing.T) {
	prev := debug.SetMemoryLimit(-1)
	debug.SetMemoryLimit(1 << 30)
	defer debug.SetMemoryLimit(prev)
	os.Unsetenv("BROKOLI_DATASET_MEMORY_BUDGET")

	got := datasetMemoryBudget()
	if got <= 0 || got >= 1<<30 {
		t.Fatalf("expected a fraction of the limit, got %d", got)
	}
	limit := int64(1) << 30
	if want := int64(float64(limit) * datasetBudgetFraction); got != want {
		t.Fatalf("expected %d, got %d", want, got)
	}
}

func TestDatasetBudgetEnvOverride(t *testing.T) {
	t.Setenv("BROKOLI_DATASET_MEMORY_BUDGET", "12345")
	if got := datasetMemoryBudget(); got != 12345 {
		t.Fatalf("expected the override, got %d", got)
	}
}

func TestDatasetBudgetIgnoresInvalidOverride(t *testing.T) {
	prev := debug.SetMemoryLimit(-1)
	debug.SetMemoryLimit(1 << 30)
	defer debug.SetMemoryLimit(prev)
	for _, bad := range []string{"0", "-1", "plenty"} {
		t.Setenv("BROKOLI_DATASET_MEMORY_BUDGET", bad)
		limit := int64(1) << 30
		if got := datasetMemoryBudget(); got != int64(float64(limit)*datasetBudgetFraction) {
			t.Fatalf("override %q was honoured as %d", bad, got)
		}
	}
}

// The estimate has to count the per-entry cost, not just the payload:
// for narrow rows the map and interface overhead dominates.
func TestEstimateRowBytesCountsEntryOverhead(t *testing.T) {
	narrow := common.DataRow{"a": 1, "b": 2}
	if got := estimateRowBytes(narrow); got < 96 {
		t.Fatalf("expected per-entry overhead to be counted, got %d", got)
	}

	wide := common.DataRow{"padding": strings.Repeat("x", 500)}
	got := estimateRowBytes(wide)
	if got < 500 {
		t.Fatalf("expected the payload to be counted, got %d", got)
	}
	if got > 700 {
		t.Fatalf("estimate implausibly high for a 500-byte value: %d", got)
	}
}

func TestEstimateRowBytesHandlesNilAndBytes(t *testing.T) {
	row := common.DataRow{"n": nil, "b": []byte("hello")}
	if got := estimateRowBytes(row); got < 5 {
		t.Fatalf("expected byte payload counted, got %d", got)
	}
}

func TestHumanBytesRendersReadableUnits(t *testing.T) {
	cases := map[int64]string{
		512:           "512 B",
		2 << 10:       "2 KiB",
		150 << 20:     "150 MiB",
		3 * (1 << 30): "3.0 GiB",
	}
	for in, want := range cases {
		if got := humanBytes(in); got != want {
			t.Fatalf("humanBytes(%d) = %q, want %q", in, got, want)
		}
	}
}
