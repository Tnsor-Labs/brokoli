// Package plugins implements the subprocess plugin protocol that lets
// Brokoli load connectors and custom node types from external binaries
// without recompiling the core.
//
// The protocol is deliberately tiny, modeled on Airbyte's connector
// protocol: a plugin is an executable that supports a handful of
// subcommands (`spec`, `check`, `discover`, `read`, `write`) and
// communicates with the host over stdin/stdout as newline-delimited
// JSON ("JSONL"). Plugins can be written in any language; a shell
// script is enough to satisfy the protocol, which is how the
// reference test plugin is shipped. (An earlier version of this
// comment recommended a `brokoli-connector-sdk` Python package that
// was never built — see ADR-002's follow-ups and ADR-029, which
// supersede that plan.)
//
// Lifecycle of one plugin invocation:
//
//  1. Host launches `plugin-binary <command> [args...]`
//  2. Host writes one line of JSON config to the plugin's stdin and closes it
//  3. Plugin streams JSONL lines back on stdout, one typed message per line
//  4. Plugin writes unstructured logs to stderr (captured into run logs)
//  5. Plugin exits; exit code 0 = success, non-zero = failure
//
// Protocol version 1 is the current version. Breaking changes bump the
// version. Plugins declare the protocol version they were built against
// in their manifest, and the host refuses to load plugins it can't
// speak to.
package plugins

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"
)

// ProtocolVersion is the current plugin protocol version. Plugins built
// against an older version can still load as long as the host declares
// support for it in SupportedProtocolVersions.
const ProtocolVersion = 1

// SupportedProtocolVersions lists every protocol version this host can
// speak. Expand when deprecating old versions.
var SupportedProtocolVersions = []int{1}

// Command is one of the subcommands a plugin binary must support.
type Command string

const (
	// CmdSpec prints the plugin's manifest JSON to stdout. Used at
	// install time to cache the plugin's declared capabilities.
	CmdSpec Command = "spec"

	// CmdCheck validates a config against the plugin's config schema
	// and tests connectivity (e.g. a SELECT 1 against a database).
	// Reads config from stdin, emits a single status message.
	CmdCheck Command = "check"

	// CmdDiscover lists the streams (tables, endpoints, etc.) available
	// under the given config. Reads config from stdin, emits one
	// StreamMessage per stream.
	CmdDiscover Command = "discover"

	// CmdPlan breaks one already-discovered stream into independent
	// work units the host can schedule, retry, and track separately
	// (ADR-013 M3) — a page range, a file list, a table's partitions,
	// whatever fits that connector. Optional: only meaningful for a
	// node type whose manifest sets NodeTypeDecl.SupportsPlan. A plugin
	// that doesn't implement it is read whole, exactly as before this
	// command existed. Reads PlanParams from stdin, emits one
	// WorkUnitMessage per unit.
	CmdPlan Command = "plan"

	// CmdRead emits records from a source stream. Reads config from
	// stdin, emits RecordMessages and optional StateMessages on stdout.
	CmdRead Command = "read"

	// CmdWrite consumes records into a sink stream. Reads config from
	// stdin (first line) then records (subsequent lines). Emits a
	// single status message when done.
	CmdWrite Command = "write"
)

// MessageType identifies a JSONL line emitted by a plugin.
type MessageType string

const (
	// MsgRecord is a single data row produced by a source.
	MsgRecord MessageType = "record"

	// MsgState is an incremental-sync cursor that the host persists
	// and passes back on the next run of the same stream.
	MsgState MessageType = "state"

	// MsgStream is a stream declaration emitted by `discover`.
	MsgStream MessageType = "stream"

	// MsgLog is a structured log line. Plain stderr is also captured,
	// but MsgLog is preferred because it lets the plugin attach a
	// level and a machine-readable context.
	MsgLog MessageType = "log"

	// MsgStatus is a single-shot ok/error report used by `check` and
	// `write` when they have no data to stream.
	MsgStatus MessageType = "status"

	// MsgError is a fatal error. A plugin should emit MsgError then
	// exit non-zero; the host treats either signal as failure but
	// the typed message gives a cleaner log.
	MsgError MessageType = "error"

	// MsgProgress is a structured progress report - the typed
	// counterpart to MsgLog's free text, carrying the dimensions RFC
	// §17 describes (current/total/unit, rows, bytes, rate) so a UI can
	// render a real progress bar instead of parsing log lines.
	MsgProgress MessageType = "progress"

	// MsgWorkUnit is one independent, schedulable piece of a stream's
	// work, emitted by `plan` (ADR-013 M3) - the typed counterpart to
	// MsgStream's "here is a stream" for "here is one piece of a
	// stream's work". A plugin that never implements `plan` never
	// emits this; every plugin that only implements read-the-whole-
	// stream is unaffected.
	MsgWorkUnit MessageType = "work_unit"
)

