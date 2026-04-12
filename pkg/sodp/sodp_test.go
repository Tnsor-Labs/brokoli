package sodp

import (
	"testing"
	"time"
)

// --- Frame encode/decode ---

func TestFrameRoundTrip(t *testing.T) {
	original := Frame{
		Type:     FrameDelta,
		StreamID: 42,
		Seq:      100,
		Body:     map[string]any{"key": "runs.abc", "version": 5},
	}

	data, err := EncodeFrame(original)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}

	decoded, err := DecodeFrame(data)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}

	if decoded.Type != original.Type {
		t.Errorf("type: got %d, want %d", decoded.Type, original.Type)
	}
	if decoded.StreamID != original.StreamID {
		t.Errorf("stream_id: got %d, want %d", decoded.StreamID, original.StreamID)
	}
	if decoded.Seq != original.Seq {
		t.Errorf("seq: got %d, want %d", decoded.Seq, original.Seq)
	}
}

func TestDecodeFrameInvalid(t *testing.T) {
	_, err := DecodeFrame([]byte{0x00})
	if err == nil {
		t.Error("expected error on garbage input")
	}
}

// --- Delta diff ---

func TestDiffAddedKeys(t *testing.T) {
	old := map[string]any{"a": "1"}
	new := map[string]any{"a": "1", "b": "2"}
	ops := Diff(old, new)

	if len(ops) != 1 {
		t.Fatalf("expected 1 op, got %d", len(ops))
	}
	if ops[0].Op != OpAdd || ops[0].Path != "/b" {
		t.Errorf("unexpected op: %+v", ops[0])
	}
}

func TestDiffRemovedKeys(t *testing.T) {
	old := map[string]any{"a": "1", "b": "2"}
	new := map[string]any{"a": "1"}
	ops := Diff(old, new)

	if len(ops) != 1 {
		t.Fatalf("expected 1 op, got %d", len(ops))
	}
	if ops[0].Op != OpRemove || ops[0].Path != "/b" {
		t.Errorf("unexpected op: %+v", ops[0])
	}
}

func TestDiffUpdatedKeys(t *testing.T) {
	old := map[string]any{"status": "running"}
	new := map[string]any{"status": "completed"}
	ops := Diff(old, new)

	if len(ops) != 1 {
		t.Fatalf("expected 1 op, got %d", len(ops))
	}
	if ops[0].Op != OpUpdate || ops[0].Path != "/status" {
		t.Errorf("unexpected op: %+v", ops[0])
	}
}

func TestDiffNestedMaps(t *testing.T) {
	old := map[string]any{"meta": map[string]any{"rows": 10}}
	new := map[string]any{"meta": map[string]any{"rows": 20}}
	ops := Diff(old, new)

	if len(ops) != 1 {
		t.Fatalf("expected 1 op, got %d", len(ops))
	}
	if ops[0].Path != "/meta/rows" {
		t.Errorf("expected nested path, got %s", ops[0].Path)
	}
}

func TestDiffNoChange(t *testing.T) {
	v := map[string]any{"a": "1"}
	ops := Diff(v, v)
	if len(ops) != 0 {
		t.Errorf("expected no ops, got %d", len(ops))
	}
}

func TestDiffNonMapReplacement(t *testing.T) {
	ops := Diff("old", "new")
	if len(ops) != 1 || ops[0].Op != OpUpdate || ops[0].Path != "/" {
		t.Errorf("expected atomic replacement, got %+v", ops)
	}
}

// --- StateStore ---

func TestStateApplyAndGet(t *testing.T) {
	s := NewStateStore()

	delta := s.Apply("runs.abc", map[string]any{"status": "running"})
	if delta == nil {
		t.Fatal("expected delta on first apply")
	}
	if delta.Version != 1 {
		t.Errorf("version: got %d, want 1", delta.Version)
	}

	val, ver := s.Get("runs.abc")
	if ver != 1 {
		t.Errorf("get version: got %d, want 1", ver)
	}
	m, ok := val.(map[string]any)
	if !ok || m["status"] != "running" {
		t.Errorf("unexpected value: %v", val)
	}
}

func TestStateApplyNoChange(t *testing.T) {
	s := NewStateStore()
	s.Apply("k", map[string]any{"a": "1"})
	delta := s.Apply("k", map[string]any{"a": "1"})
	if delta != nil {
		t.Error("expected nil delta on no-op apply")
	}
}

