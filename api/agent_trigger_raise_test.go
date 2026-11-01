package api

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/Tnsor-Labs/brokoli/models"
)

// withDeadlineEvent registers an added event type for one test.
func withDeadlineEvent(t *testing.T) {
	t.Helper()
	before := TriggerEventTypes()
	t.Cleanup(func() { triggerEventsMu.Lock(); triggerEventTypes = before; triggerEventsMu.Unlock() })
	RegisterTriggerEvent(TriggerEventType{Name: "pipeline.deadline_missed", Description: "A pipeline did not finish by its deadline."})
}

func TestRaisingNeedsTheDispatcher(t *testing.T) {
	setRaiser(nil)
	if _, err := RaiseTriggerEvent(RaisedEvent{Type: "pipeline.deadline_missed", PipelineID: "x", Key: "k"}); !errors.Is(err, ErrTriggersNotRunning) {
		t.Fatalf("err = %v, want ErrTriggersNotRunning", err)
	}
}

// An added event with no run goes out like a run event: signed, once per
// occurrence, to the triggers that want it in the pipeline's workspace.
func TestARaisedEventIsDeliveredOncePerOccurrence(t *testing.T) {
	withDeadlineEvent(t)
	b := newDispatchBed(t)
	setRaiser(b.d)
	t.Cleanup(func() { setRaiser(nil) })
	rc := newReceiver(t)
	b.pipelineWithNodes("orders", models.DefaultWorkspaceID)
	b.pipelineWithNodes("elsewhere", "ws-other")
	id, secret := b.trigger(`{"events":["pipeline.deadline_missed"],"url":"` + rc.srv.URL + `","cooldown_seconds":0}`)
	other, _ := b.trigger(`{"events":["run.failed"],"url":"` + rc.srv.URL + `"}`)

	raise := func(ev RaisedEvent) int {
		t.Helper()
		n, err := RaiseTriggerEvent(ev)
		if err != nil {
			t.Fatalf("raise %+v: %v", ev, err)
		}
		return n
	}
	ev := RaisedEvent{Type: "pipeline.deadline_missed", PipelineID: "orders", Key: "2026-11-01",
		Summary: "orders_daily had not finished by 06:00; its source is postgres://etl:hunter2@db.internal/orders"}
	if n := raise(ev); n != 1 {
		t.Fatalf("first raise made %d deliveries, want 1", n)
	}
	if n := raise(ev); n != 0 {
		t.Errorf("the same occurrence again made %d deliveries, want 0", n)
	}
	if n := raise(RaisedEvent{Type: "pipeline.deadline_missed", PipelineID: "elsewhere", Key: "other"}); n != 0 {
		t.Errorf("a pipeline in another workspace made %d deliveries", n)
	}
	b.clock = time.Now().UTC()
	b.d.send(t.Context())

	got := rc.requests()
	if len(got) != 1 || !verifyStandardWebhook(secret, got[0].header, got[0].body, time.Now()) {
		t.Fatalf("received %d deliveries (want 1, signed)", len(got))
	}
	var body map[string]any
	_ = json.Unmarshal(got[0].body, &body)
	if body["type"] != "pipeline.deadline_missed" || body["agent"] != "triage-bot" || body["pipeline"].(map[string]any)["id"] != "orders" {
		t.Errorf("event = %s", got[0].body)
	}
	if _, ok := body["run"]; ok {
		t.Errorf("an event with no run carries a run: %s", got[0].body)
	}
	summary, _ := body["summary"].(string)
	if !strings.Contains(summary, "had not finished by 06:00") || strings.Contains(summary, "hunter2") {
		t.Errorf("summary = %q, want it kept and the password redacted", summary)
	}
	if next := body["next"].(map[string]any); next["suggested_tool"] != "get_pipeline" || next["mcp"] != "https://brokoli.example.com/api/mcp" {
		t.Errorf("next = %v", next)
	}
	if ds := b.deliveries(other); len(ds) != 0 {
		t.Errorf("a trigger that does not want the type got %d deliveries", len(ds))
	}
	if ds := b.deliveries(id); len(ds) != 1 || ds[0].Status != DeliveryDelivered {
		t.Errorf("deliveries = %+v", ds)
	}

	// The next occurrence is a new event.
	if n := raise(RaisedEvent{Type: "pipeline.deadline_missed", PipelineID: "orders", Key: "2026-11-02"}); n != 1 {
		t.Errorf("the next day's occurrence made %d deliveries, want 1", n)
	}
	// A key is per pipeline: the trigger watches the whole workspace, and
	// billing missed the same day as orders.
	b.pipelineWithNodes("billing", models.DefaultWorkspaceID)
	if n := raise(RaisedEvent{Type: "pipeline.deadline_missed", PipelineID: "billing", Key: "2026-11-01"}); n != 1 {
		t.Errorf("another pipeline's occurrence with the same key made %d deliveries, want 1", n)
	}
}

