package mcpserver

import (
	"context"
	"sync"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// A distribution adds to the MCP server (ADR-046: "a distribution can
// register further tools ... under the same rules"). Its tools are REST
// calls through the same Caller, listed by the permission each needs, and
// their results are redacted and bounded like core's. It can also add
// context to explain_run.

// Tool is a tool a distribution adds.
type Tool struct {
	// Permission is what the caller must hold for the tool to be listed;
	// the routes it calls still decide every call.
	Permission string
	// Add registers the tool on s, acting through c (use AddReadTool).
	Add func(s *mcp.Server, c Caller)
}

// ExplainContext adds to an explain_run answer: named facts about the run
// that core does not know (where its pipeline was deployed from, the change
// behind it, an issue about it). Return nil to add nothing; an error is
// reported in could_not_check rather than failing the explanation.
type ExplainContext func(ctx context.Context, c Caller, run RunInfo) (map[string]any, error)

// RunInfo is what an ExplainContext gets about the run being explained.
type RunInfo struct {
	ID              string
	PipelineID      string
	Status          string
	PipelineVersion int
}

// Extension is what a distribution registers.
type Extension struct {
	Name    string // for could_not_check messages
	Tools   []Tool
	Explain ExplainContext
}

var (
	extMu      sync.RWMutex
	extensions []Extension
)

// Register adds a distribution's tools and explain context to every MCP
// server built after it. Call it once, at start-up.
func Register(e Extension) {
	extMu.Lock()
	defer extMu.Unlock()
	extensions = append(extensions, e)
}

func registered() []Extension {
	extMu.RLock()
	defer extMu.RUnlock()
	return append([]Extension(nil), extensions...)
}

// AddReadTool registers a read-only tool whose handler answers a JSON-able
// value; the value is redacted before it leaves, as core's tools are.
func AddReadTool[In any](s *mcp.Server, name, title, description string, h func(context.Context, In) (any, error)) {
	add(s, name, title, description, h)
}

// Get calls GET path (with /api) through c and decodes the answer into out;
// a non-2xx answer is an *APIError the agent can act on.
func Get(ctx context.Context, c Caller, path string, out any) error { return get(ctx, c, path, out) }

// Esc escapes one path segment.
func Esc(s string) string { return esc(s) }
