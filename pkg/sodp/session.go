package sodp

import (
	"sync"
	"sync/atomic"
	"time"
)

const (
	defaultSessionBuffer = 128
	defaultRateLimit     = 100 // mutations per second
	defaultMaxWatches    = 64  // max concurrent WATCH subscriptions per session
	firstStreamID        = 10  // stream 0 reserved for control
)

// Session represents one authenticated WebSocket connection.
type Session struct {
	ID     string
	OrgID  string         // tenant isolation — set at connect, immutable after auth
	Sub    string         // JWT subject claim
	Claims map[string]any // all JWT claims
	Send   chan []byte    // outbound pre-encoded frames
	Done   chan struct{}  // closed on disconnect

	mu         sync.Mutex
	watches    map[uint32]string // stream_id → watched key
	nextStream atomic.Uint32
	authed     bool // true after successful AUTH or if HTTP middleware pre-authenticated
	maxWatches int

	// Rate limiting (simple 1-second fixed window)
	rateCount atomic.Int64
	rateReset atomic.Int64 // unix timestamp of current window end
	rateLimit int
}

// NewSession creates a session for a new connection.
func NewSession(id, orgID string) *Session {
	s := &Session{
		ID:         id,
		OrgID:      orgID,
		Claims:     make(map[string]any),
		Send:       make(chan []byte, defaultSessionBuffer),
		Done:       make(chan struct{}),
		watches:    make(map[uint32]string),
		rateLimit:  defaultRateLimit,
		maxWatches: defaultMaxWatches,
	}
	s.nextStream.Store(firstStreamID)
	return s
}

// AllocStream allocates the next stream ID for a new WATCH.
func (s *Session) AllocStream() uint32 {
	return s.nextStream.Add(1) - 1
}

// AddWatch records a WATCH subscription. Returns false if the watch limit is reached.
func (s *Session) AddWatch(streamID uint32, key string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.watches) >= s.maxWatches {
		return false
	}
	s.watches[streamID] = key
	return true
}

// RemoveWatch removes a WATCH subscription, returning the key.
func (s *Session) RemoveWatch(streamID uint32) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key, ok := s.watches[streamID]
	if ok {
		delete(s.watches, streamID)
	}
	return key, ok
}

// WatchCount returns the number of active watches.
func (s *Session) WatchCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.watches)
}

// Watches returns a copy of active watches.
func (s *Session) Watches() map[uint32]string {
	s.mu.Lock()
	defer s.mu.Unlock()
	cp := make(map[uint32]string, len(s.watches))
	for k, v := range s.watches {
		cp[k] = v
	}
	return cp
}

// CheckRate returns true if the mutation is within the rate limit.
func (s *Session) CheckRate() bool {
	now := time.Now().Unix()
	reset := s.rateReset.Load()
	if now > reset {
		s.rateCount.Store(1)
		s.rateReset.Store(now + 1)
		return true
	}
	return s.rateCount.Add(1) <= int64(s.rateLimit)
}

// IsAuthenticated returns whether the session has completed auth.
func (s *Session) IsAuthenticated() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.authed
}

// MarkAuthenticated marks the session as authenticated. Idempotent.
func (s *Session) MarkAuthenticated() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.authed = true
}

// Close signals session termination.
func (s *Session) Close() {
	select {
	case <-s.Done:
	default:
		close(s.Done)
	}
}

// SetAuth stores authentication details on the session.
// OrgID is only updated on the first auth call — subsequent calls cannot change it.
func (s *Session) SetAuth(sub string, claims map[string]any) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Sub = sub
	s.Claims = claims
	// Only set OrgID if this is the first authentication (prevent tenant escape)
	if !s.authed {
		if orgID, ok := claims["org_id"].(string); ok && orgID != "" {
			s.OrgID = orgID
		}
	}
	s.authed = true
}
