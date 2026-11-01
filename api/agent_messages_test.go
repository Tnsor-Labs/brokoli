package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Tnsor-Labs/brokoli/models"
)

// messageBed is a server with an agent (triage-bot, owned by eve, agent
// role), its token, and a pipeline with a failed run to link to.
type messageBed struct {
	*agentBed
	agentID, token, runID string
}

func newMessageBed(t *testing.T) *messageBed {
	t.Helper()
	b := &messageBed{agentBed: newTriggerBed(t)}
	b.agentID, b.token = b.agent("triage-bot", AgentRole, "eve", "eve", true)
	b.pipeline("orders", models.DefaultWorkspaceID)
	b.runID = "run-" + generateID()[:10]
	if err := b.s.CreateRun(&models.Run{ID: b.runID, PipelineID: "orders", Status: models.RunStatusFailed}); err != nil {
		t.Fatal(err)
	}
	return b
}

func (b *messageBed) send(token, body string) (int, map[string]any, string) {
	b.t.Helper()
	return b.do(http.MethodPost, "/api/agents/me/messages", token, body)
}

type inboxAnswer struct {
	Messages []InboxMessage `json:"messages"`
	NeedsYou int            `json:"needs_you"`
}

func (b *messageBed) inbox(person string) inboxAnswer {
	b.t.Helper()
	rec := serve(b.srv, http.MethodGet, "/api/messages", b.tokens[person], "")
	var out inboxAnswer
	if err := json.Unmarshal(rec.Body.Bytes(), &out); rec.Code != http.StatusOK || err != nil {
		b.t.Fatalf("inbox of %s: %d %s", person, rec.Code, rec.Body.String())
	}
	return out
}

func TestAnAgentMessagesItsOwner(t *testing.T) {
	b := newMessageBed(t)
	code, res, raw := b.send(b.token, `{"to":"owner","level":"attention","topic":"orders_daily keeps failing",
		"text":"It failed four nights running. The fix I proposed is still waiting.",
		"links":[{"kind":"pipeline","id":"orders"},{"kind":"run","id":"`+b.runID+`","label":"click me","pipeline_id":"elsewhere"}]}`)
	if code != http.StatusCreated {
		t.Fatalf("send: %d %s", code, raw)
	}
	sent := res["sent"].([]any)
	if len(sent) != 1 || sent[0].(map[string]any)["to"] != "eve" {
		t.Fatalf("sent = %v, want one message to eve", sent)
	}

	in := b.inbox("eve")
	if len(in.Messages) != 1 || in.NeedsYou != 1 {
		t.Fatalf("eve's inbox: %+v", in)
	}
	m := in.Messages[0]
	if m.AgentName != "triage-bot" || m.AgentAvatar != defaultAgentAvatar(b.agentID) || m.Level != MessageAttention ||
		m.ShownLevel != MessageAttention || m.Topic != "orders_daily keeps failing" || m.From != MessageFromAgent {
		t.Errorf("message = %+v", m)
	}
	// The server labels links and fills in their pipeline; what the agent
	// wrote there is ignored.
	if len(m.Links) != 2 || m.Links[0].Label != "orders" || m.Links[1].PipelineID != "orders" || m.Links[1].Label == "click me" {
		t.Errorf("links = %+v", m.Links)
	}
	if len(b.inbox("vic").Messages) != 0 {
		t.Error("a message to the owner reached someone else")
	}

	// The conversation, then reading it.
	code, conv, raw := b.do(http.MethodGet, "/api/messages/conversations/"+m.ConversationID, b.tokens["eve"], "")
	if code != http.StatusOK || len(conv["messages"].([]any)) != 1 || conv["agent"].(map[string]any)["name"] != "triage-bot" {
		t.Fatalf("conversation: %d %s", code, raw)
	}
	if code, _, raw := b.do(http.MethodPost, "/api/messages/read", b.tokens["eve"], `{"message_id":"`+m.ID+`"}`); code != http.StatusNoContent {
		t.Fatalf("read: %d %s", code, raw)
	}
	if in := b.inbox("eve"); in.NeedsYou != 0 || in.Messages[0].ReadAt == nil {
		t.Errorf("after reading: %+v", in)
	}
	// A second message lands in the same conversation.
	b.send(b.token, `{"to":"owner","text":"It recovered."}`)
	if in := b.inbox("eve"); len(in.Messages) != 2 || in.Messages[0].ConversationID != m.ConversationID || in.NeedsYou != 0 {
		t.Errorf("an info message: %+v", in)
	}
}

