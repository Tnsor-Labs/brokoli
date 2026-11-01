package api

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/Tnsor-Labs/brokoli/models"
)

// A person answers an agent; the agent reads it, marks it read, and sees
// the whole thread.
func TestAPersonRepliesAndTheAgentReadsIt(t *testing.T) {
	b := newMessageBed(t)
	b.send(b.token, `{"to":"vic","level":"attention","text":"Shall I propose the new column too?"}`)
	conv := b.inbox("vic").Messages[0].ConversationID

	code, res, raw := b.do(http.MethodPost, "/api/messages/conversations/"+conv+"/messages", b.tokens["vic"], `{"text":"Yes, please.\nAnd keep it nullable."}`)
	if code != http.StatusCreated {
		t.Fatalf("reply: %d %s", code, raw)
	}
	if res["told"] != float64(0) || !strings.Contains(raw, "no trigger for messages") {
		t.Errorf("with no trigger the reply says the agent reads it when it checks: %s", raw)
	}
	// The person's own message is not in their inbox, and does not count.
	if in := b.inbox("vic"); len(in.Messages) != 1 || in.NeedsYou != 1 {
		t.Errorf("vic's inbox after replying: %+v", in)
	}

	code, got, raw := b.do(http.MethodGet, "/api/agents/me/messages", b.token, "")
	msgs, _ := got["messages"].([]any)
	if code != http.StatusOK || len(msgs) != 1 {
		t.Fatalf("agent reads: %d %s", code, raw)
	}
	m := msgs[0].(map[string]any)
	if m["from_name"] != "vic" || m["from"] != MessageFromPerson || m["text"] != "Yes, please.\nAnd keep it nullable." || m["conversation_id"] != conv {
		t.Errorf("message = %v", m)
	}
	if code, _, raw := b.do(http.MethodPost, "/api/agents/me/messages/read", b.token, `{"message_ids":["`+m["id"].(string)+`"]}`); code != http.StatusNoContent {
		t.Fatalf("mark read: %d %s", code, raw)
	}
	if _, got, _ := b.do(http.MethodGet, "/api/agents/me/messages", b.token, ""); len(got["messages"].([]any)) != 0 {
		t.Error("a read message is still unread")
	}
	if _, got, _ := b.do(http.MethodGet, "/api/agents/me/messages?unread=false", b.token, ""); len(got["messages"].([]any)) != 1 {
		t.Error("unread=false does not answer read messages")
	}
	code, thread, raw := b.do(http.MethodGet, "/api/agents/me/conversations/"+conv, b.token, "")
	if code != http.StatusOK || len(thread["messages"].([]any)) != 2 || thread["person"].(map[string]any)["username"] != "vic" {
		t.Fatalf("thread: %d %s", code, raw)
	}
	if strings.Contains(raw, `"muted"`) {
		t.Error("the agent's view of a conversation says whether the person muted it")
	}
	// Having written, vic may be sent an urgent message.
	if code, _, raw := b.send(b.token, `{"to":"vic","level":"urgent","text":"The load stopped."}`); code != http.StatusCreated {
		t.Errorf("urgent to someone who wrote to it: %d %s", code, raw)
	}
}

