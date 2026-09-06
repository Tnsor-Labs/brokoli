//go:build !unix

package taskharness

import "github.com/Tnsor-Labs/brokoli/pkg/proctree"

// resourceLimitFailure has nothing to recognize off Unix: pkg/proctree's
// own ApplyRlimits is already a no-op there (rlimits_other.go).
func resourceLimitFailure(_ error, _ proctree.Rlimits) *Failure { return nil }