func TestWhoAnAgentMayMessage(t *testing.T) {
	b := newMessageBed(t)
	if code, _, raw := b.send(b.token, `{"to":"vic","text":"hello"}`); code != http.StatusCreated {
		t.Errorf("a person in the workspace by name: %d %s", code, raw)
	}
	if code, _, raw := b.send(b.token, `{"to":"`+b.ids["ada"]+`","text":"hello"}`); code != http.StatusCreated {
		t.Errorf("a person in the workspace by id: %d %s", code, raw)
	}
	for name, body := range map[string]string{
		"nobody":                 `{"to":"","text":"x"}`,
		"a stranger":             `{"to":"mallory","text":"x"}`,
		"another agent":          `{"to":"triage-bot","text":"x"}`,
		"no text":                `{"to":"owner","text":"  "}`,
		"an unknown level":       `{"to":"owner","text":"x","level":"loud"}`,
		"a two-line topic":       `{"to":"owner","text":"x","topic":"a\nb"}`,
		"a link kind it lacks":   `{"to":"owner","text":"x","links":[{"kind":"url","id":"https://evil.example"}]}`,
		"a link to nothing":      `{"to":"owner","text":"x","links":[{"kind":"run","id":"run-nope"}]}`,
		"workspace at attention": `{"to":"workspace","text":"x","level":"attention"}`,
	} {
		if code, _, raw := b.send(b.token, body); code != http.StatusBadRequest {
			t.Errorf("%s: %d %s, want 400", name, code, raw)
		}
	}
	// The whole workspace, at info: every person, the agent itself not.
	code, res, raw := b.send(b.token, `{"to":"workspace","text":"Maintenance tonight."}`)
	if code != http.StatusCreated || len(res["sent"].([]any)) != 3 {
		t.Fatalf("workspace: %d %s, want three people", code, raw)
	}
	for _, p := range []string{"ada", "eve", "vic"} {
		found := false
		for _, m := range b.inbox(p).Messages {
			found = found || m.Text == "Maintenance tonight."
		}
		if !found {
			t.Errorf("%s did not get the workspace message", p)
		}
	}
}

func TestOnlyAnAgentThatMaySendSends(t *testing.T) {
	b := newMessageBed(t)
	_, viewerToken := b.agent("read-only", string(RoleViewer), "eve", "eve", true)
	if code, _, raw := b.send(viewerToken, `{"to":"owner","text":"x"}`); code != http.StatusForbidden || !strings.Contains(raw, "messages.send") {
		t.Errorf("a viewer agent: %d %s, want 403 naming messages.send", code, raw)
	}
	if code, _, raw := b.send(b.tokens["eve"], `{"to":"vic","text":"x"}`); code != http.StatusForbidden || !strings.Contains(raw, "only an agent") {
		t.Errorf("a person posting as an agent: %d %s, want 403 saying only an agent sends", code, raw)
	}
	if code, _, _ := b.do(http.MethodGet, "/api/messages", b.token, ""); code != http.StatusForbidden {
		t.Errorf("an agent reading a person's inbox: %d, want 403", code)
	}
	if code, _, _ := b.do(http.MethodDelete, "/api/agents/"+b.agentID, b.tokens["ada"], ""); code != http.StatusOK && code != http.StatusNoContent {
		t.Fatalf("disable: %d", code)
	}
	if code, _, _ := b.send(b.token, `{"to":"owner","text":"x"}`); code == http.StatusCreated {
		t.Error("a disabled agent sent a message")
	}
}

// Disabling an agent revokes its tokens, so its requests stop at sign-in.
// The handler refuses a disabled agent too, should one still arrive with an
// identity (a distribution's own token resolution, say).
func TestADisabledAgentIdentityIsRefused(t *testing.T) {
	b := newMessageBed(t)
	a, _ := b.users.GetAgent(b.agentID)
	owner, _ := b.users.GetUserByID(a.OwnerID)
	if err := b.users.DisableAgent(b.agentID); err != nil {
		t.Fatal(err)
	}
	claims := agentClaims(a.ID, a.Name, a.Role, owner.ID, string(owner.Role))
	r := httptest.NewRequest(http.MethodPost, "/api/agents/me/messages", strings.NewReader(`{"to":"owner","text":"x"}`))
	r = r.WithContext(contextWithClaims(r.Context(), &claims).(context.Context))
	rec := httptest.NewRecorder()
	h := &messagesAPI{users: b.users, pc: NewPermissionChecker(b.s), now: func() time.Time { return time.Now().UTC() }}
	h.send(rec, r)
	if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "disabled") {
		t.Errorf("a disabled agent's identity: %d %s, want 403", rec.Code, rec.Body.String())
	}
}

