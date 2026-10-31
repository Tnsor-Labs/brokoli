package mcpserver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Bounds on what one tool call returns, so a result fits an agent's
// context: lists are pages, text is cut, and each result says how to get
// more.
const (
	defaultPage  = 50
	maxPage      = 100 // the pipeline list's own largest page
	defaultLines = 200
	maxLines     = 1000
	maxText      = 2000 // one error message or log line
)

// readOnly marks a tool that changes nothing.
var readOnly = &mcp.ToolAnnotations{ReadOnlyHint: true, IdempotentHint: true, OpenWorldHint: new(false)}

// add registers a read-only tool whose handler returns a JSON-able value.
// The value is redacted before it leaves.
func add[In any](s *mcp.Server, name, title, description string, h func(context.Context, In) (any, error)) {
	annotations := *readOnly
	annotations.Title = title
	mcp.AddTool(s, &mcp.Tool{Name: name, Title: title, Description: description, Annotations: &annotations},
		func(ctx context.Context, _ *mcp.CallToolRequest, in In) (*mcp.CallToolResult, any, error) {
			out, err := h(ctx, in)
			if err != nil {
				return nil, nil, err
			}
			return nil, safe(out), nil
		})
}

// safe is v as plain JSON values with every credential redacted.
func safe(v any) any {
	data, err := json.Marshal(v)
	if err != nil {
		return nil
	}
	var out any
	_ = json.Unmarshal(data, &out)
	return redact(out)
}

func tools() []tool {
	return []tool{
		{permission: "pipelines.view", add: addListPipelines},
		{permission: "pipelines.view", add: addGetPipeline},
		{permission: "pipelines.view", add: addValidatePipeline},
		{permission: "runs.view", add: addListRuns},
		{permission: "runs.view", add: addGetRun},
		{permission: "runs.view", add: addGetRunLogs},
		{permission: "runs.view", add: addExplainRun},
		{permission: "pipelines.view", add: addGetLineage},
		{permission: "pipelines.view", add: addListChangeRequests},
		{permission: "pipelines.propose", add: addProposeChange},
		{permission: "pipelines.run", add: addDryRunPipeline},
		{permission: "pipelines.run", add: addTriggerRun},
		{permission: "runs.cancel", add: addCancelRun},
		{permission: "runs.view", add: addNextEvents},
	}
}

// page is a page size within bounds.
func page(n, def int) int {
	switch {
	case n <= 0:
		return def
	case n > maxPage:
		return maxPage
	}
	return n
}

func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + fmt.Sprintf("... [%d more characters]", len(s)-n)
}

func esc(s string) string { return url.PathEscape(s) }

// --- pipelines

type listPipelinesIn struct {
	Limit  int    `json:"limit,omitempty" jsonschema:"how many pipelines to return, 1 to 100 (default 50)"`
	Cursor string `json:"cursor,omitempty" jsonschema:"next_cursor from the previous page, for the next one"`
}

type pipelineSummary struct {
	ID          string   `json:"id"`
	PipelineID  string   `json:"pipeline_id,omitempty"`
	Name        string   `json:"name"`
	Description string   `json:"description,omitempty"`
	Schedule    string   `json:"schedule,omitempty"`
	Enabled     bool     `json:"enabled"`
	Draft       bool     `json:"draft,omitempty"`
	Tags        []string `json:"tags,omitempty"`
	Source      string   `json:"source,omitempty"`
	NodeCount   int      `json:"node_count"`
	UpdatedAt   string   `json:"updated_at,omitempty"`
}

func addListPipelines(s *mcp.Server, c Caller) {
	add(s, "list_pipelines", "List pipelines",
		"The pipelines you may see in this workspace: id, name, schedule, whether enabled, source (ui or git), size. Paginated.",
		func(ctx context.Context, in listPipelinesIn) (any, error) {
			limit := page(in.Limit, defaultPage)
			q := url.Values{"limit": {strconv.Itoa(limit)}}
			if in.Cursor != "" {
				q.Set("after", in.Cursor)
			}
			var raw json.RawMessage
			if err := get(ctx, c, "/api/pipelines?"+q.Encode(), &raw); err != nil {
				return nil, err
			}
			items, next, err := pipelinePage(raw, in.Cursor, limit)
			if err != nil {
				return nil, err
			}
			out := map[string]any{"pipelines": items}
			if next != "" {
				out["next_cursor"] = next
			}
			return out, nil
		})
}

