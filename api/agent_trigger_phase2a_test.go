package api

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/Tnsor-Labs/brokoli/models"
)

// A recovery is the success after a failure. The cooldown is per event
// type, so the failure that was delivered does not hide the recovery that
// follows it, while repeated failures are still reported once.
func TestARecoveryIsDeliveredAfterAFailure(t *testing.T) {
	b := newDispatchBed(t)
	rc := newReceiver(t)
	b.pipelineWithNodes("orders", models.DefaultWorkspaceID)
	id, secret := b.trigger(`{"events":["run.failed","run.recovered"],"url":"` + rc.srv.URL + `","cooldown_seconds":1800}`)

	b.runEvent("orders", models.RunEventTerminal, models.RunStatusFailed, "x")
	b.runEvent("orders", models.RunEventTerminal, models.RunStatusFailed, "x") // within the cooldown
	ok := b.runEvent("orders", models.RunEventTerminal, models.RunStatusSuccess, "")
	b.pass()

	events := map[string]int{}
	for _, d := range b.deliveries(id) {
		events[d.Event]++
	}
	if events["run.failed"] != 1 || events["run.recovered"] != 1 {
		t.Fatalf("deliveries by event = %v, want one run.failed and one run.recovered", events)
	}
	var recovered TriggerEvent
	for _, r := range rc.requests() {
		var ev TriggerEvent
		_ = json.Unmarshal(r.body, &ev)
		if ev.Type == "run.recovered" {
			recovered = ev
			if !verifyStandardWebhook(secret, r.header, r.body, time.Now()) {
				t.Error("the recovery's signature does not verify")
			}
		}
	}
	if recovered.Run.ID != ok || recovered.Run.Status != "success" || recovered.Next.SuggestedTool != "list_runs" || len(recovered.Run.FailedNodes) != 0 {
		t.Errorf("recovery event = %+v", recovered)
	}
}

func TestASuccessAfterASuccessIsNotARecovery(t *testing.T) {
	b := newDispatchBed(t)
	rc := newReceiver(t)
	b.pipelineWithNodes("orders", models.DefaultWorkspaceID)
	id, _ := b.trigger(`{"events":["run.recovered"],"url":"` + rc.srv.URL + `","cooldown_seconds":0}`)

	b.runEvent("orders", models.RunEventTerminal, models.RunStatusSuccess, "") // the first run: nothing to recover from
	b.runEvent("orders", models.RunEventTerminal, models.RunStatusSuccess, "")
	b.pass()
	if ds := b.deliveries(id); len(ds) != 0 {
		t.Fatalf("successes with no failure before them: %d deliveries", len(ds))
	}
	b.runEvent("orders", models.RunEventTerminal, models.RunStatusFailed, "x")
	recovery := b.runEvent("orders", models.RunEventTerminal, models.RunStatusSuccess, "")
	b.runEvent("orders", models.RunEventTerminal, models.RunStatusSuccess, "") // healthy again: no second recovery
	b.pass()
	ds := b.deliveries(id)
	if len(ds) != 1 || ds[0].RunID != recovery {
		t.Fatalf("deliveries = %+v, want exactly the success after the failure", ds)
	}
}

func TestATestEventIsDeliveredMarked(t *testing.T) {
	b := newDispatchBed(t)
	rc := newReceiver(t)
	b.pipelineWithNodes("orders", models.DefaultWorkspaceID)
	id, secret := b.trigger(`{"events":["run.failed"],"url":"` + rc.srv.URL + `"}`)

	code, res, raw := b.do(http.MethodPost, "/api/agents/"+b.agentID+"/triggers/"+id+"/test", b.tokens["eve"], "")
	if code != http.StatusAccepted || res["pipeline_id"] != "orders" {
		t.Fatalf("test: %d %s", code, raw)
	}
	b.clock = time.Now().UTC()
	b.d.send(t.Context())
	got := rc.requests()
	if len(got) != 1 || !verifyStandardWebhook(secret, got[0].header, got[0].body, time.Now()) {
		t.Fatalf("received %d test deliveries (want 1, signed)", len(got))
	}
	var ev TriggerEvent
	_ = json.Unmarshal(got[0].body, &ev)
	if !ev.Test || ev.Type != "run.failed" || !strings.HasPrefix(ev.Run.ID, "test-") || ev.Pipeline.ID != "orders" || !strings.Contains(ev.Run.Error, "test event") {
		t.Errorf("test event = %+v", ev)
	}
	// A second test right away is not held back by the cooldown.
	if code, _, raw := b.do(http.MethodPost, "/api/agents/"+b.agentID+"/triggers/"+id+"/test", b.tokens["eve"], ""); code != http.StatusAccepted {
		t.Fatalf("a second test: %d %s", code, raw)
	}
	b.clock = time.Now().UTC() // the endpoint stamps real time
	b.d.send(t.Context())
	if n := len(rc.requests()); n != 2 {
		t.Errorf("two tests delivered %d events", n)
	}
	// A real event still is, and says it is not a test.
	b.runEvent("orders", models.RunEventTerminal, models.RunStatusFailed, "x")
	b.clock = time.Now().UTC()
	b.pass()
	if n := len(rc.requests()); n != 3 {
		t.Fatalf("after a real failure: %d requests, want 3", n)
	}
	var real TriggerEvent
	_ = json.Unmarshal(rc.requests()[2].body, &real)
	if real.Test || strings.Contains(string(rc.requests()[2].body), `"test"`) {
		t.Error("a real event is marked as a test")
	}
}

func TestATestEventNeedsAPipelineTheAgentCanSee(t *testing.T) {
	b := newDispatchBed(t)
	rc := newReceiver(t)
	id, _ := b.trigger(`{"events":["run.failed"],"url":"` + rc.srv.URL + `"}`)
	b.pipelineWithNodes("elsewhere", "ws-other")
	code, _, raw := b.do(http.MethodPost, "/api/agents/"+b.agentID+"/triggers/"+id+"/test", b.tokens["eve"], "")
	if code != http.StatusConflict || !strings.Contains(raw, "no pipeline") {
		t.Fatalf("a test with nothing to be about = %d %s, want 409 saying so", code, raw)
	}
	if code, _, _ := b.do(http.MethodPost, "/api/agents/"+b.agentID+"/triggers/"+id+"/test", b.tokens["vic"], ""); code != http.StatusNotFound {
		t.Errorf("someone else testing the trigger = %d, want 404", code)
	}
}
