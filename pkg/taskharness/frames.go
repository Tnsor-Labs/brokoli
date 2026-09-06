// Package taskharness implements the trusted-worker side of the
// brokoli.task-runtime/v1 duplex protocol (ADR-033 section 7): spawn a
// one-shot harness process, exchange the fixed start->ready->events->
// completed|failed handshake over JSON Lines, and enforce the wire
// framing rules docs/schema/task-runtime-v1-framing.md documents (a
// JSON Schema over one decoded frame cannot express them).
//
// This is Phase 2a of the ADR-033 rollout (issue #439 step 5): Run is
// fully tested standalone here and is not yet called from the engine's
// dispatch path (that is Phase 2b). Distinct from and does not replace
// ADR-029's framed binary protocol (pkg/codeexec), which continues to
// govern 'code' nodes exclusively (ADR-035 Decision 3) — a spawned
// process speaks at most one of the two protocols.
package taskharness

// Protocol is the only protocol name a v1 harness handshake accepts.
const Protocol = "brokoli.task-runtime/v1"

// MaxFrameLine is the wire cap on one frame, LF included (framing doc's
// "1 MiB max").
const MaxFrameLine = 1 << 20

// StartFrame is the sole frame the worker sends before the harness has
// replied ready. Field names mirror docs/schema/task-runtime-v1.json's
// "start" $def exactly.
type StartFrame struct {
	Type             string `json:"type"`
	Protocol         string `json:"protocol"`
	InvocationPath   string `json:"invocation_path"`
	ResultPath       string `json:"result_path"`
	OutputStagingDir string `json:"output_staging_dir"`
	EventStream      string `json:"event_stream"`
}

// NewStartFrame fills in the two constants every start frame carries.
func NewStartFrame(invocationPath, resultPath, outputStagingDir string) StartFrame {
	return StartFrame{
		Type:             "start",
		Protocol:         Protocol,
		InvocationPath:   invocationPath,
		ResultPath:       resultPath,
		OutputStagingDir: outputStagingDir,
		EventStream:      "stdout",
	}
}

// CancelFrame is the only other worker->harness frame, valid only after
// ready has been read (framing doc's cancellation section).
type CancelFrame struct {
	Type     string `json:"type"`
	Reason   string `json:"reason"`
	Deadline string `json:"deadline,omitempty"`
}

// Ready is the harness's handshake reply.
type Ready struct {
	Adapter        string   `json:"adapter"`
	AdapterVersion string   `json:"adapter_version"`
	Capabilities   []string `json:"capabilities"`
}

// Log is one structured log event.
type Log struct {
	Level   string                 `json:"level"`
	Message string                 `json:"message"`
	Fields  map[string]interface{} `json:"fields,omitempty"`
}

// Progress is one progress event. Total and Unit are optional per the
// schema; Total nil means "unknown total".
type Progress struct {
	Completed float64  `json:"completed"`
	Total     *float64 `json:"total,omitempty"`
	Unit      string   `json:"unit,omitempty"`
}

// Warning is one non-fatal warning event.
type Warning struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// Metric is one metric event.
type Metric struct {
	Name  string  `json:"name"`
	Kind  string  `json:"kind"`
	Value float64 `json:"value"`
}

// Failure categories a harness may report (ADR-033 section 14). Only
// the harness-originated subset is ever produced by decodeFailure; the
// worker-only categories (runtime_protocol, platform, resource_exhausted,
// lease_lost) are synthesized by this package itself, never trusted from
// the wire when a harness claims them for something it isn't (rule 8
// leaves that policy decision to the control plane, not enforced here).
const (
	FailureUserCode          = "user_code"
	FailureContractViolation = "contract_violation"
	FailureDependency        = "dependency"
	FailureResourceExhausted = "resource_exhausted"
	FailureCancelled         = "cancelled"
	FailureDeadlineExceeded  = "deadline_exceeded"
	FailureRuntimeProtocol   = "runtime_protocol"
	FailurePlatform          = "platform"
	FailureLeaseLost         = "lease_lost"
)

// Failure is a "failed" frame's payload, or a worker-synthesized
// equivalent for a protocol violation the harness itself couldn't have
// reported (e.g. it never replied at all).
type Failure struct {
	Category  string                 `json:"category"`
	Code      string                 `json:"code"`
	Message   string                 `json:"message"`
	Retryable bool                   `json:"retryable"`
	Details   map[string]interface{} `json:"details,omitempty"`
	Stack     string                 `json:"stack,omitempty"`
}

// failedFrame is the wire shape of a "failed" frame, used only to reach
// into its nested "failure" object with decodeInto.
type failedFrame struct {
	Failure Failure `json:"failure"`
}

// Handlers receives non-terminal harness event frames as Run observes
// them, in wire order. A nil handler drops that event type silently —
// the same no-op convention pkg/plugins.Runner already uses for its
// optional ProgressHandler.
type Handlers struct {
	// OnReady is called once, after the ready frame is decoded but
	// before Run reports the handshake successful. Returning an error
	// (e.g. a required capability the harness didn't advertise) fails
	// the attempt with FailureRuntimeProtocol without running anything
	// further -- Run still cancels/terminates the process before
	// returning.
	OnReady    func(Ready) error
	OnLog      func(Log)
	OnProgress func(Progress)
	OnWarning  func(Warning)
	OnMetric   func(Metric)
}