// An added event about a run carries the run, and the run page lists it.
func TestARaisedEventAboutARunCarriesIt(t *testing.T) {
	withDeadlineEvent(t)
	b := newDispatchBed(t)
	setRaiser(b.d)
	t.Cleanup(func() { setRaiser(nil) })
	rc := newReceiver(t)
	b.pipelineWithNodes("orders", models.DefaultWorkspaceID)
	b.trigger(`{"events":["pipeline.deadline_missed"],"url":"` + rc.srv.URL + `","cooldown_seconds":0}`)
	rid := b.runEvent("orders", models.RunEventTerminal, models.RunStatusSuccess, "")

	if n, err := RaiseTriggerEvent(RaisedEvent{Type: "pipeline.deadline_missed", PipelineID: "orders", RunID: rid, Summary: "finished late"}); err != nil || n != 1 {
		t.Fatalf("raise: %d %v", n, err)
	}
	b.clock = time.Now().UTC()
	b.d.send(t.Context())
	got := rc.requests()
	if len(got) != 1 {
		t.Fatalf("received %d deliveries", len(got))
	}
	var ev TriggerEvent
	_ = json.Unmarshal(got[0].body, &ev)
	if ev.Run.ID != rid || ev.Run.Status != "success" || ev.Summary != "finished late" || ev.Next.SuggestedTool != "get_pipeline" {
		t.Errorf("event = %+v", ev)
	}
	rec := serve(b.srv, "GET", "/api/runs/"+rid+"/agents", b.tokens["vic"], "")
	var notices []RunNotice
	_ = json.Unmarshal(rec.Body.Bytes(), &notices)
	if len(notices) != 1 || notices[0].Event != "pipeline.deadline_missed" {
		t.Errorf("run page notices = %s", rec.Body.String())
	}
}

func TestWhatCannotBeRaised(t *testing.T) {
	withDeadlineEvent(t)
	b := newDispatchBed(t)
	setRaiser(b.d)
	t.Cleanup(func() { setRaiser(nil) })
	b.pipelineWithNodes("orders", models.DefaultWorkspaceID)
	b.pipelineWithNodes("billing", models.DefaultWorkspaceID)
	billingRun := b.runEvent("billing", models.RunEventTerminal, models.RunStatusSuccess, "")
	for name, ev := range map[string]RaisedEvent{
		"not registered":         {Type: "pipeline.nope", PipelineID: "orders", Key: "k"},
		"a built-in type":        {Type: "run.failed", PipelineID: "orders", Key: "k"},
		"no run and no key":      {Type: "pipeline.deadline_missed", PipelineID: "orders"},
		"an unknown pipeline":    {Type: "pipeline.deadline_missed", PipelineID: "nope", Key: "k"},
		"another pipeline's run": {Type: "pipeline.deadline_missed", PipelineID: "orders", RunID: billingRun},
	} {
		if _, err := RaiseTriggerEvent(ev); err == nil {
			t.Errorf("%s: raised, want an error", name)
		}
	}
}