// pipelinePage reads the pipeline list in either of its shapes: a page
// ({items, has_next, cursor}) where pipelines belong to organizations, or
// every pipeline at once otherwise, paged here with the cursor as an
// offset.
func pipelinePage(raw json.RawMessage, cursor string, limit int) ([]pipelineSummary, string, error) {
	var all []pipelineSummary
	if err := json.Unmarshal(raw, &all); err == nil {
		start, _ := strconv.Atoi(cursor)
		if start < 0 || start > len(all) {
			start = len(all)
		}
		end := start + limit
		if end >= len(all) {
			return all[start:], "", nil
		}
		return all[start:end], strconv.Itoa(end), nil
	}
	var res struct {
		Items   []pipelineSummary `json:"items"`
		HasNext bool              `json:"has_next"`
		Cursor  string            `json:"cursor"`
	}
	if err := json.Unmarshal(raw, &res); err != nil {
		return nil, "", fmt.Errorf("the pipeline list is not what was expected: %w", err)
	}
	if !res.HasNext {
		return res.Items, "", nil
	}
	return res.Items, res.Cursor, nil
}

type pipelineIn struct {
	Pipeline string `json:"pipeline" jsonschema:"the pipeline's id (the id field of list_pipelines)"`
}

func addGetPipeline(s *mcp.Server, c Caller) {
	add(s, "get_pipeline", "Get a pipeline",
		"A pipeline's full definition (the pipeline IR): its nodes with their type and settings, the edges between them, schedule, parameters. Node positions on the canvas are left out.",
		func(ctx context.Context, in pipelineIn) (any, error) {
			if in.Pipeline == "" {
				return nil, errors.New("give the pipeline's id")
			}
			var p map[string]any
			if err := get(ctx, c, "/api/pipelines/"+esc(in.Pipeline), &p); err != nil {
				return nil, err
			}
			if nodes, ok := p["nodes"].([]any); ok {
				for _, n := range nodes {
					if m, ok := n.(map[string]any); ok {
						delete(m, "position")
					}
				}
			}
			return p, nil
		})
}

type validateIn struct {
	Pipeline   string         `json:"pipeline,omitempty" jsonschema:"a saved pipeline's id, to validate it as saved"`
	Definition map[string]any `json:"definition,omitempty" jsonschema:"a pipeline definition (IR) to validate without saving it"`
}

func addValidatePipeline(s *mcp.Server, c Caller) {
	add(s, "validate_pipeline", "Validate a pipeline",
		"Checks a pipeline definition the way saving and running it would, and lists what is wrong. Give either a saved pipeline's id or a definition (IR) to check without saving anything.",
		func(ctx context.Context, in validateIn) (any, error) {
			var out map[string]any
			switch {
			case in.Pipeline != "" && in.Definition != nil:
				return nil, errors.New("give a pipeline id or a definition, not both")
			case in.Pipeline != "":
				return out, get(ctx, c, "/api/pipelines/"+esc(in.Pipeline)+"/validate", &out)
			case in.Definition != nil:
				return out, call(ctx, c, http.MethodPost, "/api/pipelines/validate", in.Definition, &out)
			}
			return nil, errors.New("give a pipeline id or a definition")
		})
}

// --- runs

type nodeRun struct {
	NodeID     string `json:"node_id"`
	Status     string `json:"status"`
	Error      string `json:"error,omitempty"`
	Attempt    int    `json:"attempt,omitempty"`
	RowCount   int    `json:"row_count"`
	DurationMs int64  `json:"duration_ms"`
	StartedAt  string `json:"started_at,omitempty"`
}

type run struct {
	ID              string    `json:"id"`
	PipelineID      string    `json:"pipeline_id"`
	Status          string    `json:"status"`
	Error           string    `json:"error,omitempty"`
	StartedAt       string    `json:"started_at,omitempty"`
	FinishedAt      string    `json:"finished_at,omitempty"`
	PipelineVersion int       `json:"pipeline_version,omitempty"`
	NodeRuns        []nodeRun `json:"node_runs,omitempty"`
}

func (r *run) clipped() *run {
	out := *r
	out.Error = clip(out.Error, maxText)
	out.NodeRuns = make([]nodeRun, len(r.NodeRuns))
	for i, n := range r.NodeRuns {
		n.Error = clip(n.Error, maxText)
		out.NodeRuns[i] = n
	}
	return &out
}

