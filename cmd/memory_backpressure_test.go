package cmd

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"
)

// withCgroupDirs points cgroupMemoryV2Dir/cgroupMemoryV1Dir at fresh, empty
// temp directories for the duration of the test, restoring the real values
// afterward — so these tests never depend on (or interfere with) whatever
// cgroup the test process itself happens to be running under.
func withCgroupDirs(t *testing.T) (v2Dir, v1Dir string) {
	t.Helper()
	oldV2, oldV1 := cgroupMemoryV2Dir, cgroupMemoryV1Dir
	v2Dir = filepath.Join(t.TempDir(), "v2")
	v1Dir = filepath.Join(t.TempDir(), "v1")
	if err := os.MkdirAll(v2Dir, 0o755); err != nil {
		t.Fatalf("mkdir v2 dir: %v", err)
	}
	if err := os.MkdirAll(v1Dir, 0o755); err != nil {
		t.Fatalf("mkdir v1 dir: %v", err)
	}
	cgroupMemoryV2Dir, cgroupMemoryV1Dir = v2Dir, v1Dir
	t.Cleanup(func() { cgroupMemoryV2Dir, cgroupMemoryV1Dir = oldV2, oldV1 })
	return v2Dir, v1Dir
}

func writeCgroupFile(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
}

func TestHasMemoryHeadroom_NoCgroupMount_FailsOpen(t *testing.T) {
	withCgroupDirs(t) // dirs exist but are empty — no memory.max/memory.current at all
	if !hasMemoryHeadroom() {
		t.Fatal("expected fail-open (true) when no cgroup files are present")
	}
}

func TestHasMemoryHeadroom_UnlimitedV2_FailsOpen(t *testing.T) {
	v2, _ := withCgroupDirs(t)
	writeCgroupFile(t, v2, "memory.max", "max\n")
	writeCgroupFile(t, v2, "memory.current", "104857600\n") // 100 MiB
	if !hasMemoryHeadroom() {
		t.Fatal("expected fail-open (true) for an unlimited (\"max\") cgroup v2 limit")
	}
}

func TestHasMemoryHeadroom_V2_PlentyOfHeadroom(t *testing.T) {
	v2, _ := withCgroupDirs(t)
	writeCgroupFile(t, v2, "memory.max", strconv.Itoa(1<<30)) // 1 GiB
	writeCgroupFile(t, v2, "memory.current", strconv.Itoa(100<<20)) // 100 MiB used, 90%+ free
	if !hasMemoryHeadroom() {
		t.Fatal("expected headroom with only 10% of the limit used")
	}
}

func TestHasMemoryHeadroom_V2_NearLimit_Denies(t *testing.T) {
	v2, _ := withCgroupDirs(t)
	writeCgroupFile(t, v2, "memory.max", strconv.Itoa(1<<30))       // 1 GiB
	writeCgroupFile(t, v2, "memory.current", strconv.Itoa(950<<20)) // ~93% used, well under the 20% margin
	if hasMemoryHeadroom() {
		t.Fatal("expected no headroom with ~93% of the limit used against a 20% safety margin")
	}
}

func TestHasMemoryHeadroom_V2_ExactlyAtMargin_Denies(t *testing.T) {
	v2, _ := withCgroupDirs(t)
	limit := int64(1 << 30) // 1 GiB
	margin := int64(float64(limit) * memoryBackpressureSafetyMarginFraction)
	writeCgroupFile(t, v2, "memory.max", strconv.FormatInt(limit, 10))
	// current such that headroom == margin exactly — the boundary must deny
	// (strictly greater than margin is required), not admit.
	writeCgroupFile(t, v2, "memory.current", strconv.FormatInt(limit-margin, 10))
	if hasMemoryHeadroom() {
		t.Fatal("expected headroom exactly at the safety margin to deny admission, not allow it")
	}
}

func TestHasMemoryHeadroom_MinMarginFloor_SmallContainer(t *testing.T) {
	v2, _ := withCgroupDirs(t)
	// A tiny 100 MiB limit: 20% of it (20 MiB) is below
	// memoryBackpressureMinMarginBytes (64 MiB), so the floor should apply
	// — headroom of 30 MiB (100-70) should still be denied even though
	// it's comfortably more than a naive 20%-of-limit margin would require.
	writeCgroupFile(t, v2, "memory.max", strconv.Itoa(100<<20))
	writeCgroupFile(t, v2, "memory.current", strconv.Itoa(70<<20))
	if hasMemoryHeadroom() {
		t.Fatal("expected the absolute minimum margin floor to deny admission for a small container")
	}
}