func TestStateDelete(t *testing.T) {
	s := NewStateStore()
	s.Apply("k", "val")

	delta := s.Delete("k")
	if delta == nil {
		t.Fatal("expected delta on delete")
	}

	val, ver := s.Get("k")
	if val != nil || ver != 0 {
		t.Errorf("expected nil after delete, got %v (ver %d)", val, ver)
	}
}

func TestStateSnapshot(t *testing.T) {
	s := NewStateStore()
	s.Apply("runs.a", "1")
	s.Apply("runs.b", "2")
	s.Apply("nodes.x", "3")

	snap := s.Snapshot("runs")
	if len(snap) != 2 {
		t.Errorf("expected 2 entries, got %d", len(snap))
	}
}

func TestStateDeltasSince(t *testing.T) {
	s := NewStateStore()
	s.Apply("k", map[string]any{"a": "1"})
	s.Apply("k", map[string]any{"a": "2"})
	s.Apply("k", map[string]any{"a": "3"})

	deltas := s.DeltasSince("k", 1)
	if len(deltas) != 2 {
		t.Errorf("expected 2 deltas since v1, got %d", len(deltas))
	}
}

func TestStateAppend(t *testing.T) {
	s := NewStateStore()

	// All appends emit ADD at /- (RFC 6901 array append). @sodp/client 0.2.1
	// correctly materializes an array when applying ADD /- to a null value.
	d1 := s.Append("logs", "entry1", 3)
	if d1 == nil {
		t.Fatal("expected delta on first append")
	}
	if len(d1.Ops) != 1 || d1.Ops[0].Op != OpAdd || d1.Ops[0].Path != "/-" {
		t.Errorf("first append should be ADD /-, got %+v", d1.Ops)
	}
	if d1.Ops[0].Value != "entry1" {
		t.Errorf("ADD /- value should be the appended element, got %v", d1.Ops[0].Value)
	}

	d2 := s.Append("logs", "entry2", 3)
	if len(d2.Ops) != 1 || d2.Ops[0].Op != OpAdd || d2.Ops[0].Path != "/-" {
		t.Errorf("second append should be ADD /-, got %+v", d2.Ops)
	}

	s.Append("logs", "entry3", 3)
	d4 := s.Append("logs", "entry4", 3) // triggers trim past cap=3

	// Trim emits ADD /- + REMOVE /0
	if len(d4.Ops) != 2 {
		t.Errorf("trim append should emit 2 ops (ADD + REMOVE), got %+v", d4.Ops)
	}
	if d4.Ops[0].Op != OpAdd || d4.Ops[0].Path != "/-" {
		t.Errorf("trim op[0] should be ADD /-, got %+v", d4.Ops[0])
	}
	if d4.Ops[1].Op != OpRemove || d4.Ops[1].Path != "/0" {
		t.Errorf("trim op[1] should be REMOVE /0, got %+v", d4.Ops[1])
	}

	val, _ := s.Get("logs")
	slice, ok := val.([]any)
	if !ok {
		t.Fatalf("expected []any, got %T", val)
	}
	if len(slice) != 3 {
		t.Errorf("expected 3 entries after trim, got %d", len(slice))
	}
	if slice[0] != "entry2" {
		t.Errorf("expected oldest to be entry2, got %v", slice[0])
	}
}

func TestStateMaxKeys(t *testing.T) {
	s := NewStateStore()
	s.maxKeys = 3

	s.Apply("k1", "v1")
	s.Apply("k2", "v2")
	s.Apply("k3", "v3")
	d := s.Apply("k4", "v4") // should be refused
	if d != nil {
		t.Error("expected nil delta when max keys reached")
	}

	// Updating existing key should still work
	d = s.Apply("k1", "updated")
	if d == nil {
		t.Error("updating existing key should work even at max keys")
	}
}

// --- Ring buffer ---

