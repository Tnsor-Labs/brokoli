package api

import (
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"
	"github.com/golang-jwt/jwt/v5"

	"github.com/Tnsor-Labs/brokoli/models"
	"github.com/Tnsor-Labs/brokoli/pkg/mcpserver"
)

// The messages API (ADR-048, phase 1).
//
//	POST /api/agents/me/messages                     an agent sends (messages.send)
//	GET  /api/messages                               the caller's messages from agents, newest first
//	GET  /api/messages/conversations/{id}            one conversation, oldest first
//	POST /api/messages/read                          mark one message, or all, read
//	POST /api/messages/conversations/{id}/mute       mute or unmute an agent's conversation
//	GET  /api/agents/{id}/messages/summary           for its owner: what the agent sent this week
//
// Phase 2, replies:
//
//	POST /api/messages/conversations/{id}/messages   a person writes in their conversation
//	POST /api/agents/{id}/conversation               its owner opens (or finds) their conversation with an agent
//	GET  /api/agents/me/messages                     an agent reads what people wrote to it
//	POST /api/agents/me/messages/read                an agent marks people's messages read
//	GET  /api/agents/me/conversations/{id}           an agent reads one of its conversations

// The limits per agent (ADR-048 section 4).
const (
	limitPerPersonHour = 20
	limitAllHour       = 60
	limitUrgentDay     = 3
	limitWorkspaceDay  = 5
	inboxDefault       = 50
	inboxMax           = 200
)

type messagesAPI struct {
	users *UserStore
	pc    *PermissionChecker
	ms    *MessageStore
	now   func() time.Time
}

func registerMessageRoutes(r chi.Router, users *UserStore, pc *PermissionChecker, ms *MessageStore) {
	h := &messagesAPI{users: users, pc: pc, ms: ms, now: func() time.Time { return time.Now().UTC() }}
	r.Post("/api/agents/me/messages", h.send)
	r.Get("/api/messages", h.inbox)
	r.Get("/api/messages/conversations/{id}", h.conversation)
	r.Post("/api/messages/read", h.read)
	r.Post("/api/messages/conversations/{id}/mute", h.mute)
	r.Get("/api/agents/{id}/messages/summary", h.summary)
	r.Post("/api/messages/conversations/{id}/messages", h.reply)
	r.Post("/api/agents/{id}/conversation", h.open)
	r.Get("/api/agents/me/messages", h.agentInbox)
	r.Post("/api/agents/me/messages/read", h.agentRead)
	r.Get("/api/agents/me/conversations/{id}", h.agentConversation)
}

// limitPersonHour is how many messages a person writes in one
// conversation in an hour.
const limitPersonHour = 30

type sendInput struct {
	To    string        `json:"to"`
	Text  string        `json:"text"`
	Level string        `json:"level"`
	Topic string        `json:"topic"`
	Links []MessageLink `json:"links"`
}

// sentTo is one message a send made.
type sentTo struct {
	Person         string `json:"to"`
	ConversationID string `json:"conversation_id"`
	MessageID      string `json:"message_id"`
}

