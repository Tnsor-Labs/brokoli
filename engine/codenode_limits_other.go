//go:build !unix

package engine

import "github.com/Tnsor-Labs/brokoli/pkg/codeexec"

// Rlimits do not exist off POSIX; the wrapper's preamble is a no-op
// there and no failure is attributable to a limit.
func signalBreachError(_ error, _ codeexec.Limits) error {
	return nil
}
