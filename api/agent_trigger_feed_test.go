package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Tnsor-Labs/brokoli/models"
)

// The pull feed (ADR-047 section 6): for an agent that cannot be reached,
// a trigger keeps its events, and the agent reads them with its own token.

func (b *dispatchBed) agentToken() string {
	b.t.Helper()
	code, res, raw := b.do(http.MethodPost, "/api/agents/"+b.agentID+"/tokens", b.tokens["eve"], `{"name":"feed","on_behalf_of_owner":true}`)
	if code != http.StatusCreated {
		b.t.Fatalf("agent token: %d %s", code, raw)
	}
	return res["token"].(string)
}

type feedPage struct {
	Events []TriggerEvent `json:"events"`
	Cursor string         `json:"cursor"`
}

func (b *dispatchBed) readFeed(token, after string, limit int) feedPage {
	b.t.Helper()
	q := url.Values{}
	if after != "" {
		q.Set("after", after)
	}
	if limit > 0 {
		q.Set("limit", strconv.Itoa(limit))
	}
	code, _, raw := b.do(http.MethodGet, "/api/agents/me/events?"+q.Encode(), token, "")
	if code != http.StatusOK {
		b.t.Fatalf("read feed: %d %s", code, raw)
	}
	var p feedPage
	if err := json.Unmarshal([]byte(raw), &p); err != nil {
		b.t.Fatal(err)
	}
	return p
}

func TestAFeedTriggerHasNoURLAndNoSecret(t *testing.T) {
	b := newDispatchBed(t)
	b.pipelineWithNodes("orders", models.DefaultWorkspaceID)
	path := "/api/agents/" + b.agentID + "/triggers"

	code, res, raw := b.do(http.MethodPost, path, b.tokens["eve"], `{"delivery":"feed","events":["run.failed"]}`)
	if code != http.StatusCreated {
		t.Fatalf("create a feed trigger: %d %s", code, raw)
	}
	if _, has := res["secret"]; has {
		t.Fatal("a feed trigger was given a signing secret")
	}
	id := res["trigger"].(map[string]any)["id"].(string)
	for body, want := range map[string]string{
		`{"delivery":"feed","events":["run.failed"],"url":"https://93.184.216.34/x"}`: "has no url",
		`{"delivery":"webhook","events":["run.failed"]}`:                              "http or https",
		`{"delivery":"carrier-pigeon","events":["run.failed"]}`:                       `delivery must be`,
	} {
		if code, _, raw := b.do(http.MethodPost, path, b.tokens["eve"], body); code != http.StatusBadRequest || !strings.Contains(raw, want) {
			t.Errorf("%s: %d %s, want 400 with %q", body, code, raw, want)
		}
	}
	if code, _, raw := b.do(http.MethodPost, path+"/"+id+"/secret", b.tokens["eve"], ""); code != http.StatusConflict {
		t.Errorf("rotating a feed trigger's secret: %d %s, want 409", code, raw)
	}
	if code, _, raw := b.do(http.MethodPatch, path+"/"+id, b.tokens["eve"], `{"delivery":"webhook","url":"https://93.184.216.34/x"}`); code != http.StatusBadRequest {
		t.Errorf("changing a trigger's delivery: %d %s, want 400", code, raw)
	}

	// Nothing is signed, so a feed trigger needs no encryption key.
	plain := newAgentBed(t)
	agentID, _ := plain.agent("feed-bot", AgentRole, "eve", "eve", true)
	if code, _, raw := plain.do(http.MethodPost, "/api/agents/"+agentID+"/triggers", plain.tokens["eve"], `{"delivery":"feed","events":["run.failed"]}`); code != http.StatusCreated {
		t.Errorf("a feed trigger on a server without a key: %d %s", code, raw)
	}
}

func TestAnAgentReadsAndAcknowledgesItsFeed(t *testing.T) {
	b := newDispatchBed(t)
	rc := newReceiver(t) // must stay silent: feed deliveries are never posted
	b.pipelineWithNodes("orders", models.DefaultWorkspaceID)
	id, _ := b.trigger(`{"events":["run.failed"],"url":"` + rc.srv.URL + `","cooldown_seconds":0}`)
	// The URL stays in the row, so a feed delivery posted by mistake would
	// reach the receiver and show.
	if _, err := b.users.db.Exec(`UPDATE agent_triggers SET delivery = 'feed' WHERE id = ?`, id); err != nil {
		t.Fatal(err)
	}
	token := b.agentToken()
	first := b.runEvent("orders", models.RunEventTerminal, models.RunStatusFailed, "x")
	second := b.runEvent("orders", models.RunEventTerminal, models.RunStatusFailed, "x")
	b.pass()
	if n := len(rc.requests()); n != 0 {
		t.Fatalf("feed deliveries were posted: %d requests", n)
	}

	page := b.readFeed(token, "", 0)
	if len(page.Events) != 2 || page.Events[0].Run.ID != first || page.Events[1].Run.ID != second || page.Cursor == "" {
		t.Fatalf("first read = %+v", page)
	}
	if again := b.readFeed(token, "", 0); len(again.Events) != 2 {
		t.Fatalf("unacknowledged events did not come back: %d", len(again.Events))
	}
	// Reading one at a time acknowledges exactly what was passed back.
	one := b.readFeed(token, "", 1)
	if len(one.Events) != 1 || one.Events[0].Run.ID != first {
		t.Fatalf("limit 1 = %+v", one)
	}
	next := b.readFeed(token, one.Cursor, 1)
	if len(next.Events) != 1 || next.Events[0].Run.ID != second {
		t.Fatalf("after the first cursor = %+v", next)
	}
	states := map[string]string{}
	for _, d := range b.deliveries(id) {
		states[d.RunID] = d.Status
	}
	if states[first] != DeliveryDelivered || states[second] != DeliveryPending {
		t.Fatalf("after acknowledging the first: %v", states)
	}
	done := b.readFeed(token, next.Cursor, 0)
	if len(done.Events) != 0 || done.Cursor != next.Cursor {
		t.Fatalf("after acknowledging both = %+v", done)
	}
	third := b.runEvent("orders", models.RunEventTerminal, models.RunStatusFailed, "x")
	b.pass()
	if later := b.readFeed(token, done.Cursor, 0); len(later.Events) != 1 || later.Events[0].Run.ID != third {
		t.Fatalf("a later failure = %+v", later)
	}
}