func (h *messagesAPI) send(w http.ResponseWriter, r *http.Request) {
	if !AgentIdentitiesEnabled {
		writeError(w, http.StatusNotFound, "agent identities are not available on this server")
		return
	}
	claims, _ := r.Context().Value("claims").(*jwt.MapClaims)
	if claims == nil {
		writeError(w, http.StatusUnauthorized, "authentication required")
		return
	}
	if !isAgentClaims(claims) {
		writeError(w, http.StatusForbidden, "only an agent sends messages here; a person replies in a conversation")
		return
	}
	agentID, _ := (*claims)["sub"].(string)
	a, err := h.users.GetAgent(agentID)
	if err != nil || a.Disabled {
		writeError(w, http.StatusForbidden, "the agent is disabled")
		return
	}
	if !h.pc.HasPermission(r, models.PermMessagesSend) {
		writeError(w, http.StatusForbidden, "insufficient permissions: "+string(models.PermMessagesSend)+"; the agent's role does not let it message people")
		return
	}
	var in sendInput
	if !decodeBody(w, r, &in) {
		return
	}
	m, why := h.compose(in)
	if why != "" {
		writeError(w, http.StatusBadRequest, why)
		return
	}
	ws := workspaceOf(r)
	if why := h.resolveLinks(a, ws, m.Links); why != "" {
		writeError(w, http.StatusBadRequest, why)
		return
	}

	audience := "person"
	var people []User
	switch strings.TrimSpace(in.To) {
	case "":
		writeError(w, http.StatusBadRequest, `to: "owner", a person's username or id in your workspace, or "workspace"`)
		return
	case "workspace":
		if m.Level != MessageInfo {
			writeError(w, http.StatusBadRequest, "a message to the whole workspace is info only; write to a person for anything that needs attention")
			return
		}
		audience = "workspace"
		people = h.members(r, ws)
	default:
		who := strings.TrimSpace(in.To)
		if who == "owner" {
			who = a.OwnerID
		}
		p := h.member(r, ws, who)
		if p == nil {
			writeError(w, http.StatusBadRequest, fmt.Sprintf("%q is not a person in your workspace", in.To))
			return
		}
		people = []User{*p}
	}

	// Each reader must be able to see what the message links to. A person
	// named is refused; the workspace's readers who cannot are left out.
	readers := people[:0:0]
	for _, p := range people {
		if why := h.mayRead(p.ID, ws, m.Links); why != "" {
			if audience == "person" {
				writeError(w, http.StatusBadRequest, fmt.Sprintf("%s may not see what this message links to: %s", p.Username, why))
				return
			}
			continue
		}
		readers = append(readers, p)
	}
	if audience == "workspace" && len(readers) == 0 {
		writeError(w, http.StatusBadRequest, "nobody in the workspace may see what this message links to")
		return
	}
	if m.Level == MessageUrgent && !h.mayUrge(a, ws, readers[0].ID) {
		writeError(w, http.StatusBadRequest, "an urgent message goes to your owner, or to a person who has written to you")
		return
	}
	now := h.now()
	if why, retry := h.overLimit(a.ID, audience, m.Level, readers, now); why != "" {
		w.Header().Set("Retry-After", strconv.Itoa(int(retry.Seconds())+1))
		writeError(w, http.StatusTooManyRequests, why)
		return
	}

	redacted := mcpserver.RedactText(m.Text)
	wasRedacted := redacted != m.Text
	m.Text = redacted
	sent := make([]sentTo, 0, len(readers))
	for _, p := range readers {
		msg := *m
		c, err := h.ms.AddAgentMessage(ws, a.ID, p.ID, audience, &msg, now)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "could not store the message")
			return
		}
		sent = append(sent, sentTo{Person: p.Username, ConversationID: c.ID, MessageID: msg.ID})
	}
	AuditLog(r, "send_message", "agent", a.ID, nil, map[string]interface{}{
		"to": in.To, "audience": audience, "level": m.Level, "readers": len(sent), "links": m.Links,
	})
	out := map[string]interface{}{"sent": sent, "level": m.Level}
	if wasRedacted {
		out["redacted"] = true
		out["note"] = "Something that looked like a credential was replaced before the message was stored. Never put secrets in a message."
	}
	if skipped := len(people) - len(readers); skipped > 0 {
		out["skipped"] = skipped
	}
	writeJSON(w, http.StatusCreated, out)
}

// compose checks and normalizes what an agent sends, or says why it cannot.
func (h *messagesAPI) compose(in sendInput) (*Message, string) {
	level := strings.TrimSpace(in.Level)
	if level == "" {
		level = MessageInfo
	}
	if level != MessageInfo && level != MessageAttention && level != MessageUrgent {
		return nil, "level: info, attention or urgent"
	}
	text := strings.TrimSpace(in.Text)
	if text == "" {
		return nil, "a message needs text"
	}
	if len(text) > maxMessageText {
		// Cut on a rune boundary, and say so in the text itself.
		cut := maxMessageText
		for cut > 0 && !utf8.RuneStart(text[cut]) {
			cut--
		}
		text = text[:cut] + "\n\n[cut: the message was longer than 8 KB]"
	}
	topic := strings.TrimSpace(in.Topic)
	if utf8.RuneCountInString(topic) > maxTopic || strings.ContainsAny(topic, "\r\n") {
		return nil, fmt.Sprintf("topic: one line, at most %d characters", maxTopic)
	}
	if len(in.Links) > maxMessageLinks {
		return nil, fmt.Sprintf("at most %d links", maxMessageLinks)
	}
	links := make([]MessageLink, 0, len(in.Links))
	for _, l := range in.Links {
		switch l.Kind {
		case LinkPipeline, LinkRun, LinkChangeRequest:
		default:
			return nil, fmt.Sprintf("link kind %q: pipeline, run or change_request", l.Kind)
		}
		if strings.TrimSpace(l.ID) == "" {
			return nil, "every link needs an id"
		}
		// Labels and pipelines are the server's to fill in.
		links = append(links, MessageLink{Kind: l.Kind, ID: strings.TrimSpace(l.ID)})
	}
	return &Message{Level: level, Topic: topic, Text: text, Links: links}, ""
}