type listRunsIn struct {
	Pipeline string `json:"pipeline" jsonschema:"the pipeline's id"`
	Limit    int    `json:"limit,omitempty" jsonschema:"how many runs, newest first, 1 to 100 (default 50)"`
	Cursor   string `json:"cursor,omitempty" jsonschema:"next_cursor from the previous page"`
}

func listRuns(ctx context.Context, c Caller, pipeline string, limit int, cursor string) ([]run, string, error) {
	q := url.Values{"limit": {strconv.Itoa(limit)}}
	if cursor != "" {
		q.Set("after", cursor)
	}
	var res struct {
		Items   []run  `json:"items"`
		HasNext bool   `json:"has_next"`
		Cursor  string `json:"cursor"`
	}
	if err := get(ctx, c, "/api/pipelines/"+esc(pipeline)+"/runs?"+q.Encode(), &res); err != nil {
		return nil, "", err
	}
	next := ""
	if res.HasNext {
		next = res.Cursor
	}
	return res.Items, next, nil
}

func addListRuns(s *mcp.Server, c Caller) {
	add(s, "list_runs", "List a pipeline's runs",
		"A pipeline's runs, newest first: status, when, the version of the pipeline it ran, and the error of a failed one. Paginated.",
		func(ctx context.Context, in listRunsIn) (any, error) {
			if in.Pipeline == "" {
				return nil, errors.New("give the pipeline's id")
			}
			runs, next, err := listRuns(ctx, c, in.Pipeline, page(in.Limit, defaultPage), in.Cursor)
			if err != nil {
				return nil, err
			}
			for i := range runs {
				runs[i].Error = clip(runs[i].Error, maxText)
				runs[i].NodeRuns = nil
			}
			out := map[string]any{"runs": runs}
			if next != "" {
				out["next_cursor"] = next
			}
			return out, nil
		})
}

type runIn struct {
	Run string `json:"run" jsonschema:"the run's id"`
}

func getRun(ctx context.Context, c Caller, id string) (*run, error) {
	if id == "" {
		return nil, errors.New("give the run's id")
	}
	var r run
	if err := get(ctx, c, "/api/runs/"+esc(id), &r); err != nil {
		return nil, err
	}
	return &r, nil
}

func addGetRun(s *mcp.Server, c Caller) {
	add(s, "get_run", "Get a run",
		"One run: its status, timing, the pipeline version it ran, and each node's status, rows, duration and error.",
		func(ctx context.Context, in runIn) (any, error) {
			r, err := getRun(ctx, c, in.Run)
			if err != nil {
				return nil, err
			}
			return r.clipped(), nil
		})
}

type logsIn struct {
	Run    string `json:"run" jsonschema:"the run's id"`
	Node   string `json:"node,omitempty" jsonschema:"only this node's lines (a node id)"`
	Level  string `json:"level,omitempty" jsonschema:"only lines of this level: debug, info, warning or error"`
	Offset *int   `json:"offset,omitempty" jsonschema:"the first line to return, from 0; without it, the last lines are returned"`
	Limit  int    `json:"limit,omitempty" jsonschema:"how many lines, 1 to 1000 (default 200)"`
}

type logLine struct {
	Time    string `json:"time"`
	Node    string `json:"node,omitempty"`
	Level   string `json:"level"`
	Message string `json:"message"`
}

func runLogs(ctx context.Context, c Caller, id, node, level string) ([]logLine, error) {
	q := url.Values{}
	if node != "" {
		q.Set("node_id", node)
	}
	if level != "" {
		q.Set("level", level)
	}
	path := "/api/runs/" + esc(id) + "/logs"
	if len(q) > 0 {
		path += "?" + q.Encode()
	}
	var raw []struct {
		Timestamp string `json:"timestamp"`
		NodeID    string `json:"node_id"`
		Level     string `json:"level"`
		Message   string `json:"message"`
	}
	if err := get(ctx, c, path, &raw); err != nil {
		return nil, err
	}
	out := make([]logLine, len(raw))
	for i, l := range raw {
		out[i] = logLine{Time: l.Timestamp, Node: l.NodeID, Level: l.Level, Message: clip(l.Message, maxText)}
	}
	return out, nil
}

