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
