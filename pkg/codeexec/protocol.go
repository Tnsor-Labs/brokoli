package codeexec

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
)

// The code-node worker protocol (ADR-029): a sibling of the plugin
// protocol with its discipline — versioned, refused on mismatch,
// advertised in capabilities — and a different wire format. Frames are
//
//	uint32 BE payload length | uint8 protocol version | uint8 type | JSON payload
//
// over a private Unix socket. Deliberately NOT the plugins JSONL codec:
// that codec tolerates garbage lines because plugin stdout catches
// stray print()s, and on a private socket that tolerance would be an
// anti-feature — corruption here means the worker is not speaking the
// protocol, and the only safe response is kill-and-respawn. JSON
// payloads inside binary frames keep both sides debuggable and leave
// room for future raw-binary row frames under new frame types.

// CodeProtocolVersion is the protocol this host speaks.
const CodeProtocolVersion = 1

// SupportedCodeProtocolVersions lists every version this host accepts
// in a worker's hello. Mirrors pkg/plugins' SupportedProtocolVersions
// discipline.
var SupportedCodeProtocolVersions = []int{1}

// MaxFramePayload bounds one frame. Inline rows ride frames only below
// the file-mode threshold, so this is generous headroom, not a limit
// anyone should meet.
const MaxFramePayload = 128 << 20

// Frame types. Host → worker.
const (
	FrameInit     byte = 0x01
	FrameExec     byte = 0x02
	FrameShutdown byte = 0x03
)

// Worker → host.
const (
	FrameHello    byte = 0x10
	FrameLog      byte = 0x11
	FrameProgress byte = 0x12
	FrameResult   byte = 0x13
	FrameError    byte = 0x14
)

// HelloMsg is the worker's first frame: identity and versions, refused
// by the host when the protocol version is unsupported.
type HelloMsg struct {
	ProtocolVersion int    `json:"protocol_version"`
	WrapperVersion  int    `json:"wrapper_version"`
	PythonVersion   string `json:"python_version"`
	Pid             int    `json:"pid"`
}

// InitMsg configures a fresh worker. Limits are applied from the
// environment before the socket connects (they must bound the
// interpreter itself); this echo exists so the worker can refuse a
// mismatch and so warmup imports are explicit.
type InitMsg struct {
	Warmup []string `json:"warmup,omitempty"`
}

// ExecInput describes one execution's input data plane. Exactly one
// mode: inline rows (small data) or an NDJSON file by reference —
// files, not FIFOs, per ADR-029's lazy-rows compatibility policy.
type ExecInput struct {
	Mode    string                   `json:"mode"` // "inline" | "ndjson" | "none"
	Rows    []map[string]interface{} `json:"rows,omitempty"`
	Columns []string                 `json:"columns,omitempty"`
	Path    string                   `json:"path,omitempty"`
}

// ExecOutput names where the execution's output goes.
type ExecOutput struct {
	Mode string `json:"mode"` // "ndjson": file written by the worker; inline results return via ResultMsg either way
	Path string `json:"path,omitempty"`
}

// ExecMsg is one invocation.
type ExecMsg struct {
	ExecID    string                 `json:"exec_id"`
	Script    string                 `json:"script"`
	Config    map[string]interface{} `json:"config"`
	Params    map[string]string      `json:"params"`
	Input     ExecInput              `json:"input"`
	Output    ExecOutput             `json:"output"`
	TimeoutMs int64                  `json:"timeout_ms"`
}

// LogMsg carries one line of the user script's stdout/stderr.
type LogMsg struct {
	ExecID  string `json:"exec_id"`
	Level   string `json:"level"`
	Message string `json:"message"`
}

// ProgressMsg is the typed successor of the v1 wrapper's #PROGRESS:
// stderr lines (which were emitted and discarded); the pool consumes
// these.
type ProgressMsg struct {
	ExecID  string `json:"exec_id"`
	Percent int    `json:"percent"`
	Message string `json:"message,omitempty"`
}

// ResultOutput is what one execution produced.
type ResultOutput struct {
	Mode        string                   `json:"mode"` // "ndjson" | "inline"
	Columns     []string                 `json:"columns"`
	Rows        []map[string]interface{} `json:"rows,omitempty"`
	Path        string                   `json:"path,omitempty"`
	RowsWritten int64                    `json:"rows_written"`
}

// ResultMsg completes an execution successfully.
type ResultMsg struct {
	ExecID string       `json:"exec_id"`
	Output ResultOutput `json:"output"`
}

// Error kinds a worker reports.
const (
	ErrKindUserException     = "user_exception"
	ErrKindMutationAfterPass = "mutation_after_pass"
	ErrKindResourceLimit     = "resource_limit"
	ErrKindInternal          = "internal"
)

// ErrorMsg completes an execution unsuccessfully.
type ErrorMsg struct {
	ExecID    string `json:"exec_id"`
	Kind      string `json:"kind"`
	Message   string `json:"message"`
	Traceback string `json:"traceback,omitempty"`
}

// WriteFrame encodes payload as JSON and writes one frame.
func WriteFrame(w io.Writer, frameType byte, payload interface{}) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("encode frame %#x: %w", frameType, err)
	}
	if len(body) > MaxFramePayload {
		return fmt.Errorf("frame %#x payload %d exceeds %d bytes", frameType, len(body), MaxFramePayload)
	}
	header := make([]byte, 6)
	binary.BigEndian.PutUint32(header[0:4], uint32(len(body)))
	header[4] = CodeProtocolVersion
	header[5] = frameType
	if _, err := w.Write(header); err != nil {
		return err
	}
	_, err = w.Write(body)
	return err
}

// ReadFrame reads one frame, refusing unsupported versions and
// oversized payloads. Any error here means the peer is not speaking
// the protocol — callers treat the worker as dead.
func ReadFrame(r io.Reader) (frameType byte, payload []byte, err error) {
	header := make([]byte, 6)
	if _, err := io.ReadFull(r, header); err != nil {
		return 0, nil, err
	}
	size := binary.BigEndian.Uint32(header[0:4])
	version := int(header[4])
	if !isCodeProtocolVersionSupported(version) {
		return 0, nil, fmt.Errorf("unsupported code protocol version %d (host speaks %v)", version, SupportedCodeProtocolVersions)
	}
	if size > MaxFramePayload {
		return 0, nil, fmt.Errorf("frame payload %d exceeds %d bytes", size, MaxFramePayload)
	}
	payload = make([]byte, size)
	if _, err := io.ReadFull(r, payload); err != nil {
		return 0, nil, err
	}
	return header[5], payload, nil
}

func isCodeProtocolVersionSupported(v int) bool {
	for _, s := range SupportedCodeProtocolVersions {
		if s == v {
			return true
		}
	}
	return false
}