// resolveLinks fills in each link's pipeline and label, and checks the
// agent may see each one.
func (h *messagesAPI) resolveLinks(a *Agent, ws string, links []MessageLink) string {
	for i := range links {
		l := &links[i]
		pipelineID := l.ID
		switch l.Kind {
		case LinkRun:
			run, err := h.pc.store.GetRun(l.ID)
			if err != nil {
				return fmt.Sprintf("no run %s", l.ID)
			}
			pipelineID = run.PipelineID
		case LinkChangeRequest:
			if changeRequests == nil {
				return "change requests are not available on this server"
			}
			cr, err := changeRequests.byID(l.ID)
			if err != nil {
				return fmt.Sprintf("no change request %s", l.ID)
			}
			pipelineID, l.Label = cr.PipelineID, cr.Title
		}
		p, err := h.pc.store.GetPipeline(pipelineID)
		if err != nil || p == nil {
			return fmt.Sprintf("no pipeline %s", pipelineID)
		}
		if ok, _ := h.users.agentMaySeePipeline(h.pc, a.ID, ws, p); !ok {
			// The same answer as a link that does not exist: what the
			// agent may not see is not its to learn about.
			return fmt.Sprintf("no %s %s", strings.ReplaceAll(l.Kind, "_", " "), l.ID)
		}
		l.PipelineID = p.ID
		if l.Kind == LinkPipeline {
			l.Label = p.Name
		}
	}
	return ""
}

// mayRead is why person may not see every linked pipeline, or "".
func (h *messagesAPI) mayRead(personID, ws string, links []MessageLink) string {
	seen := map[string]bool{}
	for _, l := range links {
		if seen[l.PipelineID] {
			continue
		}
		seen[l.PipelineID] = true
		p, err := h.pc.store.GetPipeline(l.PipelineID)
		if err != nil || p == nil {
			return "a linked pipeline is gone"
		}
		if ok, why := h.users.personMaySeePipeline(h.pc, personID, ws, p); !ok {
			return why
		}
	}
	return ""
}

// members are the people in the agent's workspace: in its organization,
// and members of the workspace where a distribution keeps memberships.
func (h *messagesAPI) members(r *http.Request, ws string) []User {
	var all []User
	var err error
	if org := GetOrgIDFromRequest(r); org != "" && org != "default" {
		all, err = h.users.ListUsersByOrg(org)
	} else {
		all, err = h.users.ListUsers()
	}
	if err != nil {
		return nil
	}
	// Both lists hold people only: agents are left out by the queries.
	out := make([]User, 0, len(all))
	for _, u := range all {
		if UserWorkspaceResolverFunc != nil && !sameWorkspace(ws, "") {
			in := false
			for _, owned := range UserWorkspaceResolverFunc(u.ID) {
				if owned == ws {
					in = true
					break
				}
			}
			if !in {
				continue
			}
		}
		out = append(out, u)
	}
	return out
}

// member is the person in the workspace with that id or username, or nil.
func (h *messagesAPI) member(r *http.Request, ws, who string) *User {
	for _, u := range h.members(r, ws) {
		if u.ID == who || u.Username == who {
			return &u
		}
	}
	return nil
}

// mayUrge reports whether the agent may send person an urgent message:
// its owner, or someone who has written to it.
func (h *messagesAPI) mayUrge(a *Agent, ws, personID string) bool {
	if personID == a.OwnerID {
		return true
	}
	c, err := h.ms.findConversation(ws, a.ID, personID)
	if err != nil {
		return false
	}
	var n int
	_ = h.ms.db.QueryRow(h.ms.q(`SELECT COUNT(*) FROM agent_messages WHERE conversation_id = ? AND author = ?`), c.ID, MessageFromPerson).Scan(&n)
	return n > 0
}