func TestHasMemoryHeadroom_V1Fallback_UsedWhenNoV2(t *testing.T) {
	_, v1 := withCgroupDirs(t) // v2 dir exists but has no memory.max/memory.current
	writeCgroupFile(t, v1, "memory.limit_in_bytes", strconv.Itoa(1<<30))
	writeCgroupFile(t, v1, "memory.usage_in_bytes", strconv.Itoa(100<<20))
	if !hasMemoryHeadroom() {
		t.Fatal("expected v1 fallback to report headroom when v2 files are absent")
	}
}

func TestHasMemoryHeadroom_V1Unconfined_FailsOpen(t *testing.T) {
	_, v1 := withCgroupDirs(t)
	// cgroup v1's spelling of "no limit" is a huge sentinel value, not a
	// string like v2's "max".
	writeCgroupFile(t, v1, "memory.limit_in_bytes", "9223372036854771712")
	writeCgroupFile(t, v1, "memory.usage_in_bytes", strconv.Itoa(100<<20))
	if !hasMemoryHeadroom() {
		t.Fatal("expected fail-open (true) for an unconfined cgroup v1 limit")
	}
}

func TestHasMemoryHeadroom_V1DeniesNearLimit(t *testing.T) {
	_, v1 := withCgroupDirs(t)
	writeCgroupFile(t, v1, "memory.limit_in_bytes", strconv.Itoa(1<<30))
	writeCgroupFile(t, v1, "memory.usage_in_bytes", strconv.Itoa(950<<20))
	if hasMemoryHeadroom() {
		t.Fatal("expected v1 fallback to deny admission near the limit")
	}
}

func TestHasMemoryHeadroom_V2PreferredOverV1WhenBothPresent(t *testing.T) {
	v2, v1 := withCgroupDirs(t)
	// v2 says plenty of headroom; v1 (which should be ignored once v2 is
	// readable) says none. If v1 won, this would incorrectly deny.
	writeCgroupFile(t, v2, "memory.max", strconv.Itoa(1<<30))
	writeCgroupFile(t, v2, "memory.current", strconv.Itoa(100<<20))
	writeCgroupFile(t, v1, "memory.limit_in_bytes", strconv.Itoa(1<<30))
	writeCgroupFile(t, v1, "memory.usage_in_bytes", strconv.Itoa(999<<20))
	if !hasMemoryHeadroom() {
		t.Fatal("expected v2 to take precedence over v1 when both are present")
	}
}

func TestHasMemoryHeadroom_DisabledEnvVar_AlwaysTrue(t *testing.T) {
	v2, _ := withCgroupDirs(t)
	writeCgroupFile(t, v2, "memory.max", strconv.Itoa(1<<30))
	writeCgroupFile(t, v2, "memory.current", strconv.Itoa(999<<20)) // would deny if checked

	old := os.Getenv("BROKOLI_MEMORY_BACKPRESSURE_DISABLED")
	if err := os.Setenv("BROKOLI_MEMORY_BACKPRESSURE_DISABLED", "1"); err != nil {
		t.Fatalf("Setenv: %v", err)
	}
	t.Cleanup(func() {
		if old == "" {
			_ = os.Unsetenv("BROKOLI_MEMORY_BACKPRESSURE_DISABLED")
		} else {
			_ = os.Setenv("BROKOLI_MEMORY_BACKPRESSURE_DISABLED", old)
		}
	})

	if !hasMemoryHeadroom() {
		t.Fatal("expected BROKOLI_MEMORY_BACKPRESSURE_DISABLED=1 to bypass the check entirely")
	}
}

func TestReadCgroupInt(t *testing.T) {
	dir := t.TempDir()
	cases := []struct {
		name    string
		content string
		wantN   int64
		wantOK  bool
	}{
		{"plain-number", "1073741824", 1073741824, true},
		{"trailing-newline", "1073741824\n", 1073741824, true},
		{"max-sentinel", "max\n", 0, false},
		{"garbage", "not-a-number\n", 0, false},
		{"empty", "", 0, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(dir, tc.name)
			if err := os.WriteFile(path, []byte(tc.content), 0o644); err != nil {
				t.Fatalf("write: %v", err)
			}
			n, ok := readCgroupInt(path)
			if ok != tc.wantOK || n != tc.wantN {
				t.Fatalf("readCgroupInt(%q) = (%d, %v), want (%d, %v)", tc.content, n, ok, tc.wantN, tc.wantOK)
			}
		})
	}
	if _, ok := readCgroupInt(filepath.Join(dir, "does-not-exist")); ok {
		t.Fatal("expected reading a nonexistent file to report ok=false")
	}
}
