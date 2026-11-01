package api

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Messages between agents and people (ADR-048). An agent writes to a person
// in that pair's conversation, at a level that says what it asks of the
// reader; the person reads it in the inbox. Phase 1 carries the agent's side
// only: people read, mark read and mute.

// Message levels.
const (
	MessageInfo      = "info"
	MessageAttention = "attention"
	MessageUrgent    = "urgent"
)

// Who wrote a message.
const (
	MessageFromAgent  = "agent"
	MessageFromPerson = "person"
)

// What a message may link to. Links are references the app resolves and
// renders, never URLs an agent writes.
const (
	LinkPipeline      = "pipeline"
	LinkRun           = "run"
	LinkChangeRequest = "change_request"
)

const (
	messageRetained = 180 * 24 * time.Hour
	maxMessageText  = 8 << 10
	maxMessageLinks = 10
	maxTopic        = 120
)

// ErrConversationNotFound is a conversation that does not exist or is not
// the caller's.
var ErrConversationNotFound = errors.New("conversation not found")

// MessageLink is one object a message is about.
type MessageLink struct {
	Kind string `json:"kind"`
	ID   string `json:"id"`
	// PipelineID is the pipeline the object belongs to (for a run or a
	// change request), filled in by the server.
	PipelineID string `json:"pipeline_id,omitempty"`
	// Label is what the app shows for the link: a pipeline's name, a
	// change request's title. Filled in by the server, never by the agent.
	Label string `json:"label,omitempty"`
}

// Message is one message in a conversation.
type Message struct {
	ID             string        `json:"id"`
	ConversationID string        `json:"conversation_id"`
	From           string        `json:"from"`
	Level          string        `json:"level"`
	Topic          string        `json:"topic,omitempty"`
	Text           string        `json:"text"`
	Links          []MessageLink `json:"links"`
	// Muted is a message from an agent its reader had muted when it
	// arrived: it is shown at info level whatever level it was sent at.
	Muted     bool       `json:"muted,omitempty"`
	CreatedAt time.Time  `json:"created_at"`
	ReadAt    *time.Time `json:"read_at,omitempty"`
}

// Conversation is the thread between one agent and one person in one
// workspace.
type Conversation struct {
	ID            string    `json:"id"`
	WorkspaceID   string    `json:"workspace_id"`
	AgentID       string    `json:"agent_id"`
	PersonID      string    `json:"person_id"`
	Muted         bool      `json:"muted"`
	CreatedAt     time.Time `json:"created_at"`
	LastMessageAt time.Time `json:"last_message_at"`
}

// MessageStore keeps conversations and messages next to the agents.
type MessageStore struct {
	db      *sql.DB
	dialect string
}

