package mcpserver

import (
	"context"
	"errors"
	"net/http"
	"sort"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Acting tools (ADR-046 phase 4): an agent runs a pipeline, previews one,
// and cancels a run. They are marked as acting in their annotations, so a
// client that asks the person before acting asks here. A run started here
// is the caller's, recorded as the agent and for whom it acts.

// maxWait bounds how long trigger_run waits for its run: under the API's
// request timeout, so the call returns an answer rather than a timeout.
const maxWait = 45

// previewShownRows is how many rows of each step dry_run_pipeline returns.
const previewShownRows = 5

// runs starts work outside the conversation: a run reads and writes what
// its pipeline does.
var runs = &mcp.ToolAnnotations{Title: "Run a pipeline", ReadOnlyHint: false, DestructiveHint: new(true),
	IdempotentHint: false, OpenWorldHint: new(true)}

// cancels stops a run; cancelling it again changes nothing.
var cancels = &mcp.ToolAnnotations{Title: "Cancel a run", ReadOnlyHint: false, DestructiveHint: new(true),
	IdempotentHint: true, OpenWorldHint: new(false)}

// previews runs a pipeline's steps on a few rows and writes nothing, but
// its sources read from outside.
var previews = &mcp.ToolAnnotations{Title: "Preview a pipeline", ReadOnlyHint: true, IdempotentHint: true,
	OpenWorldHint: new(true)}

var terminal = map[string]bool{
	"success": true, "succeeded": true, "completed": true,
	"failed": true, "error": true, "timeout": true, "cancelled": true, "canceled": true,
}

type triggerIn struct {
	Pipeline    string            `json:"pipeline" jsonschema:"the pipeline's id"`
	Params      map[string]string `json:"params,omitempty" jsonschema:"the pipeline's params to override, as strings"`
	Parameters  map[string]any    `json:"parameters,omitempty" jsonschema:"values for the pipeline's declared parameters (get_pipeline lists them, with their types)"`
	WaitSeconds int               `json:"wait_seconds,omitempty" jsonschema:"wait up to this many seconds (at most 45) for the run to finish and answer with its outcome; 0 answers at once with the run's id"`
}

func addTriggerRun(s *mcp.Server, c Caller) {
	mcp.AddTool(s, &mcp.Tool{Name: "trigger_run", Title: "Run a pipeline", Annotations: runs,
		Description: "Runs a pipeline now, as it is saved, with optional params and declared parameters. The run reads and writes what the pipeline does, so be sure it should run: preview it first with dry_run_pipeline when unsure. Answers the run's id at once, or, with wait_seconds, its outcome when it finishes in time (otherwise its state so far; follow it with get_run, and explain_run when it fails). The run is recorded as yours."},
		func(ctx context.Context, _ *mcp.CallToolRequest, in triggerIn) (*mcp.CallToolResult, any, error) {
			if in.Pipeline == "" {
				return nil, nil, errors.New("give the pipeline's id")
			}
			body := map[string]any{}
			if len(in.Params) > 0 {
				body["params"] = in.Params
			}
			if len(in.Parameters) > 0 {
				body["parameters"] = in.Parameters
			}
			var started struct {
				ID string `json:"id"`
			}
			if err := call(ctx, c, http.MethodPost, "/api/pipelines/"+esc(in.Pipeline)+"/run", body, &started); err != nil {
				return nil, nil, err
			}
			wait := in.WaitSeconds
			if wait > maxWait {
				wait = maxWait
			}
			out := map[string]any{"run_id": started.ID}
			if wait <= 0 {
				out["status"] = "pending"
				out["next"] = "follow it with get_run; explain_run says why if it fails"
				return nil, safe(out), nil
			}
			r, err := waitForRun(ctx, c, started.ID, time.Duration(wait)*time.Second)
			if err != nil {
				return nil, nil, err
			}
			out["run"] = r.clipped()
			out["finished"] = terminal[r.Status]
			switch {
			case !terminal[r.Status]:
				out["next"] = "still running; follow it with get_run"
			case r.Status != "success" && r.Status != "succeeded" && r.Status != "completed":
				out["next"] = "explain_run says why it did not succeed"
			}
			return nil, safe(out), nil
		})
}

// waitForRun polls a run until it ends or wait passes, and answers it as
// it last was.
func waitForRun(ctx context.Context, c Caller, id string, wait time.Duration) (*run, error) {
	deadline := time.Now().Add(wait)
	delay := 500 * time.Millisecond
	for {
		r, err := getRun(ctx, c, id)
		if err != nil {
			return nil, err
		}
		if terminal[r.Status] || time.Now().Add(delay).After(deadline) {
			return r, nil
		}
		select {
		case <-ctx.Done():
			return r, nil
		case <-time.After(delay):
		}
		if delay < 2*time.Second {
			delay *= 2
		}
	}
}

func addCancelRun(s *mcp.Server, c Caller) {
	mcp.AddTool(s, &mcp.Tool{Name: "cancel_run", Title: "Cancel a run", Annotations: cancels,
		Description: "Cancels a run that has not finished. What its steps already wrote stays written."},
		func(ctx context.Context, _ *mcp.CallToolRequest, in runIn) (*mcp.CallToolResult, any, error) {
			if in.Run == "" {
				return nil, nil, errors.New("give the run's id")
			}
			if err := call(ctx, c, http.MethodPost, "/api/runs/"+esc(in.Run)+"/cancel", nil, nil); err != nil {
				return nil, nil, err
			}
			return nil, safe(map[string]any{"run_id": in.Run, "status": "cancelled"}), nil
		})
}

type dryRunIn struct {
	Pipeline string `json:"pipeline" jsonschema:"the pipeline's id"`
}

type previewNode struct {
	NodeID  string           `json:"node_id"`
	Name    string           `json:"name"`
	Status  string           `json:"status"`
	Columns []string         `json:"columns,omitempty"`
	Rows    []map[string]any `json:"rows,omitempty"`
	Error   string           `json:"error,omitempty"`
	Reason  string           `json:"reason,omitempty"`
}

func addDryRunPipeline(s *mcp.Server, c Caller) {
	mcp.AddTool(s, &mcp.Tool{Name: "dry_run_pipeline", Title: "Preview a pipeline", Annotations: previews,
		Description: "Previews a saved pipeline: runs its steps on the first rows of its sources and answers what each step produced (columns, the first rows), its error, or why it did not run. Nothing is written and no run is recorded: steps that write outside Brokoli (outputs, notifications, shell, dbt) are held back, with what follows them. Sources are read, so the preview reaches what the pipeline reads."},
		func(ctx context.Context, _ *mcp.CallToolRequest, in dryRunIn) (*mcp.CallToolResult, any, error) {
			if in.Pipeline == "" {
				return nil, nil, errors.New("give the pipeline's id")
			}
			var res struct {
				Results map[string]previewNode `json:"results"`
				Error   string                 `json:"error"`
			}
			if err := call(ctx, c, http.MethodPost, "/api/pipelines/"+esc(in.Pipeline)+"/dry-run", nil, &res); err != nil {
				return nil, nil, err
			}
			nodes := make([]previewNode, 0, len(res.Results))
			for _, n := range res.Results {
				if len(n.Rows) > previewShownRows {
					n.Rows = n.Rows[:previewShownRows]
				}
				for _, row := range n.Rows {
					for k, v := range row {
						if s, ok := v.(string); ok {
							row[k] = clip(s, 200)
						}
					}
				}
				n.Error = clip(n.Error, maxText)
				nodes = append(nodes, n)
			}
			sort.Slice(nodes, func(i, j int) bool { return nodes[i].NodeID < nodes[j].NodeID })
			out := map[string]any{"nodes": nodes, "rows_shown_per_step": previewShownRows}
			if res.Error != "" {
				out["error"] = clip(res.Error, maxText)
			}
			return nil, safe(out), nil
		})
}