// overLimit is why this send would go over a limit, and how long until it
// would not, or "".
func (h *messagesAPI) overLimit(agentID, audience, level string, readers []User, now time.Time) (string, time.Duration) {
	hour, day := now.Add(-time.Hour), now.Add(-24*time.Hour)
	if n, _ := h.ms.sendsSince(agentID, hour, ""); n >= limitAllHour {
		return fmt.Sprintf("an agent sends at most %d messages an hour", limitAllHour), h.wait(agentID, hour, time.Hour)
	}
	if audience == "workspace" {
		if n, _ := h.ms.sendsSince(agentID, day, "workspace"); n >= limitWorkspaceDay {
			return fmt.Sprintf("at most %d messages a day to the whole workspace", limitWorkspaceDay), h.wait(agentID, day, 24*time.Hour)
		}
	} else {
		if n, _ := h.ms.SentSince(agentID, hour, readers[0].ID, "", ""); n >= limitPerPersonHour {
			return fmt.Sprintf("at most %d messages an hour to one person", limitPerPersonHour), h.wait(agentID, hour, time.Hour)
		}
	}
	if level == MessageUrgent {
		if n, _ := h.ms.SentSince(agentID, day, "", MessageUrgent, ""); n >= limitUrgentDay {
			return fmt.Sprintf("at most %d urgent messages a day", limitUrgentDay), h.wait(agentID, day, 24*time.Hour)
		}
	}
	return "", 0
}

// wait is how long until the oldest message in a window leaves it.
func (h *messagesAPI) wait(agentID string, since time.Time, window time.Duration) time.Duration {
	var oldest string
	if err := h.ms.db.QueryRow(h.ms.q(`SELECT COALESCE(MIN(created_at), '') FROM agent_messages WHERE agent_id = ? AND created_at >= ?`),
		agentID, fixedTime(since)).Scan(&oldest); err != nil || oldest == "" {
		return window
	}
	if d := parseFixedTime(oldest).Add(window).Sub(h.now()); d > 0 {
		return d
	}
	return time.Second
}

// person is the calling person's id; otherwise an answer is written.
func (h *messagesAPI) person(w http.ResponseWriter, r *http.Request) (string, bool) {
	claims, _ := r.Context().Value("claims").(*jwt.MapClaims)
	if claims == nil {
		writeError(w, http.StatusUnauthorized, "authentication required")
		return "", false
	}
	if isAgentClaims(claims) {
		writeError(w, http.StatusForbidden, "this is a person's inbox; an agent reads its own messages elsewhere")
		return "", false
	}
	id, _ := (*claims)["sub"].(string)
	return id, true
}

// InboxMessage is a message as the inbox shows it.
type InboxMessage struct {
	InboxItem
	AgentName   string `json:"agent_name"`
	AgentAvatar string `json:"agent_avatar"`
	// ShownLevel is the level the inbox gives it: info when muted.
	ShownLevel string `json:"shown_level"`
}

func (h *messagesAPI) inbox(w http.ResponseWriter, r *http.Request) {
	personID, ok := h.person(w, r)
	if !ok {
		return
	}
	limit := inboxDefault
	if v, err := strconv.Atoi(r.URL.Query().Get("limit")); err == nil && v > 0 {
		limit = min(v, inboxMax)
	}
	ws := workspaceOf(r)
	items, err := h.ms.Inbox(personID, ws, r.URL.Query().Get("unread") == "true", limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not read your messages")
		return
	}
	needs, _ := h.ms.NeedsYou(personID, ws)
	agents := map[string]*Agent{}
	out := make([]InboxMessage, 0, len(items))
	for _, it := range items {
		a, seen := agents[it.AgentID]
		if !seen {
			a, _ = h.users.GetAgent(it.AgentID)
			agents[it.AgentID] = a
		}
		m := InboxMessage{InboxItem: it, ShownLevel: it.Level}
		if a != nil {
			m.AgentName, m.AgentAvatar = a.Name, a.Avatar
		}
		if it.Muted {
			m.ShownLevel = MessageInfo
		}
		out = append(out, m)
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"messages": out, "needs_you": needs})
}

// own is the conversation in the path when it is the caller's; otherwise
// an answer is written.
func (h *messagesAPI) own(w http.ResponseWriter, r *http.Request) (*Conversation, bool) {
	personID, ok := h.person(w, r)
	if !ok {
		return nil, false
	}
	c, err := h.ms.Conversation(chi.URLParam(r, "id"))
	if err != nil || c.PersonID != personID || !sameWorkspace(c.WorkspaceID, workspaceOf(r)) {
		writeError(w, http.StatusNotFound, ErrConversationNotFound.Error())
		return nil, false
	}
	return c, true
}