func TestUrgentGoesToTheOwnerAndIsRationed(t *testing.T) {
	b := newMessageBed(t)
	if code, _, raw := b.send(b.token, `{"to":"vic","level":"urgent","text":"x"}`); code != http.StatusBadRequest {
		t.Errorf("urgent to someone who never wrote to it: %d %s", code, raw)
	}
	for i := 0; i < limitUrgentDay; i++ {
		if code, _, raw := b.send(b.token, `{"to":"owner","level":"urgent","text":"x"}`); code != http.StatusCreated {
			t.Fatalf("urgent %d: %d %s", i+1, code, raw)
		}
	}
	rec := serve(b.srv, http.MethodPost, "/api/agents/me/messages", b.token, `{"to":"owner","level":"urgent","text":"x"}`)
	if rec.Code != http.StatusTooManyRequests || rec.Header().Get("Retry-After") == "" {
		t.Errorf("a fourth urgent today: %d %v %s", rec.Code, rec.Header(), rec.Body.String())
	}
	// Info still goes.
	if code, _, raw := b.send(b.token, `{"to":"owner","text":"x"}`); code != http.StatusCreated {
		t.Errorf("info after the urgent limit: %d %s", code, raw)
	}
}

func TestAnAgentSendsAtMostTwentyAnHourToOnePerson(t *testing.T) {
	b := newMessageBed(t)
	for i := 0; i < limitPerPersonHour; i++ {
		if code, _, raw := b.send(b.token, `{"to":"vic","text":"x"}`); code != http.StatusCreated {
			t.Fatalf("message %d: %d %s", i+1, code, raw)
		}
	}
	if code, _, raw := b.send(b.token, `{"to":"vic","text":"x"}`); code != http.StatusTooManyRequests {
		t.Errorf("message 21 to vic: %d %s, want 429", code, raw)
	}
	if code, _, raw := b.send(b.token, `{"to":"owner","text":"x"}`); code != http.StatusCreated {
		t.Errorf("another person is another count: %d %s", code, raw)
	}
}

func TestAMessageIsRedactedAndCut(t *testing.T) {
	b := newMessageBed(t)
	code, res, raw := b.send(b.token, `{"to":"owner","text":"The source is postgres://etl:hunter2@db.internal/orders"}`)
	if code != http.StatusCreated || res["redacted"] != true {
		t.Fatalf("send: %d %s", code, raw)
	}
	if m := b.inbox("eve").Messages[0]; strings.Contains(m.Text, "hunter2") || !strings.Contains(m.Text, "db.internal") {
		t.Errorf("stored text = %q, want the password replaced and the rest kept", m.Text)
	}
	long := strings.Repeat("ü", maxMessageText) // two bytes a rune: twice the cap
	if code, _, raw := b.send(b.token, `{"to":"owner","text":"`+long+`"}`); code != http.StatusCreated {
		t.Fatalf("long send: %d %.200s", code, raw)
	}
	m := b.inbox("eve").Messages[0]
	if len(m.Text) > maxMessageText+100 || !strings.Contains(m.Text, "[cut: ") || !strings.HasPrefix(m.Text, "üü") {
		t.Errorf("long text: %d bytes, ends %q", len(m.Text), m.Text[len(m.Text)-60:])
	}
}

func TestAMutedAgentArrivesQuietly(t *testing.T) {
	b := newMessageBed(t)
	b.send(b.token, `{"to":"vic","text":"first"}`)
	b.send(b.token, `{"to":"vic","level":"attention","text":"before the mute"}`)
	if in := b.inbox("vic"); in.NeedsYou != 1 {
		t.Fatalf("before muting: needs_you = %d, want 1", in.NeedsYou)
	}
	conv := b.inbox("vic").Messages[0].ConversationID
	if code, _, raw := b.do(http.MethodPost, "/api/messages/conversations/"+conv+"/mute", b.tokens["vic"], `{"muted":true}`); code != http.StatusOK {
		t.Fatalf("mute: %d %s", code, raw)
	}
	// Muting quiets what is already waiting, too.
	if in := b.inbox("vic"); in.NeedsYou != 0 || !in.Messages[0].Muted || in.Messages[0].ShownLevel != MessageInfo {
		t.Errorf("a message from before the mute still counts: %+v", in)
	}
	b.send(b.token, `{"to":"vic","level":"attention","text":"needs you"}`)
	in := b.inbox("vic")
	if in.NeedsYou != 0 || !in.Messages[0].Muted || in.Messages[0].ShownLevel != MessageInfo || in.Messages[0].Level != MessageAttention {
		t.Errorf("muted: %+v", in)
	}
	// The owner sees that its agent is being muted.
	code, sum, raw := b.do(http.MethodGet, "/api/agents/"+b.agentID+"/messages/summary", b.tokens["eve"], "")
	if code != http.StatusOK || sum["sent"] != float64(3) || sum["muted"] != float64(1) {
		t.Errorf("summary: %d %s", code, raw)
	}
	// Unmuted: what waited from before the mute counts again; what arrived
	// while muted stays quiet; the next one counts.
	b.do(http.MethodPost, "/api/messages/conversations/"+conv+"/mute", b.tokens["vic"], `{"muted":false}`)
	if in := b.inbox("vic"); in.NeedsYou != 1 {
		t.Errorf("after unmuting: needs_you = %d, want 1 (the one from before)", in.NeedsYou)
	}
	b.send(b.token, `{"to":"vic","level":"attention","text":"again"}`)
	if in := b.inbox("vic"); in.NeedsYou != 2 {
		t.Errorf("after a new one: needs_you = %d, want 2", in.NeedsYou)
	}
}

