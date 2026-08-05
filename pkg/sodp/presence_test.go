package sodp

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestIntegration_PresenceClearedOnRealDisconnect is the full end-to-end
// proof for issue #55: a real WebSocket disconnect (not a direct
// cleanupPresence call) must trigger the cleanup wiring inside
// handleConnection. A second, separate connection watches the state key
// and asserts it receives the removal DELTA — readFrame's built-in 2s
// deadline is what absorbs the cleanup running asynchronously in the
// disconnecting connection's own goroutine.
func TestIntegration_PresenceClearedOnRealDisconnect(t *testing.T) {
	sodpSrv := NewServer()
	mux := http.NewServeMux()
	mux.HandleFunc("/api/ws", sodpSrv.HandleWS)
	ts := httptest.NewServer(mux)
	defer ts.Close()

	setter := dial(t, ts)
	readFrame(t, setter) // HELLO

	sendFrame(t, setter, Frame{
		Type:     FrameCall,
		StreamID: 1,
		Body: map[string]any{
			"call_id": "c1",
			"method":  "state.presence",
			"args": map[string]any{
				"state": "runs.r1",
				"path":  "viewers.alice",
				"value": true,
			},
		},
	})
	result := readFrame(t, setter)
	if result.Type != FrameResult {
		t.Fatalf("expected RESULT, got 0x%02x", result.Type)
	}

	watcher := dial(t, ts)
	defer watcher.Close()
	readFrame(t, watcher) // HELLO
	sendFrame(t, watcher, Frame{
		Type:     FrameWatch,
		StreamID: 10,
		Body:     map[string]any{"state": "runs.r1"},
	})
	init := readFrame(t, watcher)
	if init.Type != FrameStateInit {
		t.Fatalf("expected STATE_INIT, got 0x%02x", init.Type)
	}
	initVal := bodyMap(t, init.Body)["value"].(map[string]any)
	viewers := initVal["viewers"].(map[string]any)
	if viewers["alice"] != true {
		t.Fatalf("expected viewers.alice=true before disconnect, got %v", initVal)
	}

	// Real disconnect — not a direct cleanupPresence call.
	setter.Close()

	delta := readFrame(t, watcher)
	if delta.Type != FrameDelta {
		t.Fatalf("expected DELTA after setter disconnected, got 0x%02x", delta.Type)
	}

	val, _ := sodpSrv.State.Get("runs.r1")
	m := val.(map[string]any)
	after := m["viewers"].(map[string]any)
	if _, exists := after["alice"]; exists {
		t.Errorf("expected viewers.alice removed after real disconnect, got %v", after)
	}
}

func TestRemoveIn(t *testing.T) {
	current := map[string]any{
		"viewers": map[string]any{"alice": true, "bob": true},
		"other":   "unchanged",
	}
	result := removeIn(current, "viewers.alice")
	m := result.(map[string]any)
	viewers := m["viewers"].(map[string]any)
	if _, exists := viewers["alice"]; exists {
		t.Errorf("expected alice removed, got %v", viewers)
	}
	if viewers["bob"] != true {
		t.Errorf("expected bob preserved, got %v", viewers)
	}
	if m["other"] != "unchanged" {
		t.Errorf("expected sibling key preserved, got %v", m["other"])
	}
}

func TestRemoveIn_MissingPathIsNoOp(t *testing.T) {
	current := map[string]any{"a": map[string]any{"b": 1}}
	result := removeIn(current, "a.c.d")
	m := result.(map[string]any)
	a := m["a"].(map[string]any)
	if a["b"] != 1 {
		t.Errorf("expected untouched, got %v", m)
	}
}

func TestRemoveIn_NilCurrentIsNoOp(t *testing.T) {
	result := removeIn(nil, "a.b")
	if result != nil {
		t.Errorf("expected nil, got %v", result)
	}
}

func TestSession_PresenceEntries_DeduplicatesRepeatedRegistration(t *testing.T) {
	sess := NewSession("s1", "acme")
	sess.AddPresence("runs.r1", "viewers.alice")
	sess.AddPresence("runs.r1", "viewers.alice") // repeat registration — idempotent
	sess.AddPresence("runs.r1", "viewers.bob")

	entries := sess.PresenceEntries()
	if len(entries) != 2 {
		t.Fatalf("got %d entries, want 2 (deduplicated): %+v", len(entries), entries)
	}
}

