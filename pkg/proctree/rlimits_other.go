//go:build !linux

package proctree

// Rlimits mirrors the Linux API. Other platforms deliberately degrade
// to no external process limits; Node's heap flag remains portable.
type Rlimits struct {
	CPUSeconds    uint64
	FileSizeBytes uint64
	OpenFiles     uint64
}

func ApplyRlimits(_ int, _ Rlimits) error { return nil }