func TestWhoMayWriteToAnAgent(t *testing.T) {
	b := newMessageBed(t)
	// The owner starts a conversation with their agent.
	code, c, raw := b.do(http.MethodPost, "/api/agents/"+b.agentID+"/conversation", b.tokens["eve"], "")
	if code != http.StatusOK {
		t.Fatalf("owner opens: %d %s", code, raw)
	}
	conv := c["id"].(string)
	if code, again, _ := b.do(http.MethodPost, "/api/agents/"+b.agentID+"/conversation", b.tokens["eve"], ""); code != http.StatusOK || again["id"] != conv {
		t.Error("opening twice gives a second conversation")
	}
	if code, _, raw := b.do(http.MethodPost, "/api/messages/conversations/"+conv+"/messages", b.tokens["eve"], `{"text":"Look at billing today."}`); code != http.StatusCreated {
		t.Fatalf("owner writes: %d %s", code, raw)
	}
	// Someone else may not open a conversation with eve's agent, nor write in hers.
	if code, _, _ := b.do(http.MethodPost, "/api/agents/"+b.agentID+"/conversation", b.tokens["vic"], ""); code != http.StatusNotFound {
		t.Errorf("vic opens a conversation with eve's agent: %d", code)
	}
	if code, _, _ := b.do(http.MethodPost, "/api/messages/conversations/"+conv+"/messages", b.tokens["vic"], `{"text":"x"}`); code != http.StatusNotFound {
		t.Errorf("vic writes in eve's conversation: %d", code)
	}
	if code, _, _ := b.do(http.MethodPost, "/api/messages/conversations/"+conv+"/messages", b.token, `{"text":"x"}`); code != http.StatusForbidden {
		t.Errorf("the agent writes as a person: %d", code)
	}
	if code, _, _ := b.do(http.MethodPost, "/api/messages/conversations/"+conv+"/messages", b.tokens["eve"], `{"text":"  "}`); code != http.StatusBadRequest {
		t.Errorf("an empty message: %d", code)
	}
	// A person's message is redacted too: it may be posted to the agent's URL.
	code, res, raw := b.do(http.MethodPost, "/api/messages/conversations/"+conv+"/messages", b.tokens["eve"], `{"text":"use postgres://etl:hunter2@db/orders"}`)
	if code != http.StatusCreated || res["redacted"] != true || strings.Contains(raw, "hunter2") {
		t.Errorf("redaction: %d %s", code, raw)
	}
	// People can see only their own messages through the agent's routes.
	if code, _, _ := b.do(http.MethodGet, "/api/agents/me/messages", b.tokens["eve"], ""); code != http.StatusForbidden {
		t.Errorf("a person reads the agent's messages: %d", code)
	}
	_, other := b.agent("other-bot", AgentRole, "eve", "eve", true)
	if code, _, _ := b.do(http.MethodGet, "/api/agents/me/conversations/"+conv, other, ""); code != http.StatusNotFound {
		t.Errorf("another agent reads the conversation: %d", code)
	}
	// At most 30 an hour in one conversation (two are written already).
	for i := 0; i < limitPersonHour-2; i++ {
		if code, _, raw := b.do(http.MethodPost, "/api/messages/conversations/"+conv+"/messages", b.tokens["eve"], `{"text":"x"}`); code != http.StatusCreated {
			t.Fatalf("message %d: %d %s", i+3, code, raw)
		}
	}
	if code, _, _ := b.do(http.MethodPost, "/api/messages/conversations/"+conv+"/messages", b.tokens["eve"], `{"text":"x"}`); code != http.StatusTooManyRequests {
		t.Errorf("message 31: %d, want 429", code)
	}
	// Nothing reaches a disabled agent.
	b.do(http.MethodDelete, "/api/agents/"+b.agentID, b.tokens["ada"], "")
	if code, _, _ := b.do(http.MethodPost, "/api/messages/conversations/"+conv+"/messages", b.tokens["eve"], `{"text":"x"}`); code != http.StatusConflict {
		t.Errorf("writing to a disabled agent: %d, want 409", code)
	}
}

