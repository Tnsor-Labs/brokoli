package sodp

import (
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	defaultDeltaLogCap = 1000
	defaultMaxKeys     = 100_000 // hard cap on total state keys
)

// stateEntry is a versioned value in the state store.
type stateEntry struct {
	Value   any
	Version uint64
}

// StateStore is a thread-safe, versioned key-value store with an append-only
// delta log for each key. It is the core data structure of an SODP server.
type StateStore struct {
	mu      sync.RWMutex
	entries map[string]stateEntry
	deltas  map[string]*ringLog // per-key delta ring buffer
	global  atomic.Uint64       // global monotonic version
	logCap  int                 // max delta entries per key
	maxKeys int                 // hard cap on total keys
}

// ringLog is a fixed-capacity circular buffer for delta entries.
// O(1) append, O(n) scan for DeltasSince (but n is capped at logCap).
type ringLog struct {
	buf  []DeltaEntry
	head int // next write position
	len  int // entries currently stored
	cap  int
}

func newRingLog(cap int) *ringLog {
	return &ringLog{buf: make([]DeltaEntry, cap), cap: cap}
}

func (r *ringLog) push(e DeltaEntry) {
	r.buf[r.head] = e
	r.head = (r.head + 1) % r.cap
	if r.len < r.cap {
		r.len++
	}
}

// oldest returns the index of the oldest entry.
func (r *ringLog) oldest() int {
	if r.len < r.cap {
		return 0
	}
	return r.head // head is one past the newest, which wraps to oldest
}

// scan calls fn for each entry in chronological order. Stops if fn returns false.
func (r *ringLog) scan(fn func(DeltaEntry) bool) {
	start := r.oldest()
	for i := 0; i < r.len; i++ {
		idx := (start + i) % r.cap
		if !fn(r.buf[idx]) {
			return
		}
	}
}

// oldestVersion returns the version of the oldest entry, or 0 if empty.
func (r *ringLog) oldestVersion() uint64 {
	if r.len == 0 {
		return 0
	}
	return r.buf[r.oldest()].Version
}

// NewStateStore creates an empty state store.
func NewStateStore() *StateStore {
	return &StateStore{
		entries: make(map[string]stateEntry),
		deltas:  make(map[string]*ringLog),
		logCap:  defaultDeltaLogCap,
		maxKeys: defaultMaxKeys,
	}
}

// Apply atomically mutates a key: computes the diff, increments the version,
// stores the new value, and appends to the delta log. Returns the delta entry
// for fanout. If the diff is empty (no change), returns nil.
func (s *StateStore) Apply(key string, newValue any) *DeltaEntry {
	s.mu.Lock()
	defer s.mu.Unlock()

	old, exists := s.entries[key]
	if !exists && len(s.entries) >= s.maxKeys {
		return nil // hard cap reached — refuse new keys silently
	}

	ops := Diff(old.Value, newValue)
	if len(ops) == 0 {
		return nil
	}

	ver := s.global.Add(1)
	s.entries[key] = stateEntry{Value: newValue, Version: ver}

	entry := DeltaEntry{Key: key, Version: ver, Ops: ops}
	s.appendDelta(key, entry)

	return &entry
}

// Append atomically appends an element to a slice-typed state key.
// Emits an O(1) ADD op at JSON Pointer "/-" (RFC 6901 array append) plus,
// when the slice exceeds maxLen, a REMOVE op for the dropped head.
func (s *StateStore) Append(key string, element any, maxLen int) *DeltaEntry {
	s.mu.Lock()
	defer s.mu.Unlock()

	old := s.entries[key]
	var slice []any
	if old.Value != nil {
		slice, _ = old.Value.([]any)
	}

	// Copy to avoid mutating the stored slice
	newSlice := make([]any, len(slice)+1)
	copy(newSlice, slice)
	newSlice[len(slice)] = element

	ops := []DeltaOp{{Op: OpAdd, Path: "/-", Value: element}}

	if maxLen > 0 && len(newSlice) > maxLen {
		// Slice grew past the cap — drop the oldest entry (index 0).
		newSlice = newSlice[len(newSlice)-maxLen:]
		ops = append(ops, DeltaOp{Op: OpRemove, Path: "/0"})
	}

	ver := s.global.Add(1)
	s.entries[key] = stateEntry{Value: newSlice, Version: ver}

	entry := DeltaEntry{Key: key, Version: ver, Ops: ops}
	s.appendDelta(key, entry)
	return &entry
}

