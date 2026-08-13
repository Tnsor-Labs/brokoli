package cmd

import (
	"math"
	"os"
	"runtime/debug"
	"strconv"
	"testing"
)

// restoreMemoryLimit resets the process-global Go memory limit after a
// test that touches it. debug.SetMemoryLimit is genuinely global state —
// leaving a tiny test limit behind would make every later test in the
// package run under absurd GC pressure.
func restoreMemoryLimit(t *testing.T) {
	t.Helper()
	prev := debug.SetMemoryLimit(-1)
	t.Cleanup(func() { debug.SetMemoryLimit(prev) })
}

func TestApplyAutoMemoryLimit_SetsFromCgroup(t *testing.T) {
	restoreMemoryLimit(t)
	debug.SetMemoryLimit(math.MaxInt64) // simulate "operator set nothing"
	v2, _ := withCgroupDirs(t)
	writeCgroupFile(t, v2, "memory.max", strconv.Itoa(1<<30)) // 1 GiB
	writeCgroupFile(t, v2, "memory.current", strconv.Itoa(100<<20))

	applied := applyAutoMemoryLimit()
	want := int64(float64(1<<30) * autoMemLimitFraction)
	if applied != want {
		t.Fatalf("applied = %d, want %d (%.0f%% of 1GiB)", applied, want, autoMemLimitFraction*100)
	}
	if got := debug.SetMemoryLimit(-1); got != want {
		t.Fatalf("runtime memory limit = %d, want %d", got, want)
	}
}

func TestApplyAutoMemoryLimit_RespectsOperatorGOMEMLIMIT(t *testing.T) {
	restoreMemoryLimit(t)
	// Simulate GOMEMLIMIT=2GiB having been set at process start: the
	// runtime would hold that value, and SetMemoryLimit(-1) reads it back.
	operatorLimit := int64(2 << 30)
	debug.SetMemoryLimit(operatorLimit)
	v2, _ := withCgroupDirs(t)
	writeCgroupFile(t, v2, "memory.max", strconv.Itoa(1<<30)) // cgroup says less
	writeCgroupFile(t, v2, "memory.current", strconv.Itoa(100<<20))

	if applied := applyAutoMemoryLimit(); applied != 0 {
		t.Fatalf("applied = %d, want 0 (operator's explicit limit must win)", applied)
	}
	if got := debug.SetMemoryLimit(-1); got != operatorLimit {
		t.Fatalf("runtime memory limit = %d, want operator's %d untouched", got, operatorLimit)
	}
}

func TestApplyAutoMemoryLimit_NoCgroup_LeavesDefault(t *testing.T) {
	restoreMemoryLimit(t)
	debug.SetMemoryLimit(math.MaxInt64)
	withCgroupDirs(t) // empty dirs: no limit readable

	if applied := applyAutoMemoryLimit(); applied != 0 {
		t.Fatalf("applied = %d, want 0 with no cgroup limit readable", applied)
	}
	if got := debug.SetMemoryLimit(-1); got != math.MaxInt64 {
		t.Fatalf("runtime memory limit = %d, want untouched MaxInt64", got)
	}
}

func TestApplyAutoMemoryLimit_UnlimitedCgroup_LeavesDefault(t *testing.T) {
	restoreMemoryLimit(t)
	debug.SetMemoryLimit(math.MaxInt64)
	v2, _ := withCgroupDirs(t)
	writeCgroupFile(t, v2, "memory.max", "max\n")
	writeCgroupFile(t, v2, "memory.current", strconv.Itoa(100<<20))

	if applied := applyAutoMemoryLimit(); applied != 0 {
		t.Fatalf("applied = %d, want 0 for an unlimited cgroup", applied)
	}
}

func withMaxConcurrentRunsEnv(t *testing.T, value string) {
	t.Helper()
	old, had := os.LookupEnv("BROKOLI_MAX_CONCURRENT_RUNS")
	if value == "" {
		_ = os.Unsetenv("BROKOLI_MAX_CONCURRENT_RUNS")
	} else if err := os.Setenv("BROKOLI_MAX_CONCURRENT_RUNS", value); err != nil {
		t.Fatalf("Setenv: %v", err)
	}
	t.Cleanup(func() {
		if had {
			_ = os.Setenv("BROKOLI_MAX_CONCURRENT_RUNS", old)
		} else {
			_ = os.Unsetenv("BROKOLI_MAX_CONCURRENT_RUNS")
		}
	})
}

func TestAdaptiveMaxConcurrentRuns(t *testing.T) {
	cases := []struct {
		name       string
		limitBytes int64
		wantN      int
		wantOK     bool
	}{
		// 512Mi pod — this session's own stress-test sizing — takes ONE
		// run at a time: a single 100k-row run measures ~284Mi peak
		// against the pod's own cgroup, so two would put ~570Mi of live
		// memory on a 512Mi ceiling. Demand beyond one run belongs in the
		// queue, where the autoscaler can see it.
		{"512Mi", 512 << 20, 1, true},
		// Below one run's worth of memory still floors at 1: a pod that
		// exists should do SOMETHING, it just shouldn't stack.
		{"256Mi", 256 << 20, 1, true},
		{"128Mi", 128 << 20, 1, true},
		{"1Gi", 1 << 30, 2, true},
		{"2Gi", 2 << 30, 4, true},
		// Huge machine: capped at the old fixed default; raising the
		// ceiling is an operator decision, not a heuristic's.
		{"16Gi", 16 << 30, 4, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			withMaxConcurrentRunsEnv(t, "")
			v2, _ := withCgroupDirs(t)
			writeCgroupFile(t, v2, "memory.max", strconv.FormatInt(tc.limitBytes, 10))
			writeCgroupFile(t, v2, "memory.current", "0")
			n, ok := adaptiveMaxConcurrentRuns()
			if ok != tc.wantOK || n != tc.wantN {
				t.Fatalf("adaptiveMaxConcurrentRuns() = (%d, %v), want (%d, %v)", n, ok, tc.wantN, tc.wantOK)
			}
		})
	}
}

func TestAdaptiveMaxConcurrentRuns_ExplicitEnvWins(t *testing.T) {
	withMaxConcurrentRunsEnv(t, "8")
	v2, _ := withCgroupDirs(t)
	writeCgroupFile(t, v2, "memory.max", strconv.Itoa(512<<20))
	writeCgroupFile(t, v2, "memory.current", "0")
	if _, ok := adaptiveMaxConcurrentRuns(); ok {
		t.Fatal("expected the explicit BROKOLI_MAX_CONCURRENT_RUNS to suppress the adaptive default")
	}
}

func TestAdaptiveMaxConcurrentRuns_NoCgroup_KeepsFixedDefault(t *testing.T) {
	withMaxConcurrentRunsEnv(t, "")
	withCgroupDirs(t) // empty: nothing readable
	if _, ok := adaptiveMaxConcurrentRuns(); ok {
		t.Fatal("expected no adaptive default without a readable cgroup limit")
	}
}
