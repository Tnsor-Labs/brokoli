package mcpserver

import (
	"context"
	"net/http"

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
