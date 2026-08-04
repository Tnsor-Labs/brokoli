// Package sodp implements the State-Oriented Data Protocol (SODP) v0.1
// as an in-process Go library for real-time state synchronization over WebSockets.
//
// Wire format: each message is a 4-element MessagePack array:
//
//	[frame_type(u8), stream_id(u32), seq(u64), body(any)]
package sodp

import (
	"fmt"

	"github.com/vmihailenco/msgpack/v5"
)

// FrameType identifies the SODP message type.
type FrameType uint8

const (
	FrameHello     FrameType = 0x01
	FrameWatch     FrameType = 0x02
	FrameStateInit FrameType = 0x03
	FrameDelta     FrameType = 0x04
	FrameCall      FrameType = 0x05
	FrameResult    FrameType = 0x06
	FrameError     FrameType = 0x07
	FrameAck       FrameType = 0x08
	FrameHeartbeat FrameType = 0x09
	FrameResume    FrameType = 0x0A
	FrameAuth      FrameType = 0x0B
	FrameAuthOK    FrameType = 0x0C
	FrameUnwatch   FrameType = 0x0D
)

// Frame is a decoded SODP wire message.
type Frame struct {
	Type     FrameType
	StreamID uint32
	Seq      uint64
	Body     any
}

// EncodeFrame serializes a Frame into MessagePack bytes.
func EncodeFrame(f Frame) ([]byte, error) {
	return msgpack.Marshal([]any{uint8(f.Type), f.StreamID, f.Seq, f.Body})
}

// DecodeFrame deserializes MessagePack bytes into a Frame.
func DecodeFrame(data []byte) (Frame, error) {
	var raw []msgpack.RawMessage
	if err := msgpack.Unmarshal(data, &raw); err != nil {
		return Frame{}, fmt.Errorf("sodp: invalid frame: %w", err)
	}
	if len(raw) != 4 {
		return Frame{}, fmt.Errorf("sodp: frame must have 4 elements, got %d", len(raw))
	}

	var ft uint8
	if err := msgpack.Unmarshal(raw[0], &ft); err != nil {
		return Frame{}, fmt.Errorf("sodp: invalid frame_type: %w", err)
	}
	var streamID uint32
	if err := msgpack.Unmarshal(raw[1], &streamID); err != nil {
		return Frame{}, fmt.Errorf("sodp: invalid stream_id: %w", err)
	}
	var seq uint64
	if err := msgpack.Unmarshal(raw[2], &seq); err != nil {
		return Frame{}, fmt.Errorf("sodp: invalid seq: %w", err)
	}

	// Body: nil for bodyless frames (HEARTBEAT, ACK).
	// vmihailenco/msgpack decodes msgpack nil into an empty RawMessage.
	var body any
	if len(raw[3]) == 0 {
		body = nil
	} else if err := msgpack.Unmarshal(raw[3], &body); err != nil {
		return Frame{}, fmt.Errorf("sodp: invalid body: %w", err)
	}

	return Frame{
		Type:     FrameType(ft),
		StreamID: streamID,
		Seq:      seq,
		Body:     body,
	}, nil
}

// HelloBody is the server's initial handshake payload.
// The "auth" field tells @sodp/client whether to send an AUTH frame.
type HelloBody struct {
	Protocol string `msgpack:"protocol" json:"protocol"`
	Version  string `msgpack:"version" json:"version"`
	ServerID string `msgpack:"server_id" json:"server_id"`
	Auth     bool   `msgpack:"auth" json:"auth"` // true = AUTH frame required
}

// ProtocolVersion is the current SODP protocol version this server speaks
// and announces in HelloBody.Version.
//
// SupportedSODPVersions/IsSODPVersionSupported below mirror the shape of
// pkg/plugins/protocol.go's ProtocolVersion/SupportedProtocolVersions/
// IsProtocolVersionSupported — an existing, working version-negotiation
// pattern in this codebase for an unrelated protocol (see that file's
// package doc comment, and docs/adr/001-sodp-realtime-transport.md for why
// SODP and the plugin-invocation protocol are two different things despite
// both sometimes being called "the protocol" informally).
//
// The wire mechanism necessarily differs from plugins/protocol.go, though:
// a plugin declares its ProtocolVersion once, statically, in an on-disk
// manifest the host reads before ever launching the plugin process. SODP is
// a live WebSocket handshake with no client-side manifest to read up front
// — the server sends HELLO immediately on connect, before the client has
// said anything at all. So a SODP client instead declares the version it
// speaks via the SODPVersionParam query parameter on the WebSocket upgrade
// request (see Server.HandleWS), which the server can validate before
// upgrading the connection at all.
const ProtocolVersion = "0.1"

// SupportedSODPVersions lists every SODP protocol version this server can
// speak. Expand when supporting additional client versions; see
// Server.HandleWS for how an unsupported declared version is rejected.
var SupportedSODPVersions = []string{"0.1"}

// SODPVersionParam is the WebSocket upgrade query parameter a client uses
// to declare the SODP protocol version it speaks, e.g.
// "GET /api/ws?sodp_version=0.1". Optional: a client that omits it (every
// @sodp/client release to date has no way to set it) is treated as
// speaking the only version that has ever existed, ProtocolVersion — this
// keeps every existing client working unchanged. A client that DOES declare
// a version gets it validated against SupportedSODPVersions and the
// connection is rejected outright (before the WebSocket upgrade completes)
// if it isn't supported, rather than silently proceeding regardless of
// version as before this change.
const SODPVersionParam = "sodp_version"

// IsSODPVersionSupported reports whether this server can speak the given
// client-declared SODP protocol version.
func IsSODPVersionSupported(v string) bool {
	for _, ok := range SupportedSODPVersions {
		if ok == v {
			return true
		}
	}
	return false
}

// WatchBody is the client's subscription request.
// @sodp/client uses "state" (not "key") and "since_version" for RESUME.
type WatchBody struct {
	Key          string `msgpack:"state" json:"state"`
	SinceVersion uint64 `msgpack:"since_version,omitempty" json:"since_version,omitempty"`
}

// CallBody matches @sodp/client CALL frame: { call_id, method, args }.
type CallBody struct {
	CallID string         `msgpack:"call_id" json:"call_id"`
	Method string         `msgpack:"method" json:"method"`
	Args   map[string]any `msgpack:"args" json:"args"`
}

// ErrorBody carries error details.
type ErrorBody struct {
	Code    int    `msgpack:"code" json:"code"`
	Message string `msgpack:"message" json:"message"`
}

// AuthBody carries the JWT token from the client.
type AuthBody struct {
	Token string `msgpack:"token" json:"token"`
}

// AuthOKBody confirms authentication success.
type AuthOKBody struct {
	Subject string `msgpack:"sub" json:"sub"`
}