func (h *messagesAPI) conversation(w http.ResponseWriter, r *http.Request) {
	c, ok := h.own(w, r)
	if !ok {
		return
	}
	msgs, err := h.ms.ConversationMessages(c.ID, inboxMax)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not read the conversation")
		return
	}
	agent := map[string]interface{}{"id": c.AgentID}
	if a, err := h.users.GetAgent(c.AgentID); err == nil {
		agent["name"], agent["avatar"], agent["owner_name"], agent["disabled"] = a.Name, a.Avatar, a.OwnerName, a.Disabled
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"conversation": c, "agent": agent, "messages": msgs})
}

func (h *messagesAPI) read(w http.ResponseWriter, r *http.Request) {
	personID, ok := h.person(w, r)
	if !ok {
		return
	}
	var in struct {
		MessageID string `json:"message_id"`
	}
	if !decodeBody(w, r, &in) {
		return
	}
	if err := h.ms.MarkRead(personID, workspaceOf(r), in.MessageID, h.now()); err != nil {
		writeError(w, http.StatusInternalServerError, "could not mark read")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *messagesAPI) mute(w http.ResponseWriter, r *http.Request) {
	c, ok := h.own(w, r)
	if !ok {
		return
	}
	var in struct {
		Muted *bool `json:"muted"`
	}
	if !decodeBody(w, r, &in) || in.Muted == nil {
		if in.Muted == nil {
			writeError(w, http.StatusBadRequest, `{"muted": true} or {"muted": false}`)
		}
		return
	}
	if err := h.ms.SetMuted(c.ID, *in.Muted); err != nil {
		writeError(w, http.StatusInternalServerError, "could not change it")
		return
	}
	c.Muted = *in.Muted
	AuditLog(r, "mute_agent", "agent", c.AgentID, nil, map[string]interface{}{"muted": c.Muted, "conversation_id": c.ID})
	writeJSON(w, http.StatusOK, c)
}

func (h *messagesAPI) summary(w http.ResponseWriter, r *http.Request) {
	a, _, _, ok := (&agentsAPI{users: h.users, pc: h.pc}).owned(w, r)
	if !ok {
		return
	}
	s, err := h.ms.Summary(a.ID, h.now().Add(-7*24*time.Hour))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not count its messages")
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"days": 7, "sent": s.Sent, "muted": s.Muted})
}

// byID is a change request by id alone, for a message's link.
func (cs *ChangeRequestStore) byID(id string) (*ChangeRequest, error) {
	cr, err := scanChange(cs.db.QueryRow(cs.q(`SELECT `+changeColumns+` FROM change_requests WHERE id = ?`), id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrChangeNotFound
	}
	return cr, err
}

// cutText trims and cuts text at the message limit on a rune boundary,
// saying so in the text.
func cutText(text string) string {
	text = strings.TrimSpace(text)
	if len(text) <= maxMessageText {
		return text
	}
	cut := maxMessageText
	for cut > 0 && !utf8.RuneStart(text[cut]) {
		cut--
	}
	return text[:cut] + "\n\n[cut: the message was longer than 8 KB]"
}

// reply is a person writing in their conversation. The agent is told
// through its triggers that subscribe to message.received; with none, it
// reads the message when it checks.
func (h *messagesAPI) reply(w http.ResponseWriter, r *http.Request) {
	c, ok := h.own(w, r)
	if !ok {
		return
	}
	a, err := h.users.GetAgent(c.AgentID)
	if err != nil || a.Disabled {
		writeError(w, http.StatusConflict, "the agent is disabled; nothing reaches it")
		return
	}
	var in struct {
		Text string `json:"text"`
	}
	if !decodeBody(w, r, &in) {
		return
	}
	text := cutText(in.Text)
	if text == "" {
		writeError(w, http.StatusBadRequest, "a message needs text")
		return
	}
	now := h.now()
	if n, _ := h.ms.personSentSince(c.ID, now.Add(-time.Hour)); n >= limitPersonHour {
		w.Header().Set("Retry-After", "600")
		writeError(w, http.StatusTooManyRequests, fmt.Sprintf("at most %d messages an hour in one conversation", limitPersonHour))
		return
	}
	redacted := mcpserver.RedactText(text)
	m := &Message{Level: MessageInfo, Text: redacted, Links: []MessageLink{}}
	if err := h.ms.AddPersonMessage(c, m, now); err != nil {
		writeError(w, http.StatusInternalServerError, "could not store the message")
		return
	}
	from := c.PersonID
	if u, err := h.users.GetUserByID(c.PersonID); err == nil {
		from = u.Username
	}
	told := raiseAgentMessage(a.ID, a.Name, c.WorkspaceID, m, from)
	AuditLog(r, "write_to_agent", "agent", a.ID, nil, map[string]interface{}{"conversation_id": c.ID, "message_id": m.ID})
	out := map[string]interface{}{"message": m, "told": told}
	if told == 0 {
		out["note"] = a.Name + " has no trigger for messages, so it is not told at once; it reads them when it checks."
	}
	if redacted != text {
		out["redacted"] = true
	}
	writeJSON(w, http.StatusCreated, out)
}

// open finds or makes the caller's conversation with an agent they own, or
// that they may manage: how an owner starts talking to their agent.
func (h *messagesAPI) open(w http.ResponseWriter, r *http.Request) {
	a, callerID, _, ok := (&agentsAPI{users: h.users, pc: h.pc}).owned(w, r)
	if !ok {
		return
	}
	if a.Disabled {
		writeError(w, http.StatusConflict, "the agent is disabled")
		return
	}
	c, err := h.ms.conversation(workspaceOf(r), a.ID, callerID, h.now())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not open the conversation")
		return
	}
	writeJSON(w, http.StatusOK, c)
}

