package codeexec

import (
	"strings"
	"testing"
)

func TestResolveDefaultsAreUnlimitedWithDocumentedFloors(t *testing.T) {
	l := Resolve(nil)
	if l.MemoryMB != 0 || l.CPUSeconds != 0 {
		t.Fatalf("memory/CPU should default to unlimited, got %+v", l)
	}
	if l.FileSizeMB != defaultFileSizeMB || l.OpenFiles != defaultOpenFiles {
		t.Fatalf("file/fd floors wrong: %+v", l)
	}
	if got := l.String(); !strings.Contains(got, "memory=unlimited") || !strings.Contains(got, "cpu=unlimited") {
		t.Fatalf("String() = %q", got)
	}
}

func TestResolveServerDefaultsAndNodeOverrides(t *testing.T) {
	t.Setenv("BROKOLI_CODE_MEMORY_MB", "1024")
	t.Setenv("BROKOLI_CODE_CPU_SECONDS", "60")

	l := Resolve(nil)
	if l.MemoryMB != 1024 || l.CPUSeconds != 60 {
		t.Fatalf("server defaults not applied: %+v", l)
	}

	// A node override wins over the default, YAML ints included.
	l = Resolve(map[string]interface{}{"max_memory_mb": float64(2048), "max_cpu_seconds": 30})
	if l.MemoryMB != 2048 || l.CPUSeconds != 30 {
		t.Fatalf("node overrides not applied: %+v", l)
	}
}

func TestResolveClampsNodeOverridesToServerMaxima(t *testing.T) {
	t.Setenv("BROKOLI_CODE_MAX_MEMORY_MB", "512")
	t.Setenv("BROKOLI_CODE_MAX_CPU_SECONDS", "10")

	l := Resolve(map[string]interface{}{"max_memory_mb": float64(4096), "max_cpu_seconds": float64(600)})
	if l.MemoryMB != 512 || l.CPUSeconds != 10 {
		t.Fatalf("maxima did not clamp: %+v", l)
	}

	// The maxima clamp only explicit node overrides: with no override and
	// no default, the node stays unlimited (an operator who wants a
	// ceiling on everything sets the default).
	l = Resolve(nil)
	if l.MemoryMB != 0 || l.CPUSeconds != 0 {
		t.Fatalf("maxima leaked into defaults: %+v", l)
	}
}

func TestLimitsEnvOmitsUnlimited(t *testing.T) {
	env := Limits{MemoryMB: 256, FileSizeMB: 4096, OpenFiles: 256}.Env()
	joined := strings.Join(env, " ")
	if !strings.Contains(joined, "BROKED_LIMIT_MEMORY_MB=256") {
		t.Fatalf("memory missing: %v", env)
	}
	if strings.Contains(joined, "CPU_SECONDS") {
		t.Fatalf("unlimited CPU should be omitted: %v", env)
	}
}

func TestResolveIgnoresGarbage(t *testing.T) {
	t.Setenv("BROKOLI_CODE_MEMORY_MB", "not-a-number")
	l := Resolve(map[string]interface{}{"max_cpu_seconds": "60", "max_memory_mb": float64(-5)})
	if l.MemoryMB != 0 || l.CPUSeconds != 0 {
		t.Fatalf("garbage should resolve to unlimited: %+v", l)
	}
}