// TestStatePresence_RegistersAndAppliesValue exercises state.presence
// through the real CALL dispatch path (mutation calls are only reachable
// for unauthenticated sessions — see TestAuthenticatedSessionCannotMutateServerState
// elsewhere in this package), confirming the value is set and the session
// is recorded as owning that path.
func TestStatePresence_RegistersAndAppliesValue(t *testing.T) {
	srv := NewServer()
	sess := NewSession("s1", "acme")

	srv.dispatch(sess, Frame{
		Type:     FrameCall,
		StreamID: 1,
		Body: map[string]any{
			"call_id": "c1",
			"method":  "state.presence",
			"args": map[string]any{
				"state": "runs.r1",
				"path":  "viewers.alice",
				"value": true,
			},
		},
	})

	val, _ := srv.State.Get("runs.r1")
	m, ok := val.(map[string]any)
	if !ok {
		t.Fatalf("expected a map, got %v", val)
	}
	viewers, ok := m["viewers"].(map[string]any)
	if !ok || viewers["alice"] != true {
		t.Fatalf("expected viewers.alice=true, got %v", m)
	}

	entries := sess.PresenceEntries()
	if len(entries) != 1 || entries[0] != (presenceEntry{state: "runs.r1", path: "viewers.alice"}) {
		t.Errorf("expected session to own runs.r1/viewers.alice, got %+v", entries)
	}
}

func TestStatePresence_MissingPathReturnsError(t *testing.T) {
	srv := NewServer()
	sess := NewSession("s1", "acme")

	srv.dispatch(sess, Frame{
		Type:     FrameCall,
		StreamID: 1,
		Body: map[string]any{
			"call_id": "c1",
			"method":  "state.presence",
			"args": map[string]any{
				"state": "runs.r1",
				"value": true,
			},
		},
	})

	select {
	case encoded := <-sess.Send:
		frame, err := DecodeFrame(encoded)
		if err != nil || frame.Type != FrameError {
			t.Fatalf("response = %#v, err = %v; want a 400 error", frame, err)
		}
	default:
		t.Fatal("expected an error response for missing path")
	}
	if len(sess.PresenceEntries()) != 0 {
		t.Error("a rejected call must not register a presence entry")
	}
}

// TestServer_CleanupPresence_RemovesPathAndBroadcasts is the core
// regression test for issue #55: cleanupPresence (called from
// handleConnection's cleanup block on disconnect/timeout) must remove
// every path the session registered and broadcast the resulting DELTA to
// subscribers, per SPEC §8.2's session-lifetime binding requirement.
func TestServer_CleanupPresence_RemovesPathAndBroadcasts(t *testing.T) {
	srv := NewServer()
	sess := NewSession("s1", "acme")

	srv.dispatch(sess, Frame{
		Type:     FrameCall,
		StreamID: 1,
		Body: map[string]any{
			"call_id": "c1",
			"method":  "state.presence",
			"args": map[string]any{
				"state": "runs.r1",
				"path":  "viewers.alice",
				"value": true,
			},
		},
	})
	// Drain the RESULT frame from the presence call so it doesn't get
	// mistaken for the cleanup broadcast below.
	<-sess.Send

	ch := make(chan []byte, 16)
	srv.Fanout.Subscribe("runs.r1", Subscriber{
		SessionID: "watcher",
		StreamID:  10,
		OrgID:     "acme",
		Send:      ch,
	})

	srv.cleanupPresence(sess)

	select {
	case msg := <-ch:
		if len(msg) == 0 {
			t.Error("received empty cleanup delta")
		}
	default:
		t.Fatal("expected a DELTA broadcast from presence cleanup")
	}

	val, _ := srv.State.Get("runs.r1")
	m := val.(map[string]any)
	viewers := m["viewers"].(map[string]any)
	if _, exists := viewers["alice"]; exists {
		t.Errorf("expected viewers.alice removed after cleanup, got %v", viewers)
	}
}

// TestServer_CleanupPresence_MultipleSessionsIndependentPaths confirms
// cleanupPresence only removes the disconnecting session's own paths,
// leaving another session's presence (even on the same state key) intact.
func TestServer_CleanupPresence_MultipleSessionsIndependentPaths(t *testing.T) {
	srv := NewServer()
	alice := NewSession("s1", "acme")
	bob := NewSession("s2", "acme")

	for _, c := range []struct {
		sess *Session
		who  string
	}{
		{alice, "alice"},
		{bob, "bob"},
	} {
		srv.dispatch(c.sess, Frame{
			Type:     FrameCall,
			StreamID: 1,
			Body: map[string]any{
				"call_id": "c1",
				"method":  "state.presence",
				"args": map[string]any{
					"state": "runs.r1",
					"path":  "viewers." + c.who,
					"value": true,
				},
			},
		})
		<-c.sess.Send
	}

	srv.cleanupPresence(alice)

	val, _ := srv.State.Get("runs.r1")
	viewers := val.(map[string]any)["viewers"].(map[string]any)
	if _, exists := viewers["alice"]; exists {
		t.Errorf("alice's presence should be removed, got %v", viewers)
	}
	if viewers["bob"] != true {
		t.Errorf("bob's presence must survive alice's disconnect, got %v", viewers)
	}
}

func TestServer_CleanupPresence_NoRegistrationsIsNoOp(t *testing.T) {
	srv := NewServer()
	sess := NewSession("s1", "acme")
	// Never called state.presence — must not panic or broadcast anything.
	srv.cleanupPresence(sess)
}