func TestRingLogWrap(t *testing.T) {
	r := newRingLog(3)
	r.push(DeltaEntry{Version: 1})
	r.push(DeltaEntry{Version: 2})
	r.push(DeltaEntry{Version: 3})
	r.push(DeltaEntry{Version: 4}) // overwrites version 1

	if r.len != 3 {
		t.Errorf("len: got %d, want 3", r.len)
	}
	if r.oldestVersion() != 2 {
		t.Errorf("oldest: got %d, want 2", r.oldestVersion())
	}

	var versions []uint64
	r.scan(func(d DeltaEntry) bool {
		versions = append(versions, d.Version)
		return true
	})
	if len(versions) != 3 || versions[0] != 2 || versions[2] != 4 {
		t.Errorf("scan order: %v", versions)
	}
}

// --- FanoutBus ---

func TestFanoutSubscribeAndBroadcast(t *testing.T) {
	fb := NewFanoutBus()
	ch := make(chan []byte, 16)

	fb.Subscribe("runs.abc", Subscriber{
		SessionID: "s1",
		StreamID:  10,
		OrgID:     "default",
		Send:      ch,
	})

	delta := DeltaEntry{
		Key:     "runs.abc",
		Version: 1,
		Ops:     []DeltaOp{{Op: OpUpdate, Path: "/status", Value: "done"}},
	}

	fb.Broadcast(delta, "other-session", "")

	select {
	case msg := <-ch:
		if len(msg) == 0 {
			t.Error("received empty message")
		}
	default:
		t.Error("expected message on subscriber channel")
	}
}

func TestFanoutSkipsSender(t *testing.T) {
	fb := NewFanoutBus()
	ch := make(chan []byte, 16)

	fb.Subscribe("k", Subscriber{SessionID: "s1", StreamID: 10, OrgID: "default", Send: ch})

	fb.Broadcast(DeltaEntry{Key: "k", Version: 1, Ops: []DeltaOp{{Op: OpAdd, Path: "/x"}}}, "s1", "")

	select {
	case <-ch:
		t.Error("sender should have been skipped")
	default:
	}
}

func TestFanoutTenantIsolation(t *testing.T) {
	fb := NewFanoutBus()
	chOrg1 := make(chan []byte, 16)
	chOrg2 := make(chan []byte, 16)

	fb.Subscribe("runs.abc", Subscriber{SessionID: "s1", StreamID: 10, OrgID: "org1", Send: chOrg1})
	fb.Subscribe("runs.abc", Subscriber{SessionID: "s2", StreamID: 11, OrgID: "org2", Send: chOrg2})

	delta := DeltaEntry{Key: "runs.abc", Version: 1, Ops: []DeltaOp{{Op: OpUpdate, Path: "/status"}}}
	fb.Broadcast(delta, "", "org1") // only org1 should receive

	select {
	case <-chOrg1:
		// expected
	default:
		t.Error("org1 subscriber should have received")
	}

	select {
	case <-chOrg2:
		t.Error("org2 subscriber should NOT have received")
	default:
		// expected
	}
}

func TestFanoutDefaultOrgReceivesAll(t *testing.T) {
	fb := NewFanoutBus()
	ch := make(chan []byte, 16)

	fb.Subscribe("runs.abc", Subscriber{SessionID: "s1", StreamID: 10, OrgID: "default", Send: ch})

	delta := DeltaEntry{Key: "runs.abc", Version: 1, Ops: []DeltaOp{{Op: OpUpdate, Path: "/x"}}}
	fb.Broadcast(delta, "", "org1") // "default" org should still receive

	select {
	case <-ch:
		// expected — default org receives everything
	default:
		t.Error("default org subscriber should receive all events")
	}
}

func TestFanoutRemoveSession(t *testing.T) {
	fb := NewFanoutBus()
	ch := make(chan []byte, 16)

	fb.Subscribe("k1", Subscriber{SessionID: "s1", StreamID: 10, OrgID: "default", Send: ch})
	fb.Subscribe("k2", Subscriber{SessionID: "s1", StreamID: 11, OrgID: "default", Send: ch})

	fb.RemoveSession("s1")

	if fb.SubscriberCount("k1") != 0 || fb.SubscriberCount("k2") != 0 {
		t.Error("session should have been fully removed")
	}
}

// --- Session ---

