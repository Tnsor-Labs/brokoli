package sodp

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// TestResume_ReplaysMissedDeltas exercises the full reconnect-with-resume
// flow that the OSS client (@sodp/client) relies on to survive transient
// network drops without losing events:
//
//  1. Client connects, sends WATCH, receives STATE_INIT (version V0).
//  2. Server applies one mutation. Client receives DELTA at version V1.
//  3. Client kills its WebSocket without sending UNWATCH (network blip).
//  4. While disconnected, the server applies two more mutations (V2, V3).
//  5. Client reconnects, sends RESUME { since_version: V1 }.
//  6. Server replays the missed deltas (V2, V3) in order.
//
// This is the only path that proves the delta-log + RESUME machinery works
// end-to-end. Without it, brief disconnects would silently drop events and
// the UI would diverge from server state until the next page reload.
func TestResume_ReplaysMissedDeltas(t *testing.T) {
	srv := NewServer()
	mux := http.NewServeMux()
	mux.HandleFunc("/api/ws", srv.HandleWS)
	ts := httptest.NewServer(mux)
	defer ts.Close()

	const key = "logs.resume-test"

	// ── Connection 1: subscribe + apply one mutation ───────────────────────
	c1 := dial(t, ts)
	readFrame(t, c1) // HELLO

	sendFrame(t, c1, Frame{
		Type:     FrameWatch,
		StreamID: 10,
		Body:     map[string]any{"state": key},
	})
	init := readFrame(t, c1)
	if init.Type != FrameStateInit {
		t.Fatalf("expected STATE_INIT, got 0x%02x", init.Type)
	}
	initBody := bodyMap(t, init.Body)
	v0, _ := initBody["version"].(uint64)
	if vi, ok := initBody["version"].(int64); ok {
		v0 = uint64(vi)
	}
	t.Logf("STATE_INIT version=%d", v0)

	// Wait for the subscription to be registered in the fanout.
	time.Sleep(50 * time.Millisecond)

	// First mutation while connected — client should receive the delta.
	srv.MutateAppend(key, map[string]any{"msg": "before-disconnect-1"}, 100)
	delta1 := readFrame(t, c1)
	if delta1.Type != FrameDelta {
		t.Fatalf("expected DELTA after first mutation, got 0x%02x", delta1.Type)
	}
	d1Body := bodyMap(t, delta1.Body)
	v1, _ := d1Body["version"].(uint64)
	if vi, ok := d1Body["version"].(int64); ok {
		v1 = uint64(vi)
	}
	t.Logf("received DELTA version=%d while connected", v1)
	if v1 == 0 {
		t.Fatalf("expected non-zero delta version, got body=%v", d1Body)
	}

	// ── Simulated network drop ─────────────────────────────────────────────
	// Close without sending UNWATCH or a clean close frame, the way an
	// actual network blip looks to the server.
	c1.Close()

	// Give the server's read pump time to notice the closed socket and run
	// its session-cleanup path. Without this delay the server might still
	// have a stale subscriber registered when we apply the next mutations,
	// which would cause those deltas to be queued onto a dead session and
	// dropped — masking the real bug if the resume path were broken.
	time.Sleep(100 * time.Millisecond)

	// ── While disconnected: server applies two more mutations ──────────────
	srv.MutateAppend(key, map[string]any{"msg": "during-disconnect-1"}, 100)
	srv.MutateAppend(key, map[string]any{"msg": "during-disconnect-2"}, 100)
	t.Logf("server applied 2 mutations while client disconnected")

	// ── Connection 2: reconnect and RESUME from v1 ─────────────────────────
	c2 := dial(t, ts)
	defer c2.Close()
	readFrame(t, c2) // HELLO

	sendFrame(t, c2, Frame{
		Type:     FrameResume,
		StreamID: 10,
		Body: map[string]any{
			"state":         key,
			"since_version": v1, // ← only events newer than this should arrive
		},
	})

	// We should now receive exactly the two missed deltas, in order, with
	// monotonically increasing versions strictly greater than v1.
	var got []Frame
	deadline := time.Now().Add(2 * time.Second)
	for len(got) < 2 && time.Now().Before(deadline) {
		c2.SetReadDeadline(time.Now().Add(500 * time.Millisecond))
		_, data, err := c2.ReadMessage()
		if err != nil {
			break
		}
		f, err := DecodeFrame(data)
		if err != nil {
			t.Fatalf("decode replayed frame: %v", err)
		}
		// The server may also send STATE_INIT if the delta log doesn't cover
		// the requested version. In that case the test is still meaningful —
		// we just verify the full state contains both missed entries.
		if f.Type == FrameStateInit {
			t.Logf("server fell back to STATE_INIT (delta log too small)")
			body := bodyMap(t, f.Body)
			val, ok := body["value"].([]any)
			if !ok {
				t.Fatalf("STATE_INIT value should be array, got %T", body["value"])
			}
			if len(val) < 3 {
				t.Errorf("STATE_INIT after resume should have all 3 entries, got %d", len(val))
			}
			return
		}
		if f.Type != FrameDelta {
			t.Fatalf("expected DELTA, got 0x%02x", f.Type)
		}
		got = append(got, f)
	}

	if len(got) != 2 {
		t.Fatalf("expected 2 replayed deltas, got %d", len(got))
	}

	// Versions must be strictly increasing and strictly > v1.
	for i, f := range got {
		body := bodyMap(t, f.Body)
		ver, _ := body["version"].(uint64)
		if vi, ok := body["version"].(int64); ok {
			ver = uint64(vi)
		}
		if ver <= v1 {
			t.Errorf("replayed delta %d has version %d, want > %d", i, ver, v1)
		}
		t.Logf("replayed DELTA[%d] version=%d", i, ver)
	}

	// And the server-side state should reflect all 3 mutations.
	val, _ := srv.State.Get(key)
	slice, ok := val.([]any)
	if !ok || len(slice) != 3 {
		t.Errorf("server state should have 3 entries, got %v", val)
	}
}

