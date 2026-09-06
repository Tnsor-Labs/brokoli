// Package pyharness is the Python reference implementation of the
// brokoli.task-runtime/v1 harness side (ADR-033 section 7): harness.py,
// embedded so the trusted worker always launches its own copy, exactly
// as pkg/codeexec embeds its ADR-029 wrapper (ADR-026: a Python runtime
// is resolved, never provisioned, and no pip package is required in a
// user environment).
//
// This package also owns the invocation-descriptor convention: the
// wire protocol's "start" frame only promises invocation_path is a
// string (ADR-033 phase 0 left what lives there unspecified, since no
// adapter existed yet). Invocation is that convention for this
// reference harness -- Go and Python code that disagreed about it would
// be two implementations of an undocumented protocol, which is exactly
// what a shared struct plus a single doc comment on each side avoids.
package pyharness

import (
	"embed"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

//go:embed harness.py
var harnessFS embed.FS

// Adapter/AdapterVersion mirror harness.py's own ADAPTER/ADAPTER_VERSION
// constants exactly -- kept independently here (rather than parsed out
// of the embedded file) so a caller resolving an execution environment
// digest (ADR-033 section 4, engine/task.go) can name the adapter
// without invoking Python first. The two must be bumped together; there
// is no automated check for that, the same trust harness.py's own
// hand-kept version already relies on.
const (
	Adapter        = "brokoli-python-taskharness"
	AdapterVersion = "0.1.0"
)

// Materialize writes this package's embedded harness.py into dir and
// returns its path, so a caller can hand pkg/taskharness.Options.Command
// a real file path without shipping harness.py separately from the
// binary that launches it.
func Materialize(dir string) (string, error) {
	data, err := harnessFS.ReadFile("harness.py")
	if err != nil {
		return "", err
	}
	path := filepath.Join(dir, "harness.py")
	if err := os.WriteFile(path, data, 0o500); err != nil { // #nosec G306 -- an executable script the worker itself just wrote, not attacker-controlled content
		return "", err
	}
	return path, nil
}

// Command returns the argv Options.Command expects to launch
// harnessPath (as returned by Materialize) under the given python
// interpreter.
func Command(pythonPath, harnessPath string) []string {
	return []string{pythonPath, harnessPath}
}

// Invocation is what this reference harness expects to find, as JSON,
// at a start frame's invocation_path. Not part of the task-runtime/v1
// wire schema (docs/schema/task-runtime-v1.json) -- purely this
// harness's own convention, matched byte-for-byte by harness.py's
// load_invocation.
type Invocation struct {
	// SysPath is prepended to Python's module search path before
	// importing Module -- normally the task-bundle/v2 payload's
	// extracted root (pkg/taskbundlev2.Extract's destRoot).
	SysPath []string `json:"sys_path,omitempty"`
	// Module and Symbol name the task function to call, matching the
	// task-bundle/v2 manifest payload's entrypoint.module/.symbol
	// (docs/schema/task-bundle-v2.json).
	Module string `json:"module"`
	Symbol string `json:"symbol"`
	// Kwargs are passed to Symbol as keyword arguments.
	Kwargs map[string]interface{} `json:"kwargs,omitempty"`
	// InterfaceDigest is stamped into the candidate task-result-v1
	// manifest's own interface_digest field.
	InterfaceDigest string `json:"interface_digest"`
}

// WriteInvocation writes inv as JSON to path.
func WriteInvocation(path string, inv Invocation) error {
	if inv.Module == "" || inv.Symbol == "" {
		return fmt.Errorf("pyharness: Invocation.Module and Symbol must both be set")
	}
	b, err := json.Marshal(inv)
	if err != nil {
		return err
	}
	return os.WriteFile(path, b, 0o600) // #nosec G306 -- a per-attempt descriptor the trusted worker itself writes and immediately hands to its own child process
}