// Delete removes a key from the store and appends a remove delta.
func (s *StateStore) Delete(key string) *DeltaEntry {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, exists := s.entries[key]; !exists {
		return nil
	}

	ver := s.global.Add(1)
	delete(s.entries, key)

	entry := DeltaEntry{
		Key:     key,
		Version: ver,
		Ops:     []DeltaOp{{Op: OpRemove, Path: "/"}},
	}
	s.appendDelta(key, entry)

	return &entry
}

// Get returns the current value and version for a key.
// Returns nil, 0 if the key does not exist.
func (s *StateStore) Get(key string) (any, uint64) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	e := s.entries[key]
	return e.Value, e.Version
}

// Snapshot returns all entries matching a key prefix (dot-separated hierarchy).
// An empty prefix returns everything — callers must validate prefix before calling.
func (s *StateStore) Snapshot(prefix string) map[string]stateEntry {
	s.mu.RLock()
	defer s.mu.RUnlock()

	result := make(map[string]stateEntry)
	for k, v := range s.entries {
		if prefix == "" || k == prefix || hasPrefix(k, prefix) {
			result[k] = v
		}
	}
	return result
}

// DeltasSince returns all delta entries for a key with version > sinceVersion.
// Returns nil if the history doesn't go back far enough (caller should send full state).
func (s *StateStore) DeltasSince(key string, sinceVersion uint64) []DeltaEntry {
	s.mu.RLock()
	defer s.mu.RUnlock()

	ring := s.deltas[key]
	if ring == nil || ring.len == 0 {
		return nil
	}

	// Check if our log covers the requested version
	if ring.oldestVersion() > sinceVersion+1 {
		return nil // gap — caller must send full STATE_INIT
	}

	var result []DeltaEntry
	ring.scan(func(d DeltaEntry) bool {
		if d.Version > sinceVersion {
			result = append(result, d)
		}
		return true
	})
	return result
}

// GlobalVersion returns the current global version counter.
func (s *StateStore) GlobalVersion() uint64 {
	return s.global.Load()
}

// KeyCount returns the number of state keys.
func (s *StateStore) KeyCount() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.entries)
}

// Keys returns all current key names.
func (s *StateStore) Keys() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	keys := make([]string, 0, len(s.entries))
	for k := range s.entries {
		keys = append(keys, k)
	}
	return keys
}

// appendDelta adds an entry to the per-key ring buffer (caller must hold write lock).
func (s *StateStore) appendDelta(key string, entry DeltaEntry) {
	ring := s.deltas[key]
	if ring == nil {
		ring = newRingLog(s.logCap)
		s.deltas[key] = ring
	}
	ring.push(entry)
}

// EvictCompleted removes run state (and all child keys) for runs whose
// "finished_at" timestamp is older than the given TTL. This prevents
// indefinite memory growth from accumulated completed/failed runs.
// Returns the number of keys evicted.
func (s *StateStore) EvictCompleted(ttl time.Duration) int {
	cutoff := time.Now().Add(-ttl)
	evicted := 0

	s.mu.Lock()
	defer s.mu.Unlock()

	// First pass: find completed run keys past TTL
	var deadPrefixes []string
	for k, entry := range s.entries {
		if !strings.HasPrefix(k, "runs.") {
			continue
		}
		// Only check top-level run keys (runs.{id}), not children
		if strings.Count(k, ".") != 1 {
			continue
		}
		m, ok := entry.Value.(map[string]any)
		if !ok {
			continue
		}
		status, _ := m["status"].(string)
		if status != "success" && status != "failed" && status != "cancelled" {
			continue
		}
		finishedStr, _ := m["finished_at"].(string)
		if finishedStr == "" {
			continue
		}
		finished, err := time.Parse(time.RFC3339, finishedStr)
		if err != nil {
			continue
		}
		if finished.Before(cutoff) {
			deadPrefixes = append(deadPrefixes, k)
		}
	}

	// Second pass: delete run key and all children (nodes, logs)
	for _, prefix := range deadPrefixes {
		for k := range s.entries {
			if k == prefix || hasPrefix(k, prefix) {
				delete(s.entries, k)
				delete(s.deltas, k)
				evicted++
			}
		}
	}

	return evicted
}

// hasPrefix checks if key starts with prefix followed by a dot separator.
func hasPrefix(key, prefix string) bool {
	if len(key) <= len(prefix) {
		return false
	}
	return key[:len(prefix)] == prefix && key[len(prefix)] == '.'
}