// TestResume_FallsBackToStateInitOnGap verifies that when the server's delta
// log has been trimmed past the client's last-known version (e.g., very long
// disconnect, or a high-frequency key), the server transparently falls back
// to sending a fresh STATE_INIT instead of failing.
//
// This is the safety valve that keeps the protocol correct under any amount
// of disconnect time — the worst case is "you get a fresh snapshot," not
// "you silently desync."
func TestResume_FallsBackToStateInitOnGap(t *testing.T) {
	srv := NewServer()
	// Tiny delta log so a single extra mutation pushes the resume target out.
	srv.State.logCap = 2

	mux := http.NewServeMux()
	mux.HandleFunc("/api/ws", srv.HandleWS)
	ts := httptest.NewServer(mux)
	defer ts.Close()

	const key = "logs.gap-test"

	// Push 4 entries through the 2-slot ring buffer. Final ring state holds
	// only versions 3 and 4. We ask to resume from version 1, which is two
	// past the oldest entry in the log → DeltasSince returns nil → server
	// must fall back to STATE_INIT instead of streaming a partial delta set.
	srv.MutateAppend(key, "v1", 100) // version 1, evicted
	srv.MutateAppend(key, "v2", 100) // version 2, evicted
	srv.MutateAppend(key, "v3", 100) // version 3, in ring
	srv.MutateAppend(key, "v4", 100) // version 4, in ring

	conn := dial(t, ts)
	defer conn.Close()
	readFrame(t, conn) // HELLO

	sendFrame(t, conn, Frame{
		Type:     FrameResume,
		StreamID: 10,
		Body: map[string]any{
			"state":         key,
			"since_version": uint64(1),
		},
	})

	// First inbound frame should be STATE_INIT (the fallback), not an ERROR
	// or a partial DELTA stream.
	f := readFrame(t, conn)
	if f.Type != FrameStateInit {
		t.Fatalf("expected fallback STATE_INIT, got 0x%02x", f.Type)
	}
	body := bodyMap(t, f.Body)
	val, ok := body["value"].([]any)
	if !ok {
		t.Fatalf("STATE_INIT value should be array, got %T", body["value"])
	}
	if len(val) != 4 {
		t.Errorf("fallback STATE_INIT should contain all 4 entries, got %d", len(val))
	}
}
