package mcpserver

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// next_events reads the calling agent's trigger events (ADR-047): the
// failures and recoveries a trigger that delivers to a feed has recorded
// for it, for an agent that cannot be reached by a webhook.

// reads acknowledges what it read before: reading again with the same
// cursor changes nothing more.
var reads = &mcp.ToolAnnotations{Title: "Read your trigger events", ReadOnlyHint: false, DestructiveHint: new(false),
	IdempotentHint: true, OpenWorldHint: new(false)}

type nextEventsIn struct {
	After string `json:"after,omitempty" jsonschema:"the cursor the previous call answered; passing it acknowledges every event up to it"`
	Limit int    `json:"limit,omitempty" jsonschema:"how many events at most (50 by default, at most 200)"`
}

func addNextEvents(s *mcp.Server, c Caller) {
	mcp.AddTool(s, &mcp.Tool{Name: "next_events", Title: "Read your trigger events", Annotations: reads,
		Description: "Answers your waiting trigger events, oldest first: runs that failed, were blocked, or recovered, for triggers that deliver to your feed. Each event names the pipeline and the run; for a failure, start with explain_run on the run. Call again with the cursor this answers to acknowledge what you read; an event you did not acknowledge comes back. Only an agent identity has a feed."},
		func(ctx context.Context, _ *mcp.CallToolRequest, in nextEventsIn) (*mcp.CallToolResult, any, error) {
			q := url.Values{}
			if in.After != "" {
				q.Set("after", in.After)
			}
			if in.Limit > 0 {
				q.Set("limit", strconv.Itoa(in.Limit))
			}
			path := "/api/agents/me/events"
			if len(q) > 0 {
				path += "?" + q.Encode()
			}
			var res struct {
				Events []json.RawMessage `json:"events"`
				Cursor string            `json:"cursor"`
			}
			if err := call(ctx, c, http.MethodGet, path, nil, &res); err != nil {
				return nil, nil, err
			}
			events := make([]any, 0, len(res.Events))
			for _, raw := range res.Events {
				var e any
				_ = json.Unmarshal(raw, &e)
				events = append(events, e)
			}
			return nil, safe(map[string]any{"events": events, "cursor": res.Cursor,
				"next": "Call next_events with this cursor to acknowledge these and read on."}), nil
		})
}
