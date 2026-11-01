package mcpserver

import (
	"context"
	"net/http"
	"net/url"
	"strconv"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// send_message lets an agent tell a person something (ADR-048): what it
// found, a decision it needs, something urgent. Messages land in the
// person's inbox in Brokoli, from the agent, with what they link to.

var sends = &mcp.ToolAnnotations{Title: "Send a message to a person", ReadOnlyHint: false, DestructiveHint: new(false),
	IdempotentHint: false, OpenWorldHint: new(false)}

type messageLinkIn struct {
	Kind string `json:"kind" jsonschema:"pipeline, run or change_request"`
	ID   string `json:"id" jsonschema:"the pipeline's, run's or change request's id"`
}

type sendMessageIn struct {
	To    string          `json:"to" jsonschema:"owner (the person who owns you), a person's username or id in your workspace, or workspace (everyone, info only)"`
	Text  string          `json:"text" jsonschema:"what you want them to know or decide; plain text with simple Markdown, at most 8 KB; never a credential"`
	Level string          `json:"level,omitempty" jsonschema:"info (default): worth knowing; attention: needs a decision or action, not now; urgent: needs someone now (owner, or someone who wrote to you; at most 3 a day)"`
	Topic string          `json:"topic,omitempty" jsonschema:"a one-line subject, to keep matters apart in the conversation"`
	Links []messageLinkIn `json:"links,omitempty" jsonschema:"what the message is about; the reader gets a link to each (they must be able to see it)"`
}

func addSendMessage(s *mcp.Server, c Caller) {
	mcp.AddTool(s, &mcp.Tool{Name: "send_message", Title: "Send a message to a person", Annotations: sends,
		Description: "Sends a message from you to a person in Brokoli: your owner, someone in your workspace, or the whole workspace. Use it to report what you found, ask for a decision, or raise something urgent; link the runs, pipelines and change requests it is about. Choose the level for what it asks of the reader, and keep messages few: there are limits per hour and per day, and people can mute an agent. It is not for chatting about routine progress."},
		func(ctx context.Context, _ *mcp.CallToolRequest, in sendMessageIn) (*mcp.CallToolResult, any, error) {
			var out map[string]any
			if err := call(ctx, c, http.MethodPost, "/api/agents/me/messages", in, &out); err != nil {
				return nil, nil, err
			}
			return nil, safe(out), nil
		})
}

// read_messages is how an agent reads what people wrote to it (ADR-048
// phase 2): unread messages across its conversations, which it then marks
// read, or one conversation's thread.

var readsMessages = &mcp.ToolAnnotations{Title: "Read messages people sent you", ReadOnlyHint: false, DestructiveHint: new(false),
	IdempotentHint: true, OpenWorldHint: new(false)}

type readMessagesIn struct {
	ConversationID string `json:"conversation_id,omitempty" jsonschema:"read this conversation's whole thread, oldest first; without it, your unread messages from people"`
	Limit          int    `json:"limit,omitempty" jsonschema:"how many messages at most (50 by default, at most 200)"`
}

func addReadMessages(s *mcp.Server, c Caller) {
	mcp.AddTool(s, &mcp.Tool{Name: "read_messages", Title: "Read messages people sent you", Annotations: readsMessages,
		Description: "Answers the messages people wrote to you, oldest first, and marks them read; with conversation_id, that conversation's whole thread (yours and theirs) instead. Each message names who wrote it (from_name) and its conversation. Answer a person with send_message, to their username: it goes into the same conversation."},
		func(ctx context.Context, _ *mcp.CallToolRequest, in readMessagesIn) (*mcp.CallToolResult, any, error) {
			limit := page(in.Limit, 50)
			if in.ConversationID != "" {
				var out map[string]any
				if err := call(ctx, c, http.MethodGet, "/api/agents/me/conversations/"+url.PathEscape(in.ConversationID)+"?limit="+strconv.Itoa(limit), nil, &out); err != nil {
					return nil, nil, err
				}
				return nil, safe(out), nil
			}
			var res struct {
				Messages []map[string]any `json:"messages"`
			}
			if err := call(ctx, c, http.MethodGet, "/api/agents/me/messages?unread=true&limit="+strconv.Itoa(limit), nil, &res); err != nil {
				return nil, nil, err
			}
			ids := make([]string, 0, len(res.Messages))
			for _, m := range res.Messages {
				if id, ok := m["id"].(string); ok {
					ids = append(ids, id)
				}
			}
			if len(ids) > 0 {
				if err := call(ctx, c, http.MethodPost, "/api/agents/me/messages/read", map[string]any{"message_ids": ids}, nil); err != nil {
					return nil, nil, err
				}
			}
			next := "These are now marked read. Answer with send_message to the person's username."
			if len(ids) == 0 {
				next = "No unread messages."
			}
			return nil, safe(map[string]any{"messages": res.Messages, "next": next}), nil
		})
}