// Message is a single JSONL line on a plugin's stdout stream. The
// Type discriminator picks which of the optional fields is populated.
// Unknown types are ignored by the host so old plugins don't break new
// hosts and vice versa.
type Message struct {
	Type       MessageType            `json:"type"`
	Data       map[string]interface{} `json:"data,omitempty"`       // MsgRecord: the row
	EmittedAt  string                 `json:"emitted_at,omitempty"` // MsgRecord: ISO-8601 timestamp (optional)
	Value      map[string]interface{} `json:"value,omitempty"`      // MsgState: cursor value
	Stream     *Stream                `json:"stream,omitempty"`     // MsgStream: the declared stream
	Level      LogLevel               `json:"level,omitempty"`      // MsgLog: severity
	Message    string                 `json:"message,omitempty"`    // MsgLog, MsgError, MsgStatus
	StatusCode string                 `json:"status,omitempty"`     // MsgStatus: "ok" or "error"
	Progress   *Progress              `json:"progress,omitempty"`   // MsgProgress: the progress report
	WorkUnit   *WorkUnit              `json:"work_unit,omitempty"`  // MsgWorkUnit: the declared unit
}

// LogLevel mirrors Brokoli's run log levels so plugin logs can be
// merged into the existing run log UI without translation.
type LogLevel string

const (
	LogDebug LogLevel = "debug"
	LogInfo  LogLevel = "info"
	LogWarn  LogLevel = "warn"
	LogError LogLevel = "error"
)

// Stream is one addressable data source exposed by a plugin: a database
// table, an API endpoint, a topic, a file pattern, etc. Emitted during
// `discover` and referenced by name in `read`/`write`.
type Stream struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	// Columns is a best-effort schema hint. Order-preserving.
	Columns []StreamColumn `json:"columns,omitempty"`
	// Mode describes how incremental reads work for this stream.
	// "full_refresh" | "incremental" | "cdc". Defaults to "full_refresh"
	// when omitted.
	Mode string `json:"mode,omitempty"`
	// CursorField names the column that advances monotonically in
	// incremental mode. Optional.
	CursorField string `json:"cursor_field,omitempty"`
	// Extra holds plugin-specific metadata the host surfaces verbatim
	// in the UI without interpreting.
	Extra map[string]string `json:"extra,omitempty"`
}

// StreamColumn is one column in a Stream's schema hint.
type StreamColumn struct {
	Name string `json:"name"`
	Type string `json:"type,omitempty"` // "string" | "int" | "float" | "bool" | "timestamp" | "json"
}

// WorkUnit is one independent, schedulable piece of a stream's work
// (MsgWorkUnit), returned by `plan` (ADR-013 M3).
//
// UnitID is the plugin's own stable name for this piece within the
// stream ("page-1", "2024-01.csv", ...). The host derives its own
// durable instance key from it rather than the plugin dictating that
// key directly — the same separation MsgState already draws between
// "what the plugin knows" (the cursor value) and "what the host owns"
// (persisting and replaying it).
//
// Params is opaque to the host: whatever the plugin needs on a later
// `read` call to fetch exactly this piece (a page offset, a file path,
// a partition predicate) and nothing more. The host passes it back
// unchanged via ReadParams.Unit.
type WorkUnit struct {
	UnitID string                 `json:"unit_id"`
	Params map[string]interface{} `json:"params,omitempty"`
	// Label is optional human-readable detail for logs/UI ("page 6 of
	// ~unknown"), the same role Progress.Message plays.
	Label string `json:"label,omitempty"`
}

// Progress carries a structured progress report from a running plugin
// (MsgProgress). Every field is optional: a connector reports whichever
// dimensions it can actually measure, and the host renders what it gets.
//
// Current/Total are pointers rather than plain ints because "page 0 of
// 100" and "no idea which page" are different states that a zero value
// cannot distinguish — a nil Total specifically means the total is
// unknown or indeterminate (a cursor-paginated API that can't know how
// many pages remain), not zero.
//
// Deliberately omitted from this struct: run_id, logical_node_id and
// instance_id, which RFC §17's example includes. The host already knows
// all three from extensions.ExecutionContext; having the plugin restate
// them adds no information and invites disagreement about which is
// authoritative.
type Progress struct {
	Current *int64 `json:"current,omitempty"`
	Total   *int64 `json:"total,omitempty"` // nil = unknown/indeterminate
	Unit    string `json:"unit,omitempty"`  // "pages", "rows", "files", ...

	RowsIn   int64 `json:"rows_in,omitempty"`
	RowsOut  int64 `json:"rows_out,omitempty"`
	BytesIn  int64 `json:"bytes_in,omitempty"`
	BytesOut int64 `json:"bytes_out,omitempty"`

	// Rate is progress per second in Unit terms, as measured by the
	// plugin - the host does not derive or verify it.
	Rate float64 `json:"rate,omitempty"`

	// Message is optional human-readable detail ("Fetched offset 6000")
	Message string `json:"message,omitempty"`

	// Timestamp is when the plugin observed this state (RFC3339)
	// NewProgress fills it when empty.
	Timestamp string `json:"timestamp,omitempty"`
}

// Config is the opaque configuration a plugin receives on stdin. Its
// shape is defined by the plugin's own config schema (advertised in
// the manifest as a JSON Schema).
type Config map[string]interface{}

