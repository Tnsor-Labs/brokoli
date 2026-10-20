package mcpserver

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// A tool is one MCP tool and the permission its REST route needs. The
// permission decides only whether the tool is listed to a caller; the
// route still decides every call.
type tool struct {
	permission string
	add        func(s *mcp.Server, c Caller)
}

// Permitted reports whether the caller holds a permission.
type Permitted func(permission string) bool

// New is an MCP server whose tools act through c, listing only the tools
// permitted allows.
func New(c Caller, permitted Permitted, version string) *mcp.Server {
	s := mcp.NewServer(&mcp.Implementation{Name: "brokoli", Title: "Brokoli", Version: version}, &mcp.ServerOptions{
		Instructions: instructions,
	})
	for _, t := range tools() {
		if permitted == nil || permitted(t.permission) {
			t.add(s, c)
		}
	}
	return s
}

const instructions = `Brokoli runs data pipelines. Use these tools to see the pipelines you may see, their definitions (the pipeline IR), their runs and logs, and the lineage between their datasets, and to validate a pipeline definition without saving it.

To find out why a run failed, start with explain_run: it gathers the failing node, its error, the relevant log lines and what changed since the last successful run. Lists are paginated and long text is cut; every result says how to get more. Credentials are never shown.`

// Permissions asks the API which permissions the caller holds, in the
// workspace of the request. A server without accounts (no sign-in at all)
// has no permissions endpoint, and every permission holds there.
func Permissions(ctx context.Context, c Caller) Permitted {
	status, data, err := c.Do(ctx, http.MethodGet, "/api/auth/me/permissions", nil)
	if err == nil && status == http.StatusNotFound {
		return func(string) bool { return true }
	}
	var held []string
	if err != nil || status != http.StatusOK || json.Unmarshal(data, &held) != nil {
		return func(string) bool { return false }
	}
	set := make(map[string]bool, len(held))
	for _, p := range held {
		set[p] = true
	}
	return func(p string) bool { return set[p] }
}

// HTTPHandler serves MCP over Streamable HTTP for the API server whose
// router is router: each request gets a server acting as its sender, with
// the tools that sender may use. Stateless: nothing is kept between
// requests, so any API instance can answer any request.
func HTTPHandler(router http.Handler, version string) http.Handler {
	return mcp.NewStreamableHTTPHandler(func(r *http.Request) *mcp.Server {
		c := NewRouterCaller(router, r)
		return New(c, Permissions(r.Context(), c), version)
	}, &mcp.StreamableHTTPOptions{Stateless: true, JSONResponse: true})
}

// Serve runs an MCP server acting through c on transport t (stdio, for
// `brokoli mcp`) until the client disconnects or ctx ends. The caller's
// permissions are read once, when it starts.
func Serve(ctx context.Context, c Caller, version string, t mcp.Transport) error {
	return New(c, Permissions(ctx, c), version).Run(ctx, t)
}