func addGetRunLogs(s *mcp.Server, c Caller) {
	add(s, "get_run_logs", "Get a run's logs",
		"A run's log lines, optionally for one node or one level. Returns the last lines unless an offset is given; says how many lines there are in all.",
		func(ctx context.Context, in logsIn) (any, error) {
			if in.Run == "" {
				return nil, errors.New("give the run's id")
			}
			lines, err := runLogs(ctx, c, in.Run, in.Node, in.Level)
			if err != nil {
				return nil, err
			}
			limit := in.Limit
			switch {
			case limit <= 0:
				limit = defaultLines
			case limit > maxLines:
				limit = maxLines
			}
			start := len(lines) - limit
			if in.Offset != nil {
				start = *in.Offset
			}
			if start < 0 {
				start = 0
			}
			if start > len(lines) {
				start = len(lines)
			}
			end := start + limit
			if end > len(lines) {
				end = len(lines)
			}
			out := map[string]any{"total": len(lines), "offset": start, "lines": lines[start:end]}
			if start > 0 || end < len(lines) {
				out["more"] = fmt.Sprintf("lines %d to %d of %d; pass offset (and limit) for others", start, end, len(lines))
			}
			return out, nil
		})
}

// --- lineage

type lineageNode struct {
	ID         string `json:"id"`
	Type       string `json:"type"`
	Name       string `json:"name"`
	SubType    string `json:"sub_type,omitempty"`
	PipelineID string `json:"pipeline_id,omitempty"`
	Pipeline   string `json:"pipeline,omitempty"`
}

type lineageEdge struct {
	From       string `json:"from"`
	To         string `json:"to"`
	PipelineID string `json:"pipeline_id"`
	Pipeline   string `json:"pipeline"`
}

func addGetLineage(s *mcp.Server, c Caller) {
	add(s, "get_lineage", "Get a pipeline's lineage",
		"What a pipeline reads and writes (files, tables, APIs), and the other pipelines upstream of it (writing what it reads) and downstream (reading what it writes).",
		func(ctx context.Context, in pipelineIn) (any, error) {
			if in.Pipeline == "" {
				return nil, errors.New("give the pipeline's id")
			}
			var g struct {
				Nodes []lineageNode `json:"nodes"`
				Edges []lineageEdge `json:"edges"`
			}
			if err := get(ctx, c, "/api/lineage", &g); err != nil {
				return nil, err
			}
			return neighbourhood(in.Pipeline, g.Nodes, g.Edges), nil
		})
}

type dataset struct {
	ID   string `json:"id"`
	Type string `json:"type"`
	Name string `json:"name"`
}

type related struct {
	PipelineID string `json:"pipeline_id"`
	Pipeline   string `json:"pipeline"`
	Via        string `json:"via"` // the dataset they share
}

// neighbourhood is pipeline's datasets and the pipelines on either side,
// from the whole lineage graph.
func neighbourhood(pipeline string, nodes []lineageNode, edges []lineageEdge) map[string]any {
	byID := map[string]lineageNode{}
	for _, n := range nodes {
		byID[n.ID] = n
	}
	isData := func(id string) bool { n, ok := byID[id]; return ok && n.Type != "processing" }
	asDataset := func(id string) dataset { n := byID[id]; return dataset{ID: n.ID, Type: n.Type, Name: n.Name} }

	reads, writes := map[string]bool{}, map[string]bool{}
	readList, writeList := []dataset{}, []dataset{}
	for _, e := range edges {
		if e.PipelineID != pipeline {
			continue
		}
		if isData(e.From) && !reads[e.From] {
			reads[e.From] = true
			readList = append(readList, asDataset(e.From))
		}
		if isData(e.To) && !writes[e.To] {
			writes[e.To] = true
			writeList = append(writeList, asDataset(e.To))
		}
	}
	upstream, downstream := []related{}, []related{}
	seen := map[string]bool{}
	for _, e := range edges {
		if e.PipelineID == pipeline || e.PipelineID == "" {
			continue
		}
		if writes[e.From] && !seen["d"+e.PipelineID+e.From] {
			seen["d"+e.PipelineID+e.From] = true
			downstream = append(downstream, related{PipelineID: e.PipelineID, Pipeline: e.Pipeline, Via: byID[e.From].Name})
		}
		if reads[e.To] && !seen["u"+e.PipelineID+e.To] {
			seen["u"+e.PipelineID+e.To] = true
			upstream = append(upstream, related{PipelineID: e.PipelineID, Pipeline: e.Pipeline, Via: byID[e.To].Name})
		}
	}
	return map[string]any{"reads": readList, "writes": writeList, "upstream": upstream, "downstream": downstream}
}
