//go:build !linux

package proctree

import "errors"

// Rlimits mirrors the Linux API. Other platforms deliberately degrade
// to no external process limits; Node's heap flag remains portable.
type Rlimits struct {
	CPUSeconds    uint64
	FileSizeBytes uint64
	OpenFiles     uint64
}

func ApplyRlimits(_ int, _ Rlimits) error { return nil }

// ApplyAddressSpaceLimit is Linux-only.
func ApplyAddressSpaceLimit(_ int, _ uint64) error {
	return errors.New("address-space limits are Linux-only")
}

// ShellLimitPrelude sets no limits on other platforms, as ApplyRlimits; an
// address-space limit is refused, as ApplyAddressSpaceLimit refuses it.
func ShellLimitPrelude(_ Rlimits, addressSpaceBytes uint64) (string, error) {
	if addressSpaceBytes > 0 {
		return "", errors.New("address-space limits are Linux-only")
	}
	return "", nil
}