func TestSessionWatchLifecycle(t *testing.T) {
	sess := NewSession("s1", "org1")

	sid := sess.AllocStream()
	if sid < firstStreamID {
		t.Errorf("stream_id should be >= %d, got %d", firstStreamID, sid)
	}

	ok := sess.AddWatch(sid, "runs.abc")
	if !ok {
		t.Error("AddWatch should succeed")
	}
	watches := sess.Watches()
	if watches[sid] != "runs.abc" {
		t.Error("watch not recorded")
	}

	key, found := sess.RemoveWatch(sid)
	if !found || key != "runs.abc" {
		t.Error("remove watch failed")
	}
}

func TestSessionWatchLimit(t *testing.T) {
	sess := NewSession("s1", "default")
	sess.maxWatches = 2

	ok := sess.AddWatch(10, "k1")
	if !ok {
		t.Fatal("first watch should succeed")
	}
	ok = sess.AddWatch(11, "k2")
	if !ok {
		t.Fatal("second watch should succeed")
	}
	ok = sess.AddWatch(12, "k3")
	if ok {
		t.Error("third watch should be rejected (limit=2)")
	}
}

func TestSessionRateLimit(t *testing.T) {
	sess := NewSession("s1", "default")
	sess.rateLimit = 5

	for i := 0; i < 5; i++ {
		if !sess.CheckRate() {
			t.Fatalf("should allow mutation %d", i+1)
		}
	}
	if sess.CheckRate() {
		t.Error("should reject after rate limit")
	}
}

func TestSessionAuthPreventsOrgEscape(t *testing.T) {
	sess := NewSession("s1", "default")

	// First auth sets org
	sess.SetAuth("user1", map[string]any{"org_id": "org1"})
	if sess.OrgID != "org1" {
		t.Errorf("expected org1, got %s", sess.OrgID)
	}

	// Second auth cannot change org
	sess.SetAuth("user1", map[string]any{"org_id": "org2"})
	if sess.OrgID != "org1" {
		t.Errorf("org should still be org1, got %s", sess.OrgID)
	}
}

func TestSessionReauthBlocked(t *testing.T) {
	sess := NewSession("s1", "default")
	sess.MarkAuthenticated()
	if !sess.IsAuthenticated() {
		t.Error("should be authenticated")
	}
}

// --- Key validation ---

func TestIsValidKey(t *testing.T) {
	tests := []struct {
		key   string
		valid bool
	}{
		{"runs.abc", true},
		{"runs.abc.nodes.n1", true},
		{"k", true},
		{"", false},
		{".leading", false},
		{"trailing.", false},
		{"double..dot", false},
		{"has/slash", false},
		{"has\\backslash", false},
		{"has\x00null", false},
		{string(make([]byte, 257)), false}, // too long
	}
	for _, tt := range tests {
		got := isValidKey(tt.key)
		if got != tt.valid {
			t.Errorf("isValidKey(%q) = %v, want %v", tt.key, got, tt.valid)
		}
	}
}

// --- Server.Mutate (integration) ---

func TestServerMutateWithFanout(t *testing.T) {
	srv := NewServer()

	ch := make(chan []byte, 16)
	srv.Fanout.Subscribe("runs.abc", Subscriber{
		SessionID: "test",
		StreamID:  10,
		OrgID:     "default",
		Send:      ch,
	})

	srv.Mutate("runs.abc", map[string]any{"status": "running"})

	select {
	case msg := <-ch:
		if len(msg) == 0 {
			t.Error("received empty message")
		}
	default:
		t.Error("watcher should have received delta")
	}

	val, ver := srv.State.Get("runs.abc")
	if ver != 1 {
		t.Errorf("version: got %d, want 1", ver)
	}
	m := val.(map[string]any)
	if m["status"] != "running" {
		t.Errorf("value: %v", val)
	}
}

func TestServerMutateAppend(t *testing.T) {
	srv := NewServer()

	ch := make(chan []byte, 16)
	srv.Fanout.Subscribe("runs.r1.logs", Subscriber{
		SessionID: "test",
		StreamID:  10,
		OrgID:     "default",
		Send:      ch,
	})

	srv.MutateAppend("runs.r1.logs", map[string]any{"msg": "hello"}, 100)

	select {
	case msg := <-ch:
		if len(msg) == 0 {
			t.Error("received empty message")
		}
	default:
		t.Error("watcher should have received delta")
	}

	val, _ := srv.State.Get("runs.r1.logs")
	slice, ok := val.([]any)
	if !ok || len(slice) != 1 {
		t.Fatalf("expected 1-element slice, got %v", val)
	}
}

