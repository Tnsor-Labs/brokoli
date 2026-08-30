package codeexec

import "embed"

// The Python wrapper library, embedded so the engine always injects its
// own copy (ADR-026: a Python runtime is resolved, never provisioned,
// and no pip package is ever required in a user environment). The
// pytest suite under pywrapper/tests is deliberately NOT embedded — it
// tests the wrapper in CI, it does not ship.
//
//go:embed pywrapper/wrapper.py pywrapper/version.py
var pywrapperFS embed.FS