// A reply reaches the agent through its triggers that subscribe to
// message.received, whatever pipeline they watch; others are not told.
func TestAReplyIsDeliveredThroughTheAgentsTriggers(t *testing.T) {
	b := newDispatchBed(t)
	setRaiser(b.d)
	t.Cleanup(func() { setRaiser(nil) })
	rc := newReceiver(t)
	b.pipelineWithNodes("orders", models.DefaultWorkspaceID)
	everywhere, secret := b.trigger(`{"events":["run.failed","message.received"],"url":"` + rc.srv.URL + `"}`)
	b.trigger(`{"events":["message.received"],"url":"` + rc.srv.URL + `","pipeline_id":"orders"}`)
	notForMessages, _ := b.trigger(`{"events":["run.failed"],"url":"` + rc.srv.URL + `"}`)
	// Another agent's trigger for messages hears nothing of this one's.
	otherID, _ := b.agent("other-bot", AgentRole, "eve", "eve", true)
	code, ot, raw := b.do(http.MethodPost, "/api/agents/"+otherID+"/triggers", b.tokens["eve"], `{"events":["message.received"],"url":"`+rc.srv.URL+`"}`)
	if code != http.StatusCreated {
		t.Fatalf("other agent's trigger: %d %s", code, raw)
	}
	otherTrigger := ot["trigger"].(map[string]any)["id"].(string)

	_, c, _ := b.do(http.MethodPost, "/api/agents/"+b.agentID+"/conversation", b.tokens["eve"], "")
	conv := c["id"].(string)
	var res map[string]any
	code, res, raw = b.do(http.MethodPost, "/api/messages/conversations/"+conv+"/messages", b.tokens["eve"], `{"text":"Look at billing today."}`)
	if code != http.StatusCreated || res["told"] != float64(2) {
		t.Fatalf("reply: %d %s, want told 2", code, raw)
	}
	b.clock = time.Now().UTC()
	b.d.send(t.Context())
	got := rc.requests()
	if len(got) != 2 {
		t.Fatalf("received %d deliveries, want 2", len(got))
	}
	var ev map[string]any
	for _, r := range got {
		_ = json.Unmarshal(r.body, &ev)
		if ev["type"] == TriggerEventMessageReceived && verifyStandardWebhook(secret, r.header, r.body, time.Now()) {
			break
		}
	}
	msg, _ := ev["message"].(map[string]any)
	if ev["type"] != "message.received" || msg["text"] != "Look at billing today." || msg["from"] != "eve" || msg["conversation_id"] != conv {
		t.Errorf("event = %v", ev)
	}
	if _, ok := ev["pipeline"]; ok {
		t.Errorf("a message event names a pipeline: %v", ev)
	}
	if next := ev["next"].(map[string]any); next["suggested_tool"] != "read_messages" {
		t.Errorf("next = %v", next)
	}
	if ds := b.deliveries(notForMessages); len(ds) != 0 {
		t.Errorf("a trigger without message.received got %d deliveries", len(ds))
	}
	if ds := b.deliveries(otherTrigger); len(ds) != 0 {
		t.Errorf("another agent's trigger got %d deliveries", len(ds))
	}
	// A second message is a second delivery: no cooldown holds it back.
	b.do(http.MethodPost, "/api/messages/conversations/"+conv+"/messages", b.tokens["eve"], `{"text":"And payroll."}`)
	b.d.send(t.Context())
	if ds := b.deliveries(everywhere); len(ds) != 2 {
		t.Errorf("two messages made %d deliveries on one trigger, want 2", len(ds))
	}
}

// A feed trigger keeps the message for the agent; a delivery for an agent
// disabled before it went out is skipped.
func TestAReplyWaitsInTheFeedAndIsSkippedForADisabledAgent(t *testing.T) {
	b := newDispatchBed(t)
	setRaiser(b.d)
	t.Cleanup(func() { setRaiser(nil) })
	code, tok, raw := b.do(http.MethodPost, "/api/agents/"+b.agentID+"/tokens", b.tokens["eve"], `{"name":"feed"}`)
	if code != http.StatusCreated {
		t.Fatalf("token: %d %s", code, raw)
	}
	token := tok["token"].(string)
	if code, _, raw := b.do(http.MethodPost, "/api/agents/"+b.agentID+"/triggers", b.tokens["eve"], `{"delivery":"feed","events":["message.received"]}`); code != http.StatusCreated {
		t.Fatalf("feed trigger: %d %s", code, raw)
	}
	_, c, _ := b.do(http.MethodPost, "/api/agents/"+b.agentID+"/conversation", b.tokens["eve"], "")
	b.do(http.MethodPost, "/api/messages/conversations/"+c["id"].(string)+"/messages", b.tokens["eve"], `{"text":"Ping."}`)
	code, feed, raw := b.do(http.MethodGet, "/api/agents/me/events", token, "")
	events, _ := feed["events"].([]any)
	if code != http.StatusOK || len(events) != 1 || events[0].(map[string]any)["type"] != "message.received" {
		t.Fatalf("feed: %d %s", code, raw)
	}

	// A webhook delivery for an agent disabled before it went out is skipped.
	rc := newReceiver(t)
	hook, _ := b.trigger(`{"events":["message.received"],"url":"` + rc.srv.URL + `"}`)
	b.do(http.MethodPost, "/api/messages/conversations/"+c["id"].(string)+"/messages", b.tokens["eve"], `{"text":"Again."}`)
	if err := b.users.DisableAgent(b.agentID); err != nil {
		t.Fatal(err)
	}
	b.clock = time.Now().UTC()
	b.d.send(t.Context())
	ds := b.deliveries(hook)
	if len(rc.requests()) != 0 || len(ds) != 1 || ds[0].Status != DeliverySkipped || !strings.Contains(ds[0].Reason, "disabled") {
		t.Errorf("to a disabled agent: %d sent, deliveries %+v", len(rc.requests()), ds)
	}
}
