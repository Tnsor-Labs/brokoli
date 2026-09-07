// Package nodeharness is the Node.js reference implementation of the
// brokoli.task-runtime/v1 harness side (ADR-033 sections 3 and 7):
// harness.mjs, embedded so the trusted worker always launches its own
// copy, exactly as pkg/taskharness/pyharness embeds harness.py and
// pkg/codeexec embeds its ADR-029 wrapper (ADR-026: a runtime is
// resolved, never provisioned).
//
// `node` is ADR-033 section 3's second required reference adapter. Its
// existence is what proves the protocol is language-neutral rather than
// shaped around Python -- the scheduler contains no Python- or
// Node-specific invocation code and selects an adapter by the payload's
// declared runtime class.
//
// Like pyharness, this package owns its own invocation-descriptor
// convention: the wire protocol's "start" frame only promises
// invocation_path is a string. The two adapters' descriptors differ
// only where the languages force it (see Invocation below).
package nodeharness

import (
	"embed"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

//go:embed harness.mjs
var harnessFS embed.FS

// Adapter/AdapterVersion mirror harness.mjs's own ADAPTER/ADAPTER_VERSION
// constants exactly -- kept independently here (rather than parsed out of
// the embedded file) so a caller resolving an execution environment
// digest (ADR-033 section 4, engine/task.go) can name the adapter without
// invoking Node first. The two must be bumped together; there is no
// automated check for that, the same trust pyharness's own hand-kept
// version already relies on.
const (
	Adapter        = "brokoli-node-taskharness"
	AdapterVersion = "0.1.0"
)

// Materialize writes this package's embedded harness.mjs into dir and
// returns its path.
func Materialize(dir string) (string, error) {
	data, err := harnessFS.ReadFile("harness.mjs")
	if err != nil {
		return "", err
	}
	path := filepath.Join(dir, "harness.mjs")
	if err := os.WriteFile(path, data, 0o500); err != nil { // #nosec G306 -- an executable script the worker itself just wrote, not attacker-controlled content
		return "", err
	}
	return path, nil
}

// Command returns the argv Options.Command expects to launch harnessPath
// (as returned by Materialize) under the given node binary.
//
// memoryMB applies the attempt's memory ceiling through V8's
// --max-old-space-size, the same mechanism pkg/codeexec/worker.go
// already uses for a TypeScript code node and for the same reason
// (pkg/codeexec/limits.go: Node's reserved address space makes RLIMIT_AS
// the wrong instrument). This is where the Python and Node adapters
// genuinely differ: harness.py self-applies RLIMIT_AS from inside the
// process, which a JS harness cannot do. Zero or negative leaves V8's
// own default in place.
func Command(nodePath, harnessPath string, memoryMB int) []string {
	argv := []string{nodePath}
	if memoryMB > 0 {
		argv = append(argv, fmt.Sprintf("--max-old-space-size=%d", memoryMB))
	}
	return append(argv, harnessPath)
}

// Invocation is what this reference harness expects to find, as JSON, at
// a start frame's invocation_path -- this adapter's own convention,
// matched byte-for-byte by harness.mjs's loadInvocation.
//
// Deliberately NOT identical to pyharness.Invocation: ModuleRoots
// replaces its SysPath because Node has no sys.path to prepend to, so
// harness.mjs resolves Module against these roots itself. Module,
// Symbol, Kwargs and InterfaceDigest carry exactly the same meaning in
// both adapters -- the descriptors differ only where the languages force
// them to.
type Invocation struct {
	// ModuleRoots are the directories Module is resolved against --
	// normally the task-bundle/v2 payload's extracted root
	// (pkg/taskbundlev2.Extract's destRoot). harness.mjs tries
	// "<root>/<module>.mjs", "<root>/<module>.js" and their index.*
	// directory forms, in that order, naming every path it tried when
	// none exists.
	ModuleRoots []string `json:"module_roots,omitempty"`
	// Module and Symbol name the task function to call, matching the
	// task-bundle/v2 manifest payload's entrypoint.module/.symbol
	// (docs/schema/task-bundle-v2.json). Module is a module name without
	// an extension, the same shape the Python adapter uses.
	Module string `json:"module"`
	Symbol string `json:"symbol"`
	// Kwargs are passed to Symbol as ONE object argument, since
	// JavaScript has no keyword arguments (harness.mjs's own header
	// documents this as a forced, not chosen, difference from Python's
	// func(**kwargs)).
	Kwargs map[string]interface{} `json:"kwargs,omitempty"`
	// InterfaceDigest is stamped into the candidate task-result-v1
	// manifest's own interface_digest field.
	InterfaceDigest string `json:"interface_digest"`
	// OutputKind carries the declared output kind, same meaning and same
	// engine-is-authoritative reasoning as pyharness.Invocation's own
	// field: "dataset" makes this harness write NDJSON to the staging
	// dir and report it by reference instead of inlining a scalar.
	OutputKind string `json:"output_kind,omitempty"`
}

// WriteInvocation writes inv as JSON to path.
func WriteInvocation(path string, inv Invocation) error {
	if inv.Module == "" || inv.Symbol == "" {
		return fmt.Errorf("nodeharness: Invocation.Module and Symbol must both be set")
	}
	b, err := json.Marshal(inv)
	if err != nil {
		return err
	}
	return os.WriteFile(path, b, 0o600) // #nosec G306 -- a per-attempt descriptor the trusted worker itself writes and immediately hands to its own child process
}