// --- helpers ---

func TestSetIn(t *testing.T) {
	current := map[string]any{
		"meta": map[string]any{"rows": 10, "cols": 3},
	}
	result := setIn(current, "meta.rows", 20)
	m := result.(map[string]any)
	meta := m["meta"].(map[string]any)
	if meta["rows"] != 20 {
		t.Errorf("expected 20, got %v", meta["rows"])
	}
	if meta["cols"] != 3 {
		t.Errorf("cols should be preserved, got %v", meta["cols"])
	}
}

func TestMergeValues(t *testing.T) {
	current := map[string]any{"a": "1", "b": "2"}
	patch := map[string]any{"b": "3", "c": "4"}
	result := mergeValues(current, patch).(map[string]any)
	if result["a"] != "1" || result["b"] != "3" || result["c"] != "4" {
		t.Errorf("unexpected merge result: %v", result)
	}
}

// --- Bridge (event → state) ---

func TestBridgeRunLifecycle(t *testing.T) {
	srv := NewServer()
	ch := make(chan BridgeEvent, 16)
	Bridge(srv, ch)

	now := time.Now()

	ch <- BridgeEvent{Type: "run.started", RunID: "r1", PipelineID: "p1", OrgID: "org1", Timestamp: now}
	ch <- BridgeEvent{Type: "node.started", RunID: "r1", NodeID: "n1", Timestamp: now}
	ch <- BridgeEvent{Type: "node.completed", RunID: "r1", NodeID: "n1", RowCount: 42, DurationMs: 150, Timestamp: now}
	ch <- BridgeEvent{Type: "log", RunID: "r1", NodeID: "n1", Level: "info", Message: "done", Timestamp: now}
	ch <- BridgeEvent{Type: "run.completed", RunID: "r1", PipelineID: "p1", Timestamp: now}

	close(ch)
	time.Sleep(50 * time.Millisecond)

	val, ver := srv.State.Get("runs.r1")
	if ver == 0 {
		t.Fatal("run state not set")
	}
	m := val.(map[string]any)
	if m["status"] != "success" {
		t.Errorf("run status: got %v, want success", m["status"])
	}

	val, ver = srv.State.Get("runs.r1.nodes.n1")
	if ver == 0 {
		t.Fatal("node state not set")
	}
	nm := val.(map[string]any)
	if nm["status"] != "completed" {
		t.Errorf("node status: got %v, want completed", nm["status"])
	}
	if nm["row_count"] != 42 {
		t.Errorf("row_count: got %v, want 42", nm["row_count"])
	}

	val, _ = srv.State.Get("runs.r1.logs")
	logs, ok := val.([]any)
	if !ok || len(logs) != 1 {
		t.Fatalf("expected 1 log entry, got %v", val)
	}
}

func TestBridgeRunFailed(t *testing.T) {
	srv := NewServer()
	ch := make(chan BridgeEvent, 4)
	Bridge(srv, ch)

	now := time.Now()
	ch <- BridgeEvent{Type: "run.started", RunID: "r2", PipelineID: "p1", Timestamp: now}
	ch <- BridgeEvent{Type: "run.failed", RunID: "r2", Error: "boom", Status: "cancelled", Timestamp: now}
	close(ch)
	time.Sleep(50 * time.Millisecond)

	val, _ := srv.State.Get("runs.r2")
	m := val.(map[string]any)
	if m["status"] != "cancelled" {
		t.Errorf("status: got %v, want cancelled", m["status"])
	}
	if m["error"] != "boom" {
		t.Errorf("error: got %v, want boom", m["error"])
	}
}

func TestBridgeLogAppendPerformance(t *testing.T) {
	srv := NewServer()
	ch := make(chan BridgeEvent, 512)
	Bridge(srv, ch)

	now := time.Now()
	// Simulate high-frequency log events
	for i := 0; i < 500; i++ {
		ch <- BridgeEvent{
			Type:      "log",
			RunID:     "r1",
			NodeID:    "n1",
			Level:     "info",
			Message:   "log line",
			Timestamp: now,
		}
	}
	close(ch)
	time.Sleep(100 * time.Millisecond)

	val, _ := srv.State.Get("runs.r1.logs")
	logs, ok := val.([]any)
	if !ok {
		t.Fatalf("expected []any, got %T", val)
	}
	if len(logs) != maxLogEntries {
		t.Errorf("expected %d capped entries, got %d", maxLogEntries, len(logs))
	}
}

