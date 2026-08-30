package codeexec

import "embed"

// The Python wrapper library, embedded so the engine always injects its
// own copy (ADR-026: a Python runtime is resolved, never provisioned,
// and no pip package is ever required in a user environment). The
// pytest suite under pywrapper/tests is deliberately NOT embedded — it
// tests the wrapper in CI, it does not ship.
//
//go:embed pywrapper/wrapper.py pywrapper/version.py pywrapper/protocol.py pywrapper/worker_main.py
var pywrapperFS embed.FS

// The JavaScript wrapper is a bare-Node artifact: the engine resolves a
// Node runtime but never provisions npm dependencies (ADR-030).
//
//go:embed jswrapper/worker_main.mjs jswrapper/protocol.mjs jswrapper/contract.mjs jswrapper/version.mjs
var jswrapperFS embed.FS
