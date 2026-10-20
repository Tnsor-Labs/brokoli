package mcpserver

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type pipelineArg struct {
	Pipeline string `json:"pipeline"`
}

// A distribution's tools are listed by permission and answer through the
// same caller, redacted; its explain context lands in explain_run.
func TestExtensions(t *testing.T) {
	prev := registered()
	t.Cleanup(func() {
		extMu.Lock()
		extensions = prev
		extMu.Unlock()
	})
	Register(Extension{
		Name: "source",
		Tools: []Tool{{Permission: "pipelines.view", Add: func(s *mcp.Server, c Caller) {
			AddReadTool(s, "get_pipeline_source", "Source", "where it came from", func(ctx context.Context, in pipelineArg) (any, error) {
				var out map[string]any
				return out, Get(ctx, c, "/api/pipelines/"+Esc(in.Pipeline)+"/git", &out)
			})
		}}},
		Explain: func(ctx context.Context, c Caller, run RunInfo) (map[string]any, error) {
			return map[string]any{"deployed_from": "acme/pipelines@abc1234", "run": run.ID}, nil
		},
	})
	Register(Extension{Name: "broken", Explain: func(context.Context, Caller, RunInfo) (map[string]any, error) {
		return nil, errors.New("GitHub is down")
	}})

	api := &fakeAPI{answers: map[string]string{
		"/api/pipelines/p/git":  `{"repository":"acme/pipelines","token":"secret-123"}`,
		"/api/runs/r1/logs":     `[]`,
		"/api/runs/r1":          `{"id":"r1","pipeline_id":"p","status":"failed","node_runs":[]}`,
		"/api/pipelines/p/runs": `{"items":[]}`,
		"/api/pipelines/p":      `{"name":"P","nodes":[]}`,
	}}
	cs := session(t, api)
	if got := toolText(t, cs, "get_pipeline_source", map[string]any{"pipeline": "p"}); !strings.Contains(got, "acme/pipelines") || strings.Contains(got, "secret-123") {
		t.Fatalf("get_pipeline_source = %s", got)
	}
	ex, err := explainRun(context.Background(), api, "r1")
	if err != nil {
		t.Fatal(err)
	}
	if ex.Context["deployed_from"] != "acme/pipelines@abc1234" || ex.Context["run"] != "r1" {
		t.Fatalf("context = %v", ex.Context)
	}
	if len(ex.Unanswered) != 1 || ex.Unanswered[0] != "broken: GitHub is down" {
		t.Fatalf("could_not_check = %v", ex.Unanswered)
	}

	// Without the permission, the tool is not listed.
	st, ct := mcp.NewInMemoryTransports()
	go func() {
		_ = New(api, func(p string) bool { return p != "pipelines.view" }, "t").Run(context.Background(), st)
	}()
	c2, err := mcp.NewClient(&mcp.Implementation{Name: "t", Version: "1"}, nil).Connect(context.Background(), ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer c2.Close()
	list, _ := c2.ListTools(context.Background(), nil)
	for _, tl := range list.Tools {
		if tl.Name == "get_pipeline_source" {
			t.Fatal("listed without its permission")
		}
	}
}
