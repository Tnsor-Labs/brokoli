package mcpserver

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// explain_run answers "what happened in this run, and why" in one bounded
// result: what a person reads across the run, its logs, the pipeline and
// its history.

const (
	explainErrorLines = 20 // error lines from the whole run
	explainNodeLines  = 30 // last lines of each failing node
	explainHistory    = 50 // runs looked back through for the last good one
	explainVersions   = 10 // versions listed between the good run and this one
)

type failure struct {
	Node     string    `json:"node"`
	Name     string    `json:"name,omitempty"`
	Type     string    `json:"type,omitempty"`
	Error    string    `json:"error,omitempty"`
	Attempt  int       `json:"attempt,omitempty"`
	LastLogs []logLine `json:"last_logs,omitempty"`
}

type version struct {
	Version   int    `json:"version"`
	Message   string `json:"message,omitempty"`
	CreatedAt string `json:"created_at,omitempty"`
}

type explanation struct {
	Summary    string         `json:"summary"`
	Run        *run           `json:"run"`
	Pipeline   string         `json:"pipeline,omitempty"`
	Failures   []failure      `json:"failures,omitempty"`
	ErrorLogs  []logLine      `json:"error_logs,omitempty"`
	LastGood   *run           `json:"last_successful_run,omitempty"`
	Changes    []version      `json:"changes_since_last_success,omitempty"`
	Context    map[string]any `json:"context,omitempty"`
	Unanswered []string       `json:"could_not_check,omitempty"`
	Next       []string       `json:"next_steps,omitempty"`
}

func addExplainRun(s *mcp.Server, c Caller) {
	add(s, "explain_run", "Explain a run",
		"What happened in a run and why, in one answer: its outcome, each failing node with its error and last log lines, the run's error lines, the last successful run of the pipeline before it, and the changes to the pipeline since then. Start here when a run failed.",
		func(ctx context.Context, in runIn) (any, error) {
			return explainRun(ctx, c, in.Run)
		})
}

func explainRun(ctx context.Context, c Caller, id string) (*explanation, error) {
	r, err := getRun(ctx, c, id)
	if err != nil {
		return nil, err
	}
	ex := &explanation{Run: r.clipped()}
	ex.Run.NodeRuns = nil // the failures say what matters; get_run has every node

	// The pipeline, for node names and types. A run may outlive its pipeline.
	var p struct {
		Name  string `json:"name"`
		Nodes []struct {
			ID   string `json:"id"`
			Name string `json:"name"`
			Type string `json:"type"`
		} `json:"nodes"`
	}
	nodeName, nodeType := map[string]string{}, map[string]string{}
	if err := get(ctx, c, "/api/pipelines/"+esc(r.PipelineID), &p); err != nil {
		ex.Unanswered = append(ex.Unanswered, "the pipeline: "+err.Error())
	} else {
		ex.Pipeline = p.Name
		for _, n := range p.Nodes {
			nodeName[n.ID], nodeType[n.ID] = n.Name, n.Type
		}
	}

	for _, n := range r.NodeRuns {
		if n.Status != "failed" {
			continue
		}
		f := failure{Node: n.NodeID, Name: nodeName[n.NodeID], Type: nodeType[n.NodeID], Error: clip(n.Error, maxText), Attempt: n.Attempt}
		if lines, err := runLogs(ctx, c, r.ID, n.NodeID, ""); err == nil {
			f.LastLogs = tail(lines, explainNodeLines)
		}
		ex.Failures = append(ex.Failures, f)
	}
	if lines, err := runLogs(ctx, c, r.ID, "", "error"); err != nil {
		ex.Unanswered = append(ex.Unanswered, "the logs: "+err.Error())
	} else {
		ex.ErrorLogs = tail(lines, explainErrorLines)
	}

	// The last successful run before this one, and what changed since.
	if runs, _, err := listRuns(ctx, c, r.PipelineID, explainHistory, ""); err != nil {
		ex.Unanswered = append(ex.Unanswered, "earlier runs: "+err.Error())
	} else {
		// The latest success that started before this run; by time, not
		// by the list's order.
		before, okBefore := parseTime(r.StartedAt)
		var best time.Time
		for i := range runs {
			at, ok := parseTime(runs[i].StartedAt)
			if runs[i].ID == r.ID || runs[i].Status != "success" || !ok || (okBefore && !at.Before(before)) {
				continue
			}
			if ex.LastGood == nil || at.After(best) {
				good := runs[i]
				good.NodeRuns, good.Error = nil, ""
				ex.LastGood, best = &good, at
			}
		}
	}
	if ex.LastGood != nil && r.PipelineVersion > 0 && ex.LastGood.PipelineVersion > 0 &&
		r.PipelineVersion != ex.LastGood.PipelineVersion {
		var versions []version
		if err := get(ctx, c, "/api/pipelines/"+esc(r.PipelineID)+"/versions", &versions); err != nil {
			ex.Unanswered = append(ex.Unanswered, "the pipeline's versions: "+err.Error())
		} else {
			for _, v := range versions {
				if v.Version > ex.LastGood.PipelineVersion && v.Version <= r.PipelineVersion {
					v.Message = clip(v.Message, 300)
					ex.Changes = append(ex.Changes, v)
				}
			}
			if len(ex.Changes) > explainVersions {
				ex.Changes = ex.Changes[:explainVersions]
			}
		}
	}

	// What a distribution knows about the run, under its own names.
	for _, e := range registered() {
		if e.Explain == nil {
			continue
		}
		extra, err := e.Explain(ctx, c, RunInfo{ID: r.ID, PipelineID: r.PipelineID, Status: r.Status, PipelineVersion: r.PipelineVersion})
		if err != nil {
			ex.Unanswered = append(ex.Unanswered, e.Name+": "+err.Error())
			continue
		}
		for k, v := range extra {
			if ex.Context == nil {
				ex.Context = map[string]any{}
			}
			ex.Context[k] = v
		}
	}

	ex.Summary, ex.Next = summarize(ex, r)
	return ex, nil
}