// ReadParams carries stream + incremental state to a `read` invocation.
// Serialized as a single JSON object on the plugin's stdin, wrapping
// the user's Config under a "config" key so the plugin can tell them
// apart without needing a separate channel.
type ReadParams struct {
	Config Config                 `json:"config"`
	Stream string                 `json:"stream"`
	State  map[string]interface{} `json:"state,omitempty"`
	// Unit carries a WorkUnit.Params from a prior `plan` call, when the
	// host is reading one planned piece of the stream rather than the
	// whole thing at once. Empty for every plugin that doesn't
	// implement `plan` — reading a stream without planning it first
	// behaves exactly as it always has.
	Unit map[string]interface{} `json:"unit,omitempty"`
}

// WriteParams is the stdin header for a `write` invocation. The host
// writes this line first, then streams RecordMessages.
type WriteParams struct {
	Config Config `json:"config"`
	Stream string `json:"stream"`
}

// CheckParams is the stdin payload for `check`. Just the config; the
// plugin's job is to validate it and report back.
type CheckParams struct {
	Config Config `json:"config"`
}

// DiscoverParams is the stdin payload for `discover`.
type DiscoverParams struct {
	Config Config `json:"config"`
}

// PlanParams is the stdin payload for `plan`. Same shape as
// DiscoverParams plus a stream name: planning is always scoped to one
// already-discovered stream, never the whole connector at once.
type PlanParams struct {
	Config Config `json:"config"`
	Stream string `json:"stream"`
}

// EncodeLine writes a Message as a single JSON line with a trailing
// newline, matching what plugins emit on stdout. Used both by
// reference/test plugins and by the SDK.
func EncodeLine(w io.Writer, m Message) error {
	buf, err := json.Marshal(m)
	if err != nil {
		return fmt.Errorf("encode message: %w", err)
	}
	if _, err := w.Write(buf); err != nil {
		return err
	}
	if _, err := w.Write([]byte{'\n'}); err != nil {
		return err
	}
	return nil
}

// DecodeStream reads JSONL from r and yields Messages via the handler.
// Stops on EOF or the first decode error. Lines that don't parse as a
// valid Message are reported via the handler with Type=MsgLog and a
// synthetic warn-level message — we don't fail the stream on garbage
// because a plugin's stderr might accidentally land on stdout if the
// author misuses `print()`, and we'd rather surface that as a warning
// than abort the whole run.
func DecodeStream(r io.Reader, handle func(Message) error) error {
	scanner := bufio.NewScanner(r)
	// Allow long lines — record payloads can be large.
	scanner.Buffer(make([]byte, 1<<20), 64<<20) // 64 MiB max
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 || allWhitespace(line) {
			continue
		}
		var m Message
		if err := json.Unmarshal(line, &m); err != nil {
			// Not JSON — treat as a plugin misprint and surface as warn.
			if herr := handle(Message{
				Type:    MsgLog,
				Level:   LogWarn,
				Message: "plugin wrote non-JSON line to stdout: " + strings.TrimSpace(string(line)),
			}); herr != nil {
				return herr
			}
			continue
		}
		if err := handle(m); err != nil {
			return err
		}
	}
	return scanner.Err()
}

func allWhitespace(b []byte) bool {
	for _, c := range b {
		if c != ' ' && c != '\t' && c != '\n' && c != '\r' {
			return false
		}
	}
	return true
}

// NewRecord constructs a MsgRecord Message with the current time as
// the emitted_at field. Used by SDK helpers.
func NewRecord(data map[string]interface{}) Message {
	return Message{
		Type:      MsgRecord,
		Data:      data,
		EmittedAt: time.Now().UTC().Format(time.RFC3339Nano),
	}
}

// NewState constructs a MsgState Message.
func NewState(value map[string]interface{}) Message {
	return Message{Type: MsgState, Value: value}
}

// NewLog constructs a MsgLog Message at the given level.
func NewLog(level LogLevel, msg string) Message {
	return Message{Type: MsgLog, Level: level, Message: msg}
}

// NewError constructs a MsgError Message.
func NewError(msg string) Message {
	return Message{Type: MsgError, Message: msg}
}

// NewStatus constructs a MsgStatus Message. code should be "ok" or "error".
func NewStatus(code, msg string) Message {
	return Message{Type: MsgStatus, StatusCode: code, Message: msg}
}

// NewProgress constructs a MsgProgress Message, stamping Timestamp with
// the current time when the caller left it empty. Used by SDK helpers
// and reference plugins.
func NewProgress(p Progress) Message {
	if p.Timestamp == "" {
		p.Timestamp = time.Now().UTC().Format(time.RFC3339Nano)
	}

	return Message{Type: MsgProgress, Progress: &p}
}

// NewWorkUnit constructs a MsgWorkUnit Message. Used by SDK helpers and
// reference plugins that implement `plan` in Go rather than shell.
func NewWorkUnit(u WorkUnit) Message {
	return Message{Type: MsgWorkUnit, WorkUnit: &u}
}

// IsProtocolVersionSupported returns true if the host can speak the
// given plugin protocol version.
func IsProtocolVersionSupported(v int) bool {
	for _, ok := range SupportedProtocolVersions {
		if ok == v {
			return true
		}
	}
	return false
}
