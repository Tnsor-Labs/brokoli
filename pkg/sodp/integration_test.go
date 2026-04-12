package sodp

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/vmihailenco/msgpack/v5"
)

// dial upgrades a test HTTP server to a WebSocket connection.
func dial(t *testing.T, srv *httptest.Server) *websocket.Conn {
	t.Helper()
	url := "ws" + strings.TrimPrefix(srv.URL, "http") + "/api/ws"
	conn, _, err := websocket.DefaultDialer.Dial(url, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	return conn
}

// readFrame reads a single binary frame and decodes it.
func readFrame(t *testing.T, conn *websocket.Conn) Frame {
	t.Helper()
	conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	_, data, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	f, err := DecodeFrame(data)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	return f
}

// sendFrame encodes and writes a frame to the connection.
func sendFrame(t *testing.T, conn *websocket.Conn, f Frame) {
	t.Helper()
	data, err := EncodeFrame(f)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	if err := conn.WriteMessage(websocket.BinaryMessage, data); err != nil {
		t.Fatalf("write: %v", err)
	}
}

// bodyMap extracts the body as map[string]any, handling msgpack's varying types.
func bodyMap(t *testing.T, body any) map[string]any {
	t.Helper()
	// msgpack may decode as map[string]any or require re-marshal
	if m, ok := body.(map[string]any); ok {
		return m
	}
	// Round-trip through msgpack to normalize
	data, err := msgpack.Marshal(body)
	if err != nil {
		t.Fatalf("marshal body: %v", err)
	}
	var m map[string]any
	if err := msgpack.Unmarshal(data, &m); err != nil {
		t.Fatalf("unmarshal body: %v", err)
	}
	return m
}

// --- Integration tests ---

func TestIntegration_HelloOnConnect(t *testing.T) {
	sodpSrv := NewServer()
	mux := http.NewServeMux()
	mux.HandleFunc("/api/ws", sodpSrv.HandleWS)
	ts := httptest.NewServer(mux)
	defer ts.Close()

	conn := dial(t, ts)
	defer conn.Close()

	// First frame must be HELLO
	f := readFrame(t, conn)
	if f.Type != FrameHello {
		t.Fatalf("expected HELLO (0x01), got 0x%02x", f.Type)
	}

	m := bodyMap(t, f.Body)
	if m["protocol"] != "sodp" {
		t.Errorf("protocol: %v", m["protocol"])
	}
	if m["version"] != "0.1" {
		t.Errorf("version: %v", m["version"])
	}
}

func TestIntegration_WatchAndStateInit(t *testing.T) {
	sodpSrv := NewServer()

	// Pre-populate state
	sodpSrv.Mutate("runs.abc", map[string]any{"status": "running", "org_id": ""})

	mux := http.NewServeMux()
	mux.HandleFunc("/api/ws", sodpSrv.HandleWS)
	ts := httptest.NewServer(mux)
	defer ts.Close()

	conn := dial(t, ts)
	defer conn.Close()

	// Read HELLO
	hello := readFrame(t, conn)
	if hello.Type != FrameHello {
		t.Fatalf("expected HELLO, got 0x%02x", hello.Type)
	}

	// Send WATCH for "runs.abc"
	sendFrame(t, conn, Frame{
		Type:     FrameWatch,
		StreamID: 10,
		Body:     map[string]any{"state": "runs.abc"},
	})

	// Should receive STATE_INIT
	init := readFrame(t, conn)
	if init.Type != FrameStateInit {
		t.Fatalf("expected STATE_INIT (0x03), got 0x%02x", init.Type)
	}
	if init.StreamID != 10 {
		t.Errorf("stream_id: got %d, want 10", init.StreamID)
	}

	m := bodyMap(t, init.Body)
	if m["state"] != "runs.abc" {
		t.Errorf("state: %v", m["state"])
	}
	if m["initialized"] != true {
		t.Error("STATE_INIT should have initialized=true for existing key")
	}
	// Value should be the map we set
	val, ok := m["value"].(map[string]any)
	if !ok {
		t.Fatalf("value not a map: %T", m["value"])
	}
	if val["status"] != "running" {
		t.Errorf("value.status: %v", val["status"])
	}
}

func TestIntegration_WatchReceivesDelta(t *testing.T) {
	sodpSrv := NewServer()

	mux := http.NewServeMux()
	mux.HandleFunc("/api/ws", sodpSrv.HandleWS)
	ts := httptest.NewServer(mux)
	defer ts.Close()

	conn := dial(t, ts)
	defer conn.Close()

	// Read HELLO
	readFrame(t, conn)

	// WATCH "_events" (will get STATE_INIT with empty/nil)
	sendFrame(t, conn, Frame{
		Type:     FrameWatch,
		StreamID: 10,
		Body:     map[string]any{"state": "_events"},
	})
	readFrame(t, conn) // STATE_INIT

	// Wait for subscription to be registered
	time.Sleep(50 * time.Millisecond)

	// Mutate from server side (simulates bridge)
	sodpSrv.MutateAppend("_events", map[string]any{
		"type":   "run.started",
		"run_id": "r1",
	}, 100)

	// Should receive DELTA
	delta := readFrame(t, conn)
	if delta.Type != FrameDelta {
		t.Fatalf("expected DELTA (0x04), got 0x%02x", delta.Type)
	}
}

func TestIntegration_BridgeToClient(t *testing.T) {
	sodpSrv := NewServer()

	// Start bridge
	bridgeCh := make(chan BridgeEvent, 16)
	Bridge(sodpSrv, bridgeCh)

	mux := http.NewServeMux()
	mux.HandleFunc("/api/ws", sodpSrv.HandleWS)
	ts := httptest.NewServer(mux)
	defer ts.Close()

	conn := dial(t, ts)
	defer conn.Close()

	// HELLO
	readFrame(t, conn)

	// WATCH the dashboard snapshot key the bridge maintains
	sendFrame(t, conn, Frame{
		Type:     FrameWatch,
		StreamID: 10,
		Body:     map[string]any{"state": "dashboard.default"},
	})
	readFrame(t, conn) // STATE_INIT

	time.Sleep(50 * time.Millisecond)

	// Pump a real event through the bridge
	bridgeCh <- BridgeEvent{
		Type:       "run.started",
		RunID:      "run-123",
		PipelineID: "pipe-1",
		Timestamp:  time.Now(),
	}

	// Client should receive a DELTA on dashboard.default with the new
	// aggregate snapshot reflecting the new running run.
	delta := readFrame(t, conn)
	if delta.Type != FrameDelta {
		t.Fatalf("expected DELTA, got 0x%02x", delta.Type)
	}

	// Also verify the run state was created
	val, ver := sodpSrv.State.Get("runs.run-123")
	if ver == 0 {
		t.Fatal("bridge should have created runs.run-123")
	}
	m, ok := val.(map[string]any)
	if !ok {
		t.Fatalf("expected map, got %T", val)
	}
	if m["status"] != "running" {
		t.Errorf("status: %v", m["status"])
	}
	if m["pipeline_id"] != "pipe-1" {
		t.Errorf("pipeline_id: %v", m["pipeline_id"])
	}
}

func TestIntegration_FullRunLifecycle(t *testing.T) {
	sodpSrv := NewServer()

	bridgeCh := make(chan BridgeEvent, 32)
	Bridge(sodpSrv, bridgeCh)

	mux := http.NewServeMux()
	mux.HandleFunc("/api/ws", sodpSrv.HandleWS)
	ts := httptest.NewServer(mux)
	defer ts.Close()

	conn := dial(t, ts)
	defer conn.Close()

	readFrame(t, conn) // HELLO

	// Watch the dashboard snapshot — it gets recomputed on every non-log
	// event, so each pipeline state transition produces a DELTA.
	sendFrame(t, conn, Frame{
		Type:     FrameWatch,
		StreamID: 10,
		Body:     map[string]any{"state": "dashboard.default"},
	})
	readFrame(t, conn) // STATE_INIT

	time.Sleep(50 * time.Millisecond)

	now := time.Now()

	// Simulate a complete pipeline run lifecycle.
	//
	// What we expect on the dashboard.default watch:
	//   • run.started   → snapshot changes (running goes 0→1)        DELTA #1
	//   • node.started  → run-level state unchanged                  no delta
	//   • log           → skipped                                    no delta
	//   • node.completed→ run-level state unchanged                  no delta
	//   • node.started  → unchanged                                  no delta
	//   • node.completed→ unchanged                                  no delta
	//   • run.completed → snapshot changes (running 1→0, success ↑)  DELTA #2
	//
	// The dashboard is intentionally a *run-level* aggregate. Node and log
	// events flow into runs.{id}.nodes.{node} and runs.{id}.logs respectively,
	// where the per-run detail page subscribes to them directly. Pages that
	// only display run-level state don't get re-rendered for every log line.
	events := []BridgeEvent{
		{Type: "run.started", RunID: "r1", PipelineID: "p1", Timestamp: now},
		{Type: "node.started", RunID: "r1", NodeID: "extract", Timestamp: now},
		{Type: "log", RunID: "r1", NodeID: "extract", Level: "info", Message: "fetching data", Timestamp: now},
		{Type: "node.completed", RunID: "r1", NodeID: "extract", RowCount: 1000, DurationMs: 500, Timestamp: now},
		{Type: "node.started", RunID: "r1", NodeID: "transform", Timestamp: now},
		{Type: "node.completed", RunID: "r1", NodeID: "transform", RowCount: 950, DurationMs: 200, Timestamp: now},
		{Type: "run.completed", RunID: "r1", PipelineID: "p1", Timestamp: now},
	}

	for _, ev := range events {
		bridgeCh <- ev
	}

	// Expect exactly 2 DELTAs on the dashboard watch.
	for i := 0; i < 2; i++ {
		f := readFrame(t, conn)
		if f.Type != FrameDelta {
			t.Fatalf("event %d: expected DELTA, got 0x%02x", i, f.Type)
		}
	}

	// Wait for bridge to finish processing
	time.Sleep(50 * time.Millisecond)

	// Verify final run state
	val, _ := sodpSrv.State.Get("runs.r1")
	m := val.(map[string]any)
	if m["status"] != "success" {
		t.Errorf("run status: %v", m["status"])
	}

	// Verify node states
	val, _ = sodpSrv.State.Get("runs.r1.nodes.extract")
	nm := val.(map[string]any)
	if nm["status"] != "completed" || nm["row_count"] != 1000 {
		t.Errorf("extract node: %v", nm)
	}

	val, _ = sodpSrv.State.Get("runs.r1.nodes.transform")
	nm = val.(map[string]any)
	if nm["status"] != "completed" || nm["row_count"] != 950 {
		t.Errorf("transform node: %v", nm)
	}

	// Verify logs
	val, _ = sodpSrv.State.Get("runs.r1.logs")
	logs := val.([]any)
	if len(logs) != 1 {
		t.Errorf("expected 1 log entry, got %d", len(logs))
	}

	// Verify dashboard snapshot reflects the completed run
	val, _ = sodpSrv.State.Get("dashboard.default")
	snap := val.(map[string]any)
	if snap["runs_running"] != 0 {
		t.Errorf("dashboard.runs_running: got %v, want 0", snap["runs_running"])
	}
	if snap["runs_24h_success"] != 1 {
		t.Errorf("dashboard.runs_24h_success: got %v, want 1", snap["runs_24h_success"])
	}
}

func TestIntegration_MultipleClients(t *testing.T) {
	sodpSrv := NewServer()

	mux := http.NewServeMux()
	mux.HandleFunc("/api/ws", sodpSrv.HandleWS)
	ts := httptest.NewServer(mux)
	defer ts.Close()

	// Connect two clients
	c1 := dial(t, ts)
	defer c1.Close()
	readFrame(t, c1) // HELLO

	c2 := dial(t, ts)
	defer c2.Close()
	readFrame(t, c2) // HELLO

	if sodpSrv.SessionCount() != 2 {
		t.Errorf("session count: %d, want 2", sodpSrv.SessionCount())
	}

	// Both watch "_events"
	sendFrame(t, c1, Frame{Type: FrameWatch, StreamID: 10, Body: map[string]any{"state": "_events"}})
	readFrame(t, c1) // STATE_INIT

	sendFrame(t, c2, Frame{Type: FrameWatch, StreamID: 10, Body: map[string]any{"state": "_events"}})
	readFrame(t, c2) // STATE_INIT

	time.Sleep(50 * time.Millisecond)

	// Server mutates — both clients should receive
	sodpSrv.MutateAppend("_events", map[string]any{"type": "test"}, 100)

	d1 := readFrame(t, c1)
	d2 := readFrame(t, c2)

	if d1.Type != FrameDelta || d2.Type != FrameDelta {
		t.Error("both clients should receive DELTA")
	}
}

func TestIntegration_DisconnectCleanup(t *testing.T) {
	sodpSrv := NewServer()

	mux := http.NewServeMux()
	mux.HandleFunc("/api/ws", sodpSrv.HandleWS)
	ts := httptest.NewServer(mux)
	defer ts.Close()

	conn := dial(t, ts)
	readFrame(t, conn) // HELLO

	sendFrame(t, conn, Frame{Type: FrameWatch, StreamID: 10, Body: map[string]any{"state": "_events"}})
	readFrame(t, conn) // STATE_INIT

	if sodpSrv.Fanout.SubscriberCount("_events") != 1 {
		t.Fatal("expected 1 subscriber")
	}

	// Close connection
	conn.Close()
	time.Sleep(100 * time.Millisecond)

	// Subscriber should be cleaned up
	if sodpSrv.Fanout.SubscriberCount("_events") != 0 {
		t.Error("subscriber should be removed after disconnect")
	}
	if sodpSrv.SessionCount() != 0 {
		t.Errorf("session count: %d, want 0", sodpSrv.SessionCount())
	}
}

func TestIntegration_InvalidKeyRejected(t *testing.T) {
	sodpSrv := NewServer()

	mux := http.NewServeMux()
	mux.HandleFunc("/api/ws", sodpSrv.HandleWS)
	ts := httptest.NewServer(mux)
	defer ts.Close()

	conn := dial(t, ts)
	defer conn.Close()

	readFrame(t, conn) // HELLO

	// Watch with empty key — should get ERROR
	sendFrame(t, conn, Frame{
		Type:     FrameWatch,
		StreamID: 10,
		Body:     map[string]any{"state": ""},
	})

	f := readFrame(t, conn)
	if f.Type != FrameError {
		t.Fatalf("expected ERROR (0x07), got 0x%02x", f.Type)
	}
}

func TestIntegration_HeartbeatEcho(t *testing.T) {
	sodpSrv := NewServer()

	mux := http.NewServeMux()
	mux.HandleFunc("/api/ws", sodpSrv.HandleWS)
	ts := httptest.NewServer(mux)
	defer ts.Close()

	conn := dial(t, ts)
	defer conn.Close()

	readFrame(t, conn) // HELLO

	// Send HEARTBEAT
	sendFrame(t, conn, Frame{Type: FrameHeartbeat, Seq: 42})

	// Should echo back
	f := readFrame(t, conn)
	if f.Type != FrameHeartbeat {
		t.Fatalf("expected HEARTBEAT echo, got 0x%02x", f.Type)
	}
	if f.Seq != 42 {
		t.Errorf("seq: got %d, want 42", f.Seq)
	}
}

func TestIntegration_CallMutation(t *testing.T) {
	sodpSrv := NewServer()

	mux := http.NewServeMux()
	mux.HandleFunc("/api/ws", sodpSrv.HandleWS)
	ts := httptest.NewServer(mux)
	defer ts.Close()

	conn := dial(t, ts)
	defer conn.Close()

	readFrame(t, conn) // HELLO

	// CALL state.set
	sendFrame(t, conn, Frame{
		Type:     FrameCall,
		StreamID: 1,
		Seq:      1,
		Body: map[string]any{
			"call_id": "test-1",
			"method":  "state.set",
			"args": map[string]any{
				"state": "custom.key",
				"value": map[string]any{"data": "hello"},
			},
		},
	})

	// Should get RESULT
	f := readFrame(t, conn)
	if f.Type != FrameResult {
		t.Fatalf("expected RESULT (0x06), got 0x%02x", f.Type)
	}

	m := bodyMap(t, f.Body)
	if m["success"] != true {
		t.Errorf("result: %v", m)
	}
	if m["call_id"] != "test-1" {
		t.Errorf("call_id: %v", m["call_id"])
	}

	// Verify state was set
	val, ver := sodpSrv.State.Get("custom.key")
	if ver == 0 {
		t.Fatal("key should exist")
	}
	vm := val.(map[string]any)
	if vm["data"] != "hello" {
		t.Errorf("value: %v", vm)
	}
}

func TestIntegration_WatcherGetsDeltaFromCall(t *testing.T) {
	sodpSrv := NewServer()

	mux := http.NewServeMux()
	mux.HandleFunc("/api/ws", sodpSrv.HandleWS)
	ts := httptest.NewServer(mux)
	defer ts.Close()

	// Client 1: watcher
	c1 := dial(t, ts)
	defer c1.Close()
	readFrame(t, c1)

	sendFrame(t, c1, Frame{
		Type: FrameWatch, StreamID: 10,
		Body: map[string]any{"state": "shared.state"},
	})
	readFrame(t, c1) // STATE_INIT

	// Client 2: mutator
	c2 := dial(t, ts)
	defer c2.Close()
	readFrame(t, c2)

	time.Sleep(50 * time.Millisecond)

	// Client 2 sets state
	sendFrame(t, c2, Frame{
		Type: FrameCall, StreamID: 1, Seq: 1,
		Body: map[string]any{
			"call_id": "c2-1",
			"method":  "state.set",
			"args": map[string]any{
				"state": "shared.state",
				"value": map[string]any{"count": 42},
			},
		},
	})
	readFrame(t, c2) // RESULT

	// Client 1 should receive DELTA
	delta := readFrame(t, c1)
	if delta.Type != FrameDelta {
		t.Fatalf("watcher expected DELTA, got 0x%02x", delta.Type)
	}
}

func TestIntegration_ConnectionLimit(t *testing.T) {
	sodpSrv := NewServer()

	mux := http.NewServeMux()
	mux.HandleFunc("/api/ws", sodpSrv.HandleWS)
	ts := httptest.NewServer(mux)
	defer ts.Close()

	// Force a low limit for testing
	sodpSrv.sessionCount.Store(int64(maxSessions))

	url := "ws" + strings.TrimPrefix(ts.URL, "http") + "/api/ws"
	_, resp, err := websocket.DefaultDialer.Dial(url, nil)
	if err == nil {
		t.Fatal("should reject when at connection limit")
	}
	if resp != nil && resp.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("status: %d, want 503", resp.StatusCode)
	}
}
