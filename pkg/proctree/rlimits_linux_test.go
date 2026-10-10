//go:build linux

package proctree

import (
	"fmt"
	"os/exec"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
)

func TestApplyRlimits(t *testing.T) {
	cmd := exec.Command("sleep", "30")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	if err := ApplyRlimits(cmd.Process.Pid, Rlimits{CPUSeconds: 7, FileSizeBytes: 8192, OpenFiles: 64}); err != nil {
		t.Fatal(err)
	}
	for name, item := range map[string]struct {
		resource int
		want     uint64
	}{
		"cpu": {unix.RLIMIT_CPU, 7}, "file": {unix.RLIMIT_FSIZE, 8192}, "files": {unix.RLIMIT_NOFILE, 64},
	} {
		var got unix.Rlimit
		if err := unix.Prlimit(cmd.Process.Pid, item.resource, nil, &got); err != nil {
			t.Fatal(err)
		}
		if got.Cur != item.want || got.Max != item.want {
			t.Errorf("%s rlimit = %+v, want %d", name, got, item.want)
		}
	}
}

func TestShellLimitPreludeSetsNothingWhenAskedForNothing(t *testing.T) {
	got, err := ShellLimitPrelude(Rlimits{}, 0)
	if err != nil || got != "" {
		t.Fatalf("prelude = %q, %v; want empty", got, err)
	}
}

// Soft before hard, in ulimit's units (1024-byte blocks for file size and
// address space), with POSIX mode off so those units hold.
func TestShellLimitPreludeUnitsAndOrder(t *testing.T) {
	got, err := ShellLimitPrelude(Rlimits{CPUSeconds: 5, FileSizeBytes: 10 << 20}, 100<<20)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"set +o posix\n", "ulimit -S -t 5 && ulimit -H -t 5", "ulimit -S -f 10240 && ulimit -H -f 10240", "ulimit -S -v 102400 && ulimit -H -v 102400"} {
		if !strings.Contains(got, want) {
			t.Errorf("prelude lacks %q:\n%s", want, got)
		}
	}
}

// A value above this process's own hard limit is clamped to it, as
// ApplyRlimits clamps: an unprivileged shell cannot raise a hard limit, and
// asking it to would refuse the whole command.
func TestShellLimitPreludeClampsToTheInheritedHardLimit(t *testing.T) {
	var own unix.Rlimit
	if err := unix.Getrlimit(unix.RLIMIT_NOFILE, &own); err != nil {
		t.Fatal(err)
	}
	if own.Max == unix.RLIM_INFINITY {
		t.Skip("open-files hard limit is unlimited here")
	}
	got, err := ShellLimitPrelude(Rlimits{OpenFiles: own.Max + 1000}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if want := fmt.Sprintf("ulimit -H -n %d", own.Max); !strings.Contains(got, want) {
		t.Errorf("prelude does not clamp to the hard limit (want %q):\n%s", want, got)
	}
	if want := fmt.Sprintf("ulimit -S -n %d", min(own.Cur, own.Max)); !strings.Contains(got, want) {
		t.Errorf("prelude does not clamp the soft limit (want %q):\n%s", want, got)
	}
}