// NewMessageStore creates the store and its tables.
func NewMessageStore(db *sql.DB) (*MessageStore, error) {
	ms := &MessageStore{db: db, dialect: detectUserStoreDialect(db)}
	for _, stmt := range []string{
		`CREATE TABLE IF NOT EXISTS agent_conversations (
			id TEXT PRIMARY KEY,
			workspace_id TEXT NOT NULL,
			agent_id TEXT NOT NULL,
			person_id TEXT NOT NULL,
			muted INTEGER NOT NULL DEFAULT 0,
			created_at TEXT NOT NULL,
			last_message_at TEXT NOT NULL
		)`,
		// One conversation per agent and person in a workspace.
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_agent_conversations_pair ON agent_conversations (agent_id, person_id, workspace_id)`,
		`CREATE INDEX IF NOT EXISTS idx_agent_conversations_person ON agent_conversations (person_id, workspace_id, last_message_at)`,
		`CREATE TABLE IF NOT EXISTS agent_messages (
			id TEXT PRIMARY KEY,
			conversation_id TEXT NOT NULL,
			agent_id TEXT NOT NULL,
			author TEXT NOT NULL,
			level TEXT NOT NULL,
			audience TEXT NOT NULL DEFAULT 'person',
			topic TEXT NOT NULL DEFAULT '',
			body TEXT NOT NULL,
			links TEXT NOT NULL DEFAULT '[]',
			muted INTEGER NOT NULL DEFAULT 0,
			created_at TEXT NOT NULL,
			read_at TEXT NOT NULL DEFAULT ''
		)`,
		`CREATE INDEX IF NOT EXISTS idx_agent_messages_conversation ON agent_messages (conversation_id, created_at)`,
		// What the rate limits count: an agent's messages by time.
		`CREATE INDEX IF NOT EXISTS idx_agent_messages_agent ON agent_messages (agent_id, created_at)`,
	} {
		if _, err := db.Exec(stmt); err != nil {
			return nil, fmt.Errorf("create message tables: %w", err)
		}
	}
	return ms, nil
}

func (ms *MessageStore) q(query string) string { return dialectQuery(ms.dialect, query) }

// conversation is the pair's conversation, made when it does not exist.
func (ms *MessageStore) conversation(workspaceID, agentID, personID string, now time.Time) (*Conversation, error) {
	if c, err := ms.findConversation(workspaceID, agentID, personID); err == nil {
		return c, nil
	} else if !errors.Is(err, ErrConversationNotFound) {
		return nil, err
	}
	id := generateID()
	_, err := ms.db.Exec(ms.q(`INSERT INTO agent_conversations (id, workspace_id, agent_id, person_id, created_at, last_message_at)
		VALUES (?, ?, ?, ?, ?, ?) ON CONFLICT (agent_id, person_id, workspace_id) DO NOTHING`),
		id, workspaceID, agentID, personID, fixedTime(now), fixedTime(now))
	if err != nil {
		return nil, err
	}
	// Another request may have made it first; either way this reads the one.
	return ms.findConversation(workspaceID, agentID, personID)
}

const conversationColumns = `id, workspace_id, agent_id, person_id, muted, created_at, last_message_at`

func scanConversation(row interface{ Scan(...any) error }) (*Conversation, error) {
	var c Conversation
	var muted int
	var created, last string
	if err := row.Scan(&c.ID, &c.WorkspaceID, &c.AgentID, &c.PersonID, &muted, &created, &last); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrConversationNotFound
		}
		return nil, err
	}
	c.Muted = muted != 0
	c.CreatedAt, c.LastMessageAt = parseFixedTime(created), parseFixedTime(last)
	return &c, nil
}

func (ms *MessageStore) findConversation(workspaceID, agentID, personID string) (*Conversation, error) {
	return scanConversation(ms.db.QueryRow(ms.q(`SELECT `+conversationColumns+` FROM agent_conversations
		WHERE workspace_id = ? AND agent_id = ? AND person_id = ?`), workspaceID, agentID, personID))
}

// Conversation is one conversation by id.
func (ms *MessageStore) Conversation(id string) (*Conversation, error) {
	return scanConversation(ms.db.QueryRow(ms.q(`SELECT `+conversationColumns+` FROM agent_conversations WHERE id = ?`), id))
}

// AddAgentMessage stores an agent's message to a person, in their
// conversation, made if needed. A conversation the person muted takes the
// message quietly.
func (ms *MessageStore) AddAgentMessage(workspaceID, agentID, personID, audience string, m *Message, now time.Time) (*Conversation, error) {
	c, err := ms.conversation(workspaceID, agentID, personID, now)
	if err != nil {
		return nil, err
	}
	links, err := json.Marshal(m.Links)
	if err != nil {
		return nil, err
	}
	m.ID, m.ConversationID, m.From, m.CreatedAt, m.Muted = newMessageID(now), c.ID, MessageFromAgent, now, c.Muted
	if _, err := ms.db.Exec(ms.q(`INSERT INTO agent_messages (id, conversation_id, agent_id, author, level, audience, topic, body, links, muted, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`),
		m.ID, c.ID, agentID, MessageFromAgent, m.Level, audience, m.Topic, m.Text, string(links), boolInt(c.Muted), fixedTime(now)); err != nil {
		return nil, err
	}
	if _, err := ms.db.Exec(ms.q(`UPDATE agent_conversations SET last_message_at = ? WHERE id = ?`), fixedTime(now), c.ID); err != nil {
		return nil, err
	}
	return c, nil
}

// AddPersonMessage stores a person's message in their conversation with an
// agent (ADR-048 phase 2). Its read_at is when the agent read it.
func (ms *MessageStore) AddPersonMessage(c *Conversation, m *Message, now time.Time) error {
	links, err := json.Marshal(m.Links)
	if err != nil {
		return err
	}
	m.ID, m.ConversationID, m.From, m.CreatedAt = newMessageID(now), c.ID, MessageFromPerson, now
	if m.Level == "" {
		m.Level = MessageInfo
	}
	if _, err := ms.db.Exec(ms.q(`INSERT INTO agent_messages (id, conversation_id, agent_id, author, level, audience, topic, body, links, muted, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, 0, ?)`),
		m.ID, c.ID, c.AgentID, MessageFromPerson, m.Level, "person", m.Topic, m.Text, string(links), fixedTime(now)); err != nil {
		return err
	}
	_, err = ms.db.Exec(ms.q(`UPDATE agent_conversations SET last_message_at = ? WHERE id = ?`), fixedTime(now), c.ID)
	return err
}

// AgentMessage is a person's message as its agent reads it.
type AgentMessage struct {
	Message
	PersonID string `json:"person_id"`
}

// FromPeople is what people wrote to an agent, oldest first: unread only
// when unreadOnly, in one conversation when conversationID is set.
func (ms *MessageStore) FromPeople(agentID, conversationID string, unreadOnly bool, limit int) ([]AgentMessage, error) {
	query := `SELECT m.` + strings.ReplaceAll(messageColumns, ", ", ", m.") + `, c.person_id FROM agent_messages m
		JOIN agent_conversations c ON c.id = m.conversation_id
		WHERE c.agent_id = ? AND m.author = ?`
	args := []any{agentID, MessageFromPerson}
	if conversationID != "" {
		query += ` AND c.id = ?`
		args = append(args, conversationID)
	}
	if unreadOnly {
		query += ` AND m.read_at = ''`
	}
	rows, err := ms.db.Query(ms.q(query+` ORDER BY m.created_at, m.id LIMIT ?`), append(args, limit)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []AgentMessage{}
	for rows.Next() {
		var am AgentMessage
		var links, created, read string
		var muted int
		if err := rows.Scan(&am.ID, &am.ConversationID, &am.From, &am.Level, &am.Topic, &am.Text, &links, &muted, &created, &read, &am.PersonID); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(links), &am.Links); err != nil || am.Links == nil {
			am.Links = []MessageLink{}
		}
		am.CreatedAt = parseFixedTime(created)
		if read != "" {
			t := parseFixedTime(read)
			am.ReadAt = &t
		}
		out = append(out, am)
	}
	return out, rows.Err()
}

// MarkReadByAgent marks people's messages to an agent read: the ones named,
// or all of them when ids is empty.
func (ms *MessageStore) MarkReadByAgent(agentID string, ids []string, now time.Time) error {
	query := `UPDATE agent_messages SET read_at = ? WHERE read_at = '' AND author = ? AND agent_id = ?`
	args := []any{fixedTime(now), MessageFromPerson, agentID}
	if len(ids) > 0 {
		query += ` AND id IN (?` + strings.Repeat(", ?", len(ids)-1) + `)`
		for _, id := range ids {
			args = append(args, id)
		}
	}
	_, err := ms.db.Exec(ms.q(query), args...)
	return err
}

// personSentSince is how many messages a person wrote in a conversation since a time.
func (ms *MessageStore) personSentSince(conversationID string, since time.Time) (int, error) {
	var n int
	err := ms.db.QueryRow(ms.q(`SELECT COUNT(*) FROM agent_messages WHERE conversation_id = ? AND author = ? AND created_at >= ?`),
		conversationID, MessageFromPerson, fixedTime(since)).Scan(&n)
	return n, err
}

// newMessageID sorts in the order messages are written, so a conversation
// reads in order by id when two share a timestamp.
func newMessageID(now time.Time) string {
	return fmt.Sprintf("msg_%016x%s", now.UnixNano(), generateID()[:12])
}

const messageColumns = `id, conversation_id, author, level, topic, body, links, muted, created_at, read_at`

func scanMessage(row interface{ Scan(...any) error }) (*Message, error) {
	var m Message
	var links, created, read string
	var muted int
	if err := row.Scan(&m.ID, &m.ConversationID, &m.From, &m.Level, &m.Topic, &m.Text, &links, &muted, &created, &read); err != nil {
		return nil, err
	}
	if err := json.Unmarshal([]byte(links), &m.Links); err != nil || m.Links == nil {
		m.Links = []MessageLink{}
	}
	m.Muted = muted != 0
	m.CreatedAt = parseFixedTime(created)
	if read != "" {
		t := parseFixedTime(read)
		m.ReadAt = &t
	}
	return &m, nil
}

func (ms *MessageStore) messages(query string, args ...any) ([]Message, error) {
	rows, err := ms.db.Query(ms.q(query), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Message{}
	for rows.Next() {
		m, err := scanMessage(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *m)
	}
	return out, rows.Err()
}

// ConversationMessages is a conversation's messages, oldest first.
func (ms *MessageStore) ConversationMessages(conversationID string, limit int) ([]Message, error) {
	// The newest limit messages, then put back in order.
	out, err := ms.messages(`SELECT `+messageColumns+` FROM agent_messages WHERE conversation_id = ?
		ORDER BY created_at DESC, id DESC LIMIT ?`, conversationID, limit)
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out, err
}

// InboxItem is a message to a person, with the conversation it is in.
type InboxItem struct {
	Message
	AgentID string `json:"agent_id"`
}

// Inbox is the newest messages from agents to a person in a workspace.
func (ms *MessageStore) Inbox(personID, workspaceID string, unreadOnly bool, limit int) ([]InboxItem, error) {
	query := `SELECT m.` + strings.ReplaceAll(messageColumns, ", ", ", m.") + `, c.agent_id, c.muted FROM agent_messages m
		JOIN agent_conversations c ON c.id = m.conversation_id
		WHERE c.person_id = ? AND c.workspace_id = ? AND m.author = ?`
	args := []any{personID, workspaceID, MessageFromAgent}
	if unreadOnly {
		query += ` AND m.read_at = ''`
	}
	rows, err := ms.db.Query(ms.q(query+` ORDER BY m.created_at DESC, m.id DESC LIMIT ?`), append(args, limit)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []InboxItem{}
	for rows.Next() {
		var it InboxItem
		var links, created, read string
		var muted, conversationMuted int
		if err := rows.Scan(&it.ID, &it.ConversationID, &it.From, &it.Level, &it.Topic, &it.Text, &links, &muted, &created, &read, &it.AgentID, &conversationMuted); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(links), &it.Links); err != nil || it.Links == nil {
			it.Links = []MessageLink{}
		}
		// Quiet when it arrived muted, or while its conversation is muted.
		it.Muted, it.CreatedAt = muted != 0 || conversationMuted != 0, parseFixedTime(created)
		if read != "" {
			t := parseFixedTime(read)
			it.ReadAt = &t
		}
		out = append(out, it)
	}
	return out, rows.Err()
}

// NeedsYou is how many unread messages to a person ask something of them:
// attention or urgent, not muted when they arrived, and not in a
// conversation muted now. It is the bell's count.
func (ms *MessageStore) NeedsYou(personID, workspaceID string) (int, error) {
	var n int
	err := ms.db.QueryRow(ms.q(`SELECT COUNT(*) FROM agent_messages m JOIN agent_conversations c ON c.id = m.conversation_id
		WHERE c.person_id = ? AND c.workspace_id = ? AND m.author = ? AND m.read_at = '' AND m.muted = 0 AND c.muted = 0
		AND m.level IN (?, ?)`),
		personID, workspaceID, MessageFromAgent, MessageAttention, MessageUrgent).Scan(&n)
	return n, err
}

// MarkRead marks a person's messages read: one message, or every message
// in their conversations in the workspace when messageID is empty.
func (ms *MessageStore) MarkRead(personID, workspaceID, messageID string, now time.Time) error {
	query := `UPDATE agent_messages SET read_at = ? WHERE read_at = '' AND author = ? AND conversation_id IN
		(SELECT id FROM agent_conversations WHERE person_id = ? AND workspace_id = ?)`
	args := []any{fixedTime(now), MessageFromAgent, personID, workspaceID}
	if messageID != "" {
		query += ` AND id = ?`
		args = append(args, messageID)
	}
	_, err := ms.db.Exec(ms.q(query), args...)
	return err
}

// SetMuted mutes or unmutes a conversation for its person.
func (ms *MessageStore) SetMuted(conversationID string, muted bool) error {
	res, err := ms.db.Exec(ms.q(`UPDATE agent_conversations SET muted = ? WHERE id = ?`), boolInt(muted), conversationID)
	return oneRow(res, err, ErrConversationNotFound)
}

// SentSince is how many messages an agent sent since a time: in all, to
// one person when personID is set, and at a level or to an audience when
// those are set.
func (ms *MessageStore) SentSince(agentID string, since time.Time, personID, level, audience string) (int, error) {
	query := `SELECT COUNT(*) FROM agent_messages m JOIN agent_conversations c ON c.id = m.conversation_id
		WHERE m.agent_id = ? AND m.author = ? AND m.created_at >= ?`
	args := []any{agentID, MessageFromAgent, fixedTime(since)}
	if personID != "" {
		query += ` AND c.person_id = ?`
		args = append(args, personID)
	}
	if level != "" {
		query += ` AND m.level = ?`
		args = append(args, level)
	}
	if audience != "" {
		query += ` AND m.audience = ?`
		args = append(args, audience)
	}
	var n int
	err := ms.db.QueryRow(ms.q(query), args...).Scan(&n)
	return n, err
}

// sendsSince counts an agent's sends since a time: a message to the whole
// workspace is one send however many people it reaches (its copies share a
// timestamp). audience narrows it when set.
func (ms *MessageStore) sendsSince(agentID string, since time.Time, audience string) (int, error) {
	query := `SELECT COUNT(DISTINCT created_at) FROM agent_messages WHERE agent_id = ? AND author = ? AND created_at >= ?`
	args := []any{agentID, MessageFromAgent, fixedTime(since)}
	if audience != "" {
		query += ` AND audience = ?`
		args = append(args, audience)
	}
	var n int
	err := ms.db.QueryRow(ms.q(query), args...).Scan(&n)
	return n, err
}

// AgentSummary is what an agent's owner sees of its messages: how many it
// sent in a window, and how many landed in a muted conversation.
type AgentSummary struct {
	Sent  int `json:"sent"`
	Muted int `json:"muted"`
}

// Summary counts an agent's messages since a time.
func (ms *MessageStore) Summary(agentID string, since time.Time) (AgentSummary, error) {
	var s AgentSummary
	err := ms.db.QueryRow(ms.q(`SELECT COUNT(*), COALESCE(SUM(muted), 0) FROM agent_messages
		WHERE agent_id = ? AND author = ? AND created_at >= ?`), agentID, MessageFromAgent, fixedTime(since)).Scan(&s.Sent, &s.Muted)
	return s, err
}

// Prune deletes messages older than before, and conversations left empty.
func (ms *MessageStore) Prune(before time.Time) error {
	if _, err := ms.db.Exec(ms.q(`DELETE FROM agent_messages WHERE created_at < ?`), fixedTime(before)); err != nil {
		return err
	}
	_, err := ms.db.Exec(ms.q(`DELETE FROM agent_conversations WHERE last_message_at < ?
		AND id NOT IN (SELECT conversation_id FROM agent_messages)`), fixedTime(before))
	return err
}