// --- Body decoders ---

func TestDecodeWatchBody(t *testing.T) {
	body := map[string]any{"state": "runs.abc", "since_version": int64(5)}
	wb, err := decodeWatchBody(body)
	if err != nil {
		t.Fatal(err)
	}
	if wb.Key != "runs.abc" || wb.SinceVersion != 5 {
		t.Errorf("unexpected: %+v", wb)
	}
}

func TestDecodeWatchBodyMissingKey(t *testing.T) {
	_, err := decodeWatchBody(map[string]any{})
	if err == nil {
		t.Error("expected error on missing state")
	}
}

func TestDecodeCallBody(t *testing.T) {
	body := map[string]any{
		"call_id": "c1",
		"method":  "state.set",
		"args":    map[string]any{"state": "runs.abc", "value": "data"},
	}
	cb, err := decodeCallBody(body)
	if err != nil {
		t.Fatal(err)
	}
	if cb.Method != "state.set" || cb.CallID != "c1" {
		t.Errorf("unexpected: %+v", cb)
	}
	if cb.Args["state"] != "runs.abc" {
		t.Errorf("args.state: %v", cb.Args["state"])
	}
}

func TestDecodeAuthBody(t *testing.T) {
	body := map[string]any{"token": "jwt.token.here"}
	ab, err := decodeAuthBody(body)
	if err != nil {
		t.Fatal(err)
	}
	if ab.Token != "jwt.token.here" {
		t.Errorf("unexpected: %+v", ab)
	}
}

func TestDecodeAuthBodyEmpty(t *testing.T) {
	_, err := decodeAuthBody(map[string]any{})
	if err == nil {
		t.Error("expected error on missing token")
	}
}

// --- State eviction ---

func TestEvictCompleted(t *testing.T) {
	s := NewStateStore()

	old := time.Now().Add(-2 * time.Hour).Format(time.RFC3339)
	recent := time.Now().Add(-1 * time.Minute).Format(time.RFC3339)

	// Old completed run (should be evicted)
	s.Apply("runs.old1", map[string]any{"status": "success", "finished_at": old, "org_id": "org1"})
	s.Apply("runs.old1.nodes.n1", map[string]any{"status": "completed"})
	s.Apply("runs.old1.logs", []any{"log1"})

	// Recent completed run (should survive)
	s.Apply("runs.new1", map[string]any{"status": "success", "finished_at": recent})

	// Running run (should survive)
	s.Apply("runs.active", map[string]any{"status": "running"})

	// Non-run key (should survive eviction since it's not under runs.*)
	s.Apply("dashboard.default", map[string]any{"runs_today": 1})

	evicted := s.EvictCompleted(1 * time.Hour)
	if evicted != 3 { // runs.old1 + runs.old1.nodes.n1 + runs.old1.logs
		t.Errorf("expected 3 evicted, got %d", evicted)
	}

	if _, ver := s.Get("runs.old1"); ver != 0 {
		t.Error("old run should be evicted")
	}
	if _, ver := s.Get("runs.old1.nodes.n1"); ver != 0 {
		t.Error("old run's nodes should be evicted")
	}
	if _, ver := s.Get("runs.new1"); ver == 0 {
		t.Error("recent run should survive")
	}
	if _, ver := s.Get("runs.active"); ver == 0 {
		t.Error("active run should survive")
	}
	if _, ver := s.Get("dashboard.default"); ver == 0 {
		t.Error("dashboard.default should survive (not under runs.*)")
	}
}

// --- Bridge dashboard snapshot ---