func parseTime(s string) (time.Time, bool) {
	t, err := time.Parse(time.RFC3339Nano, s)
	return t, err == nil
}

func tail(lines []logLine, n int) []logLine {
	if len(lines) > n {
		return lines[len(lines)-n:]
	}
	return lines
}

// summarize is the explanation in a few sentences, and what to look at next.
func summarize(ex *explanation, r *run) (string, []string) {
	var b strings.Builder
	name := ex.Pipeline
	if name == "" {
		name = r.PipelineID
	}
	fmt.Fprintf(&b, "Run %s of %s: %s.", r.ID, name, r.Status)
	var next []string
	for _, f := range ex.Failures {
		label := f.Node
		if f.Name != "" {
			label = fmt.Sprintf("%q (%s, %s)", f.Name, f.Node, f.Type)
		}
		fmt.Fprintf(&b, " Node %s failed", label)
		if f.Attempt > 0 {
			fmt.Fprintf(&b, " after %d retries", f.Attempt)
		}
		if f.Error != "" {
			fmt.Fprintf(&b, ": %s", clip(firstLine(f.Error), 300))
		}
		b.WriteString(".")
	}
	if len(ex.Failures) == 0 && r.Error != "" {
		fmt.Fprintf(&b, " %s.", clip(firstLine(r.Error), 300))
	}
	switch {
	case ex.LastGood == nil && r.Status != "success":
		b.WriteString(" No successful run of this pipeline in its recent history.")
	case ex.LastGood != nil && len(ex.Changes) > 0:
		fmt.Fprintf(&b, " The last successful run was %s (version %d); the pipeline changed %d time(s) since.",
			ex.LastGood.ID, ex.LastGood.PipelineVersion, len(ex.Changes))
		next = append(next, "compare the pipeline now (get_pipeline) with what changed since the last successful run")
	case ex.LastGood != nil && r.Status != "success":
		fmt.Fprintf(&b, " The last successful run was %s, with the same pipeline version: the cause is likely outside the definition (its data, a source, a connection, the worker).", ex.LastGood.ID)
	}
	if len(ex.Failures) > 0 {
		next = append(next, "read more of a failing node's logs with get_run_logs (node, offset)")
		next = append(next, "check a fix with validate_pipeline before proposing it")
	}
	return b.String(), next
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}