func TestOnlyAnAgentHasAFeed(t *testing.T) {
	b := newDispatchBed(t)
	if code, _, raw := b.do(http.MethodGet, "/api/agents/me/events", b.tokens["eve"], ""); code != http.StatusForbidden || !strings.Contains(raw, "only an agent") {
		t.Errorf("a person reading a feed: %d %s, want 403", code, raw)
	}
	token := b.agentToken()
	if code, _, raw := b.do(http.MethodGet, "/api/agents/me/events?after=not-a-cursor", token, ""); code != http.StatusBadRequest {
		t.Errorf("a forged cursor: %d %s, want 400", code, raw)
	}
	if code, _, _ := b.do(http.MethodGet, "/api/agents/me/events", "", ""); code != http.StatusUnauthorized {
		t.Errorf("no credential: %d, want 401", code)
	}
}

func TestTheFeedChecksAccessWhenItIsRead(t *testing.T) {
	b := newDispatchBed(t)
	b.pipelineWithNodes("orders", models.DefaultWorkspaceID)
	code, res, raw := b.do(http.MethodPost, "/api/agents/"+b.agentID+"/triggers", b.tokens["eve"], `{"delivery":"feed","events":["run.failed"]}`)
	if code != http.StatusCreated {
		t.Fatalf("create: %d %s", code, raw)
	}
	id := res["trigger"].(map[string]any)["id"].(string)
	token := b.agentToken()
	b.runEvent("orders", models.RunEventTerminal, models.RunStatusFailed, "x")
	b.pass()
	if err := b.s.CreateRole(&models.Role{ID: "blind", Name: "blind", Permissions: []models.Permission{models.PermPipelinesView}}); err != nil {
		t.Fatal(err)
	}
	if code, _, raw := b.do(http.MethodPatch, "/api/agents/"+b.agentID, b.tokens["ada"], `{"role":"blind"}`); code != http.StatusOK {
		t.Fatalf("narrow the agent: %d %s", code, raw)
	}
	if page := b.readFeed(token, "", 0); len(page.Events) != 0 {
		t.Fatalf("an agent that may no longer see runs read %d events", len(page.Events))
	}
	if d := b.deliveries(id)[0]; d.Status != DeliverySkipped || !strings.Contains(d.Reason, "runs.view") {
		t.Fatalf("delivery = %+v", d)
	}
}

func TestUnreadFeedEventsExpire(t *testing.T) {
	b := newDispatchBed(t)
	b.pipelineWithNodes("orders", models.DefaultWorkspaceID)
	_, res, _ := b.do(http.MethodPost, "/api/agents/"+b.agentID+"/triggers", b.tokens["eve"], `{"delivery":"feed","events":["run.failed"]}`)
	id := res["trigger"].(map[string]any)["id"].(string)
	token := b.agentToken()
	b.runEvent("orders", models.RunEventTerminal, models.RunStatusFailed, "x")
	b.pass()
	if err := b.d.ts.ExpireFeed(time.Now().Add(-time.Hour)); err != nil { // not old enough yet
		t.Fatal(err)
	}
	if d := b.deliveries(id)[0]; d.Status != DeliveryPending {
		t.Fatalf("expired too early: %+v", d)
	}
	if err := b.d.ts.ExpireFeed(time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if d := b.deliveries(id)[0]; d.Status != DeliveryExpired || !strings.Contains(d.Reason, "7 days") {
		t.Fatalf("delivery = %+v", d)
	}
	if page := b.readFeed(token, "", 0); len(page.Events) != 0 {
		t.Fatalf("an expired event was read: %+v", page)
	}
}

func TestAnAgentReadsItsFeedOverMCP(t *testing.T) {
	b := newDispatchBed(t)
	b.pipelineWithNodes("orders", models.DefaultWorkspaceID)
	if code, _, raw := b.do(http.MethodPost, "/api/agents/"+b.agentID+"/triggers", b.tokens["eve"], `{"delivery":"feed","events":["run.failed"]}`); code != http.StatusCreated {
		t.Fatalf("create: %d %s", code, raw)
	}
	token := b.agentToken()
	runID := b.runEvent("orders", models.RunEventTerminal, models.RunStatusFailed, "x")
	b.pass()

	hs := httptest.NewServer(b.srv.router)
	defer hs.Close()
	cs, err := mcpSession(t, hs.URL, token)
	if err != nil {
		t.Fatal(err)
	}
	defer cs.Close()
	out, text := callTool(t, cs, "next_events", map[string]any{})
	if out == nil {
		t.Fatalf("next_events: %s", text)
	}
	events, _ := out["events"].([]any)
	if len(events) != 1 || events[0].(map[string]any)["run"].(map[string]any)["id"] != runID {
		t.Fatalf("next_events = %s", text)
	}
	cursor, _ := out["cursor"].(string)
	out, text = callTool(t, cs, "next_events", map[string]any{"after": cursor})
	if events, _ := out["events"].([]any); len(events) != 0 {
		t.Fatalf("after acknowledging: %s", text)
	}
}
