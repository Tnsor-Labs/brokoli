//go:build !unix

package engine

// applyNativeWorkerLimits has no portable implementation off Unix. The
// native worker is only built with driver support for Unix platforms, so a
// child here can never load a driver to limit.
func applyNativeWorkerLimits(nativeWorkerLimits) error { return nil }