func TestAConversationIsItsPersonsAlone(t *testing.T) {
	b := newMessageBed(t)
	b.send(b.token, `{"to":"owner","text":"for eve"}`)
	conv := b.inbox("eve").Messages[0].ConversationID
	for _, path := range []string{"/api/messages/conversations/" + conv} {
		if code, _, _ := b.do(http.MethodGet, path, b.tokens["vic"], ""); code != http.StatusNotFound {
			t.Errorf("vic reads eve's conversation: %d", code)
		}
	}
	if code, _, _ := b.do(http.MethodPost, "/api/messages/conversations/"+conv+"/mute", b.tokens["vic"], `{"muted":true}`); code != http.StatusNotFound {
		t.Errorf("vic mutes eve's conversation: %d", code)
	}
	// Marking read touches only the caller's own messages.
	b.do(http.MethodPost, "/api/messages/read", b.tokens["vic"], `{}`)
	if b.inbox("eve").Messages[0].ReadAt != nil {
		t.Error("vic's read-all marked eve's message read")
	}
	if code, _, _ := b.do(http.MethodGet, "/api/agents/"+b.agentID+"/messages/summary", b.tokens["vic"], ""); code != http.StatusNotFound {
		t.Errorf("vic reads the summary of eve's agent: %d", code)
	}
}

// A person sees a linked pipeline only as their own sign-in would: where
// there are workspaces, one in another workspace is refused.
func TestAPersonMaySeeOnlyTheirWorkspacesPipelines(t *testing.T) {
	b := newMessageBed(t)
	multiTenant(t, map[string][]string{b.ids["vic"]: {wsAlpha}})
	alpha := seedPipeline(t, b.s, "alpha-p", wsAlpha)
	beta := seedPipeline(t, b.s, "beta-p", wsBeta)
	pc := NewPermissionChecker(b.s)
	if ok, why := b.users.personMaySeePipeline(pc, b.ids["vic"], wsAlpha, alpha); !ok {
		t.Errorf("vic, own workspace: %s", why)
	}
	if ok, _ := b.users.personMaySeePipeline(pc, b.ids["vic"], wsBeta, beta); ok {
		t.Error("vic sees a pipeline in a workspace they are not in")
	}
	if ok, _ := b.users.personMaySeePipeline(pc, b.agentID, wsAlpha, alpha); ok {
		t.Error("an agent passed the person check")
	}
}

func TestOldMessagesArePruned(t *testing.T) {
	b := newMessageBed(t)
	ms, err := NewMessageStore(b.users.db)
	if err != nil {
		t.Fatal(err)
	}
	old := time.Now().UTC().Add(-messageRetained - time.Hour)
	m := &Message{Level: MessageInfo, Text: "old", Links: []MessageLink{}}
	if _, err := ms.AddAgentMessage(models.DefaultWorkspaceID, b.agentID, b.ids["vic"], "person", m, old); err != nil {
		t.Fatal(err)
	}
	b.send(b.token, `{"to":"eve","text":"new"}`)
	if err := ms.Prune(time.Now().UTC().Add(-messageRetained)); err != nil {
		t.Fatal(err)
	}
	if len(b.inbox("vic").Messages) != 0 {
		t.Error("a message past retention is still there")
	}
	if len(b.inbox("eve").Messages) != 1 {
		t.Error("a recent message was pruned")
	}
	if _, err := ms.Conversation(m.ConversationID); err == nil {
		t.Error("the emptied conversation is still there")
	}
}