// TestBridgeDashboardSnapshot is the new state-driven flow: each run/node
// event recomputes a dashboard.{org} snapshot key the UI watches directly.
// No event log, no client-side dedup, no liveRunStatuses reconciliation —
// just current state.
func TestBridgeDashboardSnapshot(t *testing.T) {
	srv := NewServer()
	ch := make(chan BridgeEvent, 8)
	Bridge(srv, ch)

	now := time.Now().UTC()
	// One run that succeeds, one that fails, one that's still running.
	ch <- BridgeEvent{Type: "run.started", RunID: "ok-1", PipelineID: "p1", Timestamp: now}
	ch <- BridgeEvent{Type: "run.completed", RunID: "ok-1", PipelineID: "p1", Timestamp: now}
	ch <- BridgeEvent{Type: "run.started", RunID: "fail-1", PipelineID: "p2", Timestamp: now}
	ch <- BridgeEvent{Type: "run.failed", RunID: "fail-1", PipelineID: "p2", Error: "boom", Timestamp: now}
	ch <- BridgeEvent{Type: "run.started", RunID: "active-1", PipelineID: "p1", Timestamp: now}
	close(ch)
	time.Sleep(50 * time.Millisecond)

	val, ver := srv.State.Get("dashboard.default")
	if ver == 0 {
		t.Fatal("expected dashboard.default to be populated")
	}
	snap, ok := val.(map[string]any)
	if !ok {
		t.Fatalf("expected map, got %T", val)
	}
	// runs_today should count all 3 (today's date matches)
	if snap["runs_today"] != 3 {
		t.Errorf("runs_today: got %v, want 3", snap["runs_today"])
	}
	if snap["runs_running"] != 1 {
		t.Errorf("runs_running: got %v, want 1", snap["runs_running"])
	}
	if snap["runs_24h_failed"] != 1 {
		t.Errorf("runs_24h_failed: got %v, want 1", snap["runs_24h_failed"])
	}
	if snap["runs_24h_success"] != 1 {
		t.Errorf("runs_24h_success: got %v, want 1", snap["runs_24h_success"])
	}
	// running_run_ids should contain exactly "active-1"
	ids, _ := snap["running_run_ids"].([]string)
	if len(ids) != 1 || ids[0] != "active-1" {
		t.Errorf("running_run_ids: got %v, want [active-1]", ids)
	}
	// recent_runs should be sorted newest-first; with same timestamps the
	// order depends on map iteration, so just check the count
	recent, _ := snap["recent_runs"].([]map[string]any)
	if len(recent) != 3 {
		t.Errorf("recent_runs: got %d entries, want 3", len(recent))
	}
}

func TestBridgeDashboardOrgScoped(t *testing.T) {
	srv := NewServer()
	ch := make(chan BridgeEvent, 4)
	Bridge(srv, ch)

	now := time.Now().UTC()
	ch <- BridgeEvent{Type: "run.started", RunID: "acme-1", OrgID: "acme", Timestamp: now}
	ch <- BridgeEvent{Type: "run.started", RunID: "widgets-1", OrgID: "widgets", Timestamp: now}
	close(ch)
	time.Sleep(50 * time.Millisecond)

	// Each org gets its own dashboard key
	acme, ver := srv.State.Get("dashboard.acme")
	if ver == 0 {
		t.Fatal("dashboard.acme should be populated")
	}
	acmeMap := acme.(map[string]any)
	if acmeMap["runs_running"] != 1 {
		t.Errorf("acme runs_running: got %v, want 1", acmeMap["runs_running"])
	}

	widgets, ver := srv.State.Get("dashboard.widgets")
	if ver == 0 {
		t.Fatal("dashboard.widgets should be populated")
	}
	widgetsMap := widgets.(map[string]any)
	if widgetsMap["runs_running"] != 1 {
		t.Errorf("widgets runs_running: got %v, want 1", widgetsMap["runs_running"])
	}

	// Cross-tenant isolation: each snapshot should only see its own runs
	acmeIDs, _ := acmeMap["running_run_ids"].([]string)
	if len(acmeIDs) != 1 || acmeIDs[0] != "acme-1" {
		t.Errorf("acme running_run_ids: got %v, want [acme-1]", acmeIDs)
	}
	widgetsIDs, _ := widgetsMap["running_run_ids"].([]string)
	if len(widgetsIDs) != 1 || widgetsIDs[0] != "widgets-1" {
		t.Errorf("widgets running_run_ids: got %v, want [widgets-1]", widgetsIDs)
	}
}

// --- Key validation for SODP internal keys ---

func TestValidKeyInternalPrefixes(t *testing.T) {
	if !isValidKey("dashboard.default") {
		t.Error("dashboard.default should be valid")
	}
	if !isValidKey("dashboard.acme") {
		t.Error("dashboard.acme should be valid")
	}
}
