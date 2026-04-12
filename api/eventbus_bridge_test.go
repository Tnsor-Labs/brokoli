package api

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/Tnsor-Labs/brokoli/extensions"
	"github.com/Tnsor-Labs/brokoli/models"
	"github.com/Tnsor-Labs/brokoli/pkg/sodp"
)

// TestEventBusBridge_ForwardsToSODP simulates the distributed-mode flow:
// a worker pod publishes models.Event to the EventBus, the API pod
// (this test) subscribes via startEventBusBridge, the event lands in the
// SODP bridge channel, the bridge mutates state, and a watcher receives
// the resulting delta. This is the path that distributed deployments rely
// on for cross-pod event delivery.
func TestEventBusBridge_ForwardsToSODP(t *testing.T) {
	reg := extensions.DefaultRegistry()
	if reg.EventBus == nil {
		t.Fatal("DefaultRegistry should provide an in-memory EventBus")
	}

	srv := sodp.NewServer()
	bridgeCh := make(chan sodp.BridgeEvent, 16)
	sodp.Bridge(srv, bridgeCh)

	startEventBusBridge(reg.EventBus, bridgeCh)

	// Subscribe a fake watcher to the dashboard snapshot the bridge maintains
	// for community-mode runs. The bridge recomputes this key on every
	// run-level event from the bus.
	subCh := make(chan []byte, 16)
	srv.Fanout.Subscribe("dashboard.default", sodp.Subscriber{
		SessionID: "test",
		StreamID:  10,
		OrgID:     "default",
		Send:      subCh,
	})

	// Give the EventBus subscriber goroutine a moment to register.
	time.Sleep(50 * time.Millisecond)

	// Worker pod publishes a real models.Event (JSON-encoded, as the worker does).
	ev := models.Event{
		Type:       models.EventRunStarted,
		RunID:      "dist-run-1",
		PipelineID: "dist-pipe",
		Status:     models.RunStatusRunning,
		Timestamp:  time.Now().UTC(),
	}
	data, err := json.Marshal(ev)
	if err != nil {
		t.Fatalf("marshal event: %v", err)
	}
	if err := reg.EventBus.Publish("events:run", data); err != nil {
		t.Fatalf("publish: %v", err)
	}

	// Give the bus → bridge → SODP path time to propagate.
	time.Sleep(150 * time.Millisecond)

	// Verify the run state was created in SODP from the bus event.
	val, ver := srv.State.Get("runs.dist-run-1")
	if ver == 0 {
		t.Fatal("expected run state to be created from bus event")
	}
	m, ok := val.(map[string]any)
	if !ok {
		t.Fatalf("expected map, got %T", val)
	}
	if m["status"] != "running" {
		t.Errorf("status: got %v, want running", m["status"])
	}
	if m["pipeline_id"] != "dist-pipe" {
		t.Errorf("pipeline_id: got %v, want dist-pipe", m["pipeline_id"])
	}

	// Verify the watcher received a delta on dashboard.default
	select {
	case <-subCh:
		// expected
	default:
		t.Error("watcher should have received a dashboard delta from the bus-originated event")
	}
}

// TestEventBusBridge_OrgScopedChannel verifies that worker events published
// on the per-org channel ("events:org:acme") arrive at the bridge with the
// org_id intact, and end up in the org-scoped dashboard snapshot.
func TestEventBusBridge_OrgScopedChannel(t *testing.T) {
	reg := extensions.DefaultRegistry()
	srv := sodp.NewServer()
	bridgeCh := make(chan sodp.BridgeEvent, 16)
	sodp.Bridge(srv, bridgeCh)
	startEventBusBridge(reg.EventBus, bridgeCh)

	time.Sleep(50 * time.Millisecond)

	ev := models.Event{
		Type:      models.EventRunStarted,
		RunID:     "acme-run-1",
		OrgID:     "acme",
		Timestamp: time.Now().UTC(),
	}
	data, _ := json.Marshal(ev)
	reg.EventBus.Publish("events:org:acme", data)

	time.Sleep(150 * time.Millisecond)

	// Run state should exist with org_id
	val, ver := srv.State.Get("runs.acme-run-1")
	if ver == 0 {
		t.Fatal("expected run state from org-scoped bus event")
	}
	m := val.(map[string]any)
	if m["org_id"] != "acme" {
		t.Errorf("org_id: got %v, want acme", m["org_id"])
	}

	// Dashboard snapshot should land on the org-scoped key
	val, _ = srv.State.Get("dashboard.acme")
	if val == nil {
		t.Error("expected dashboard.acme to be populated for org-scoped event")
	}
	snap := val.(map[string]any)
	if snap["runs_running"] != 1 {
		t.Errorf("dashboard.acme runs_running: got %v, want 1", snap["runs_running"])
	}

	// Cross-tenant: the default dashboard should not have this run
	val, _ = srv.State.Get("dashboard.default")
	if val != nil {
		defaultSnap := val.(map[string]any)
		if defaultSnap["runs_running"] != 0 {
			t.Errorf("dashboard.default should not see acme's run, got runs_running=%v", defaultSnap["runs_running"])
		}
	}
}

// TestEventBusBridge_MalformedEventDoesNotCrash verifies the subscriber
// keeps running when a malformed event arrives on the bus. A bad message
// from one worker should not poison the channel.
func TestEventBusBridge_MalformedEventDoesNotCrash(t *testing.T) {
	reg := extensions.DefaultRegistry()
	srv := sodp.NewServer()
	bridgeCh := make(chan sodp.BridgeEvent, 16)
	sodp.Bridge(srv, bridgeCh)
	startEventBusBridge(reg.EventBus, bridgeCh)

	time.Sleep(50 * time.Millisecond)

	// First: garbage
	reg.EventBus.Publish("events:run", []byte("not valid json"))
	// Then: a real event
	ev := models.Event{
		Type:      models.EventRunStarted,
		RunID:     "after-bad-1",
		Timestamp: time.Now().UTC(),
	}
	data, _ := json.Marshal(ev)
	reg.EventBus.Publish("events:run", data)

	time.Sleep(150 * time.Millisecond)

	// The good event must still have been processed
	if _, ver := srv.State.Get("runs.after-bad-1"); ver == 0 {
		t.Error("subscriber should keep running after a malformed message")
	}
}
