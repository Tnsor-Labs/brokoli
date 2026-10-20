package mcpserver

import (
	"context"
	"errors"
	"net/http"
	"net/url"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Change requests (ADR-046 phase 3): the one way an agent changes a
// pipeline. It proposes; a person applies.

type proposeIn struct {
	Pipeline    string         `json:"pipeline" jsonschema:"the pipeline's id"`
	Definition  map[string]any `json:"definition" jsonschema:"the whole pipeline as it should be (the IR, as get_pipeline returns it, with your changes)"`
	Title       string         `json:"title" jsonschema:"what the change does, in a line, for the person deciding"`
	Description string         `json:"description,omitempty" jsonschema:"why, and anything the person deciding should check"`
}

// proposes changes nothing that runs, but records something.
var proposes = &mcp.ToolAnnotations{Title: "Propose a change", ReadOnlyHint: false, DestructiveHint: new(false),
	IdempotentHint: false, OpenWorldHint: new(false)}

func addProposeChange(s *mcp.Server, c Caller) {
	mcp.AddTool(s, &mcp.Tool{Name: "propose_change", Title: "Propose a change", Annotations: proposes,
		Description: "Proposes a new version of a pipeline as a change request. Nothing that runs changes: a person reviews the diff and the validation, and applies or declines it. Send the whole definition as it should be (start from get_pipeline), check it first with validate_pipeline, and give it a title that says what it does. Answers the request with its diff and validation."},
		func(ctx context.Context, _ *mcp.CallToolRequest, in proposeIn) (*mcp.CallToolResult, any, error) {
			if in.Pipeline == "" || in.Definition == nil || in.Title == "" {
				return nil, nil, errors.New("give the pipeline's id, the whole definition as it should be, and a title")
			}
			var cr map[string]any
			err := call(ctx, c, http.MethodPost, "/api/pipelines/"+esc(in.Pipeline)+"/changes",
				map[string]any{"pipeline": in.Definition, "title": in.Title, "description": in.Description}, &cr)
			if err != nil {
				return nil, nil, err
			}
			delete(cr, "proposed") // the agent has it; the diff says what changes
			return nil, safe(cr), nil
		})
}

type listChangesIn struct {
	Pipeline string `json:"pipeline" jsonschema:"the pipeline's id"`
	Status   string `json:"status,omitempty" jsonschema:"open, applied or declined (default: all)"`
}

func addListChangeRequests(s *mcp.Server, c Caller) {
	add(s, "list_change_requests", "List change requests",
		"A pipeline's change requests, newest first: title, status (open, applied, declined), whether an open one is stale (the pipeline changed since; it cannot be applied), its validation, who proposed it and who decided.",
		func(ctx context.Context, in listChangesIn) (any, error) {
			if in.Pipeline == "" {
				return nil, errors.New("give the pipeline's id")
			}
			path := "/api/pipelines/" + esc(in.Pipeline) + "/changes"
			if in.Status != "" {
				path += "?" + url.Values{"status": {in.Status}}.Encode()
			}
			var list []map[string]any
			if err := get(ctx, c, path, &list); err != nil {
				return nil, err
			}
			return map[string]any{"change_requests": list}, nil
		})
}
