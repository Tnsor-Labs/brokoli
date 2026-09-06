// Package taskruntime hosts ADR-033's polyglot task runtime types: a
// worker's structured capability advertisement (section 5), the
// control-plane's resolved and pinned payload selection for one 'task'
// node attempt (section 4), and the pure placement predicate that
// matches the two.
//
// Rollout phase 1 (issue #439 step 5): pure data types and a pure
// matching function only. Nothing here is wired into a worker
// registration endpoint, physical planning, ClaimAttempt, or
// extensions.InstanceWorkOrder yet -- see docs/schema/worker-capabilities-v1.json
// and docs/schema/resolved-execution-record-v1.json for the canonical
// JSON contracts these types parse, and pkg/codeexec for the unrelated,
// already-shipped ADR-029 pool that continues to govern 'code' nodes
// (ADR-035 Decision 2/3 -- this package is not a modification of that
// one).
package taskruntime

import "encoding/json"

// RuntimeClass is ADR-033 section 3's closed runtime-class vocabulary --
// the same enum task-bundle-v2.json's payload.runtime uses.
type RuntimeClass string

const (
	RuntimeClassPython    RuntimeClass = "python"
	RuntimeClassNode      RuntimeClass = "node"
	RuntimeClassNative    RuntimeClass = "native"
	RuntimeClassContainer RuntimeClass = "container"
	RuntimeClassWASI      RuntimeClass = "wasi"
	RuntimeClassJVM       RuntimeClass = "jvm"
)

// IOMode is ADR-033 section 9's physical I/O mode vocabulary.
type IOMode string

const (
	IOModeBatchReference IOMode = "batch-reference"
	IOModeBatchInline    IOMode = "batch-inline"
	IOModeStreamV1       IOMode = "stream-v1"
)

// Platform identifies a worker's OS/architecture.
type Platform struct {
	OS   string `json:"os"`
	Arch string `json:"arch"`
}

// RuntimeCapability is one runtime class a worker can execute, with the
// concrete versions installed and its adapter's own version (ADR-033
// section 15). Adapter is optional: a native/container runtime class may
// have no separate adapter version to report.
type RuntimeCapability struct {
	Class    RuntimeClass `json:"class"`
	Versions []string     `json:"versions"`
	Adapter  string       `json:"adapter,omitempty"`
}

// Resources is a worker's advertised resource ceiling.
type Resources struct {
	CPUMillis   int64 `json:"cpu_millis"`
	MemoryBytes int64 `json:"memory_bytes"`
}

// WorkerCapabilities is a worker's structured capability advertisement
// (ADR-033 section 5) -- an authenticated observation, never an author
// assertion. Matches docs/schema/worker-capabilities-v1.json exactly.
type WorkerCapabilities struct {
	WorkerID  string              `json:"worker_id"`
	Protocols []string            `json:"protocols"`
	Platform  Platform            `json:"platform"`
	Runtimes  []RuntimeCapability `json:"runtimes"`
	IO        []IOMode            `json:"io"`
	// Isolation names the isolation mechanisms a worker can enforce
	// (e.g. "process", "container"). Deliberately []string, not a closed
	// enum type: ADR-033 section 12 names trust profiles, not a fixed
	// mechanism vocabulary, and this package does not invent a strength
	// ordering between mechanism names the ADR itself never defines --
	// see Requirements.Isolation and Match's doc comment.
	Isolation []string          `json:"isolation"`
	Resources Resources         `json:"resources"`
	Labels    map[string]string `json:"labels,omitempty"`
}

// HasProtocol reports whether the worker advertises protocol.
func (c WorkerCapabilities) HasProtocol(protocol string) bool {
	for _, p := range c.Protocols {
		if p == protocol {
			return true
		}
	}
	return false
}

// HasIsolation reports whether the worker advertises mechanism.
func (c WorkerCapabilities) HasIsolation(mechanism string) bool {
	for _, m := range c.Isolation {
		if m == mechanism {
			return true
		}
	}
	return false
}

// HasIO reports whether the worker advertises mode.
func (c WorkerCapabilities) HasIO(mode IOMode) bool {
	for _, m := range c.IO {
		if m == mode {
			return true
		}
	}
	return false
}

// Runtime returns the worker's capability entry for class, if any.
func (c WorkerCapabilities) Runtime(class RuntimeClass) (RuntimeCapability, bool) {
	for _, r := range c.Runtimes {
		if r.Class == class {
			return r, true
		}
	}
	return RuntimeCapability{}, false
}

// HasRuntimeVersion reports whether the worker's class capability lists
// the exact concrete version (not a range -- range matching against a
// bundle payload's declared 'requires' happens before Requirements is
// constructed, per ADR-033 section 4).
func (c WorkerCapabilities) HasRuntimeVersion(class RuntimeClass, version string) bool {
	rc, ok := c.Runtime(class)
	if !ok {
		return false
	}
	for _, v := range rc.Versions {
		if v == version {
			return true
		}
	}
	return false
}

// ParseWorkerCapabilities decodes a worker-capabilities-v1.json document.
// It performs only JSON structural decoding -- schema validation
// (enums, required fields, the protocol-name pattern) is a separate,
// explicit step against the JSON Schema, exactly like
// taskinterface.ParseType does not re-implement task-interface-v1.json's
// own constraints.
func ParseWorkerCapabilities(data []byte) (*WorkerCapabilities, error) {
	var c WorkerCapabilities
	if err := json.Unmarshal(data, &c); err != nil {
		return nil, err
	}
	return &c, nil
}
