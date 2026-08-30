//go:build linux

package proctree

import (
	"os/exec"
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
