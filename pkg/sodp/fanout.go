package sodp

import (
	"sync"

	"github.com/vmihailenco/msgpack/v5"
)

// Subscriber represents one active WATCH from a session.
type Subscriber struct {
	SessionID string
	StreamID  uint32
	OrgID     string         // tenant scope — subscriber only receives matching keys
	Send      chan<- []byte   // pre-encoded frame bytes
}

// FanoutBus manages per-key subscriber lists and broadcasts pre-encoded
// delta frames to all watchers. The body is encoded once; per-subscriber
// cost is only the 4-element envelope with their stream_id.
type FanoutBus struct {
	mu   sync.RWMutex
	subs map[string][]Subscriber // key → watchers
}

// NewFanoutBus creates an empty fanout bus.
func NewFanoutBus() *FanoutBus {
	return &FanoutBus{
		subs: make(map[string][]Subscriber),
	}
}

// Subscribe adds a subscriber for a state key.
func (fb *FanoutBus) Subscribe(key string, sub Subscriber) {
	fb.mu.Lock()
	defer fb.mu.Unlock()
	fb.subs[key] = append(fb.subs[key], sub)
}

// Unsubscribe removes a subscriber by session and stream ID.
func (fb *FanoutBus) Unsubscribe(key string, sessionID string, streamID uint32) {
	fb.mu.Lock()
	defer fb.mu.Unlock()

	subs := fb.subs[key]
	for i, s := range subs {
		if s.SessionID == sessionID && s.StreamID == streamID {
			subs[i] = subs[len(subs)-1]
			fb.subs[key] = subs[:len(subs)-1]
			return
		}
	}
}

// RemoveSession purges all subscriptions for a disconnected session.
func (fb *FanoutBus) RemoveSession(sessionID string) {
	fb.mu.Lock()
	defer fb.mu.Unlock()

	for key, subs := range fb.subs {
		filtered := subs[:0]
		for _, s := range subs {
			if s.SessionID != sessionID {
				filtered = append(filtered, s)
			}
		}
		if len(filtered) == 0 {
			delete(fb.subs, key)
		} else {
			fb.subs[key] = filtered
		}
	}
}

// Broadcast encodes a delta entry's body ONCE, then composes the frame
// envelope per-subscriber with their specific stream_id.
// Skips the sender session and subscribers whose OrgID doesn't match the key's org.
func (fb *FanoutBus) Broadcast(delta DeltaEntry, senderSessionID string, keyOrgID string) {
	// Encode the delta body to raw msgpack bytes — done once
	bodyBytes, err := msgpack.Marshal(delta)
	if err != nil {
		return
	}
	rawBody := msgpack.RawMessage(bodyBytes)

	fb.mu.RLock()
	subs := fb.subs[delta.Key]
	snapshot := make([]Subscriber, len(subs))
	copy(snapshot, subs)
	fb.mu.RUnlock()

	for _, sub := range snapshot {
		if sub.SessionID == senderSessionID {
			continue
		}
		// Tenant isolation: skip subscribers from a different org
		// "default" org sees everything (community/single-tenant mode)
		if sub.OrgID != "default" && keyOrgID != "" && sub.OrgID != keyOrgID {
			continue
		}

		// Compose frame: [type, stream_id, seq, raw_body]
		// Body is pre-encoded RawMessage — not re-serialized
		frame, err := msgpack.Marshal([]any{
			uint8(FrameDelta),
			sub.StreamID,
			delta.Version,
			rawBody,
		})
		if err != nil {
			continue
		}

		select {
		case sub.Send <- frame:
		default:
			// Subscriber buffer full — drop (slow client protection)
		}
	}
}

// BroadcastAll sends a delta to subscribers of the specific key AND any
// subscribers watching a parent prefix. For example, a delta on "runs.abc"
// is delivered to watchers of "runs.abc" and "runs".
func (fb *FanoutBus) BroadcastAll(delta DeltaEntry, senderSessionID string, keyOrgID string) {
	fb.Broadcast(delta, senderSessionID, keyOrgID)

	// Walk up the key hierarchy
	key := delta.Key
	for i := len(key) - 1; i >= 0; i-- {
		if key[i] == '.' {
			parent := key[:i]
			parentDelta := DeltaEntry{
				Key:     parent,
				Version: delta.Version,
				Ops:     delta.Ops,
			}
			fb.Broadcast(parentDelta, senderSessionID, keyOrgID)
			key = parent
		}
	}
}

// SubscriberCount returns the number of active watchers for a key.
func (fb *FanoutBus) SubscriberCount(key string) int {
	fb.mu.RLock()
	defer fb.mu.RUnlock()
	return len(fb.subs[key])
}