// agentID is the calling agent's id; otherwise an answer is written.
func (h *messagesAPI) agentID(w http.ResponseWriter, r *http.Request) (string, bool) {
	if !AgentIdentitiesEnabled {
		writeError(w, http.StatusNotFound, "agent identities are not available on this server")
		return "", false
	}
	claims, _ := r.Context().Value("claims").(*jwt.MapClaims)
	if claims == nil {
		writeError(w, http.StatusUnauthorized, "authentication required")
		return "", false
	}
	if !isAgentClaims(claims) {
		writeError(w, http.StatusForbidden, "only an agent reads its messages here; a person reads the inbox")
		return "", false
	}
	id, _ := (*claims)["sub"].(string)
	return id, true
}

// PersonMessage is a person's message to an agent, as the agent reads it.
type PersonMessage struct {
	AgentMessage
	FromName string `json:"from_name"`
}

func (h *messagesAPI) agentInbox(w http.ResponseWriter, r *http.Request) {
	agentID, ok := h.agentID(w, r)
	if !ok {
		return
	}
	limit := inboxDefault
	if v, err := strconv.Atoi(r.URL.Query().Get("limit")); err == nil && v > 0 {
		limit = min(v, inboxMax)
	}
	unread := r.URL.Query().Get("unread") != "false"
	msgs, err := h.ms.FromPeople(agentID, "", unread, limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not read your messages")
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"messages": h.named(msgs)})
}

// named adds each writer's username.
func (h *messagesAPI) named(msgs []AgentMessage) []PersonMessage {
	names := map[string]string{}
	out := make([]PersonMessage, 0, len(msgs))
	for _, m := range msgs {
		n, seen := names[m.PersonID]
		if !seen {
			n = m.PersonID
			if u, err := h.users.GetUserByID(m.PersonID); err == nil {
				n = u.Username
			}
			names[m.PersonID] = n
		}
		out = append(out, PersonMessage{AgentMessage: m, FromName: n})
	}
	return out
}

func (h *messagesAPI) agentRead(w http.ResponseWriter, r *http.Request) {
	agentID, ok := h.agentID(w, r)
	if !ok {
		return
	}
	var in struct {
		MessageIDs []string `json:"message_ids"`
	}
	if !decodeBody(w, r, &in) {
		return
	}
	if len(in.MessageIDs) > inboxMax {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("at most %d ids", inboxMax))
		return
	}
	if err := h.ms.MarkReadByAgent(agentID, in.MessageIDs, h.now()); err != nil {
		writeError(w, http.StatusInternalServerError, "could not mark read")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *messagesAPI) agentConversation(w http.ResponseWriter, r *http.Request) {
	agentID, ok := h.agentID(w, r)
	if !ok {
		return
	}
	c, err := h.ms.Conversation(chi.URLParam(r, "id"))
	if err != nil || c.AgentID != agentID {
		writeError(w, http.StatusNotFound, ErrConversationNotFound.Error())
		return
	}
	msgs, err := h.ms.ConversationMessages(c.ID, inboxMax)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not read the conversation")
		return
	}
	person := map[string]interface{}{"id": c.PersonID}
	if u, err := h.users.GetUserByID(c.PersonID); err == nil {
		person["username"] = u.Username
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"conversation": map[string]interface{}{
		"id": c.ID, "workspace_id": c.WorkspaceID, "created_at": c.CreatedAt, "last_message_at": c.LastMessageAt,
	}, "person": person, "messages": msgs})
}
