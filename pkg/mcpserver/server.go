package mcpserver

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"

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
	return NewAs(c, permitted, version, Identity{})
}

// NewAs is New for a known caller: the instructions say who the agent acts
// as, and warn when that is the server's administrator key.
func NewAs(c Caller, permitted Permitted, version string, who Identity) *mcp.Server {
	s := mcp.NewServer(&mcp.Implementation{Name: "brokoli", Title: "Brokoli", Version: version}, &mcp.ServerOptions{
		Instructions: who.instructions() + instructions,
	})
	for _, t := range tools() {
		if permitted == nil || permitted(t.permission) {
			t.add(s, c)
		}
	}
	return s
}

const instructions = `Brokoli runs data pipelines. Use these tools to see the pipelines you may see, their definitions (the pipeline IR), their runs and logs, and the lineage between their datasets, and to validate a pipeline definition without saving it.

To change a pipeline, propose it with propose_change: the whole definition as it should be, and a title. Nothing runs differently until a person applies the change request. Validate first; say in the description why the change is needed.

To find out why a run failed, start with explain_run: it gathers the failing node, its error, the relevant log lines and what changed since the last successful run. Lists are paginated and long text is cut; every result says how to get more. Credentials are never shown.`

// Identity is who the MCP client acts as, from /api/auth/me.
type Identity struct {
	Subject    string `json:"sub"`
	Username   string `json:"username"`
	Kind       string `json:"kind"`
	OnBehalfOf string `json:"on_behalf_of"`
}

// StaticKey reports whether the identity is the server's static API key:
// an administrator, which ADR-046 says not to give an agent.
func (i Identity) StaticKey() bool { return strings.HasPrefix(i.Subject, "apikey:") }

// Describe is the identity in a few words.
func (i Identity) Describe() string {
	switch {
	case i.StaticKey():
		return "the server's static API key (an administrator)"
	case i.Kind == "agent" && i.OnBehalfOf != "":
		return "agent " + i.Username + ", on behalf of " + i.OnBehalfOf
	case i.Kind == "agent":
		return "agent " + i.Username
	}
	return i.Username
}

func (i Identity) instructions() string {
	switch {
	case i.Subject == "":
		return ""
	case i.StaticKey():
		return "WARNING: you are connected with the server's static API key, which is an administrator. Ask the person to give you an agent token instead (see docs/mcp.md).\n\n"
	}
	return "You act in Brokoli as " + i.Describe() + ".\n\n"
}

// Who asks the API who the caller is. Empty when it cannot tell.
func Who(ctx context.Context, c Caller) Identity {
	var i Identity
	_ = get(ctx, c, "/api/auth/me", &i)
	return i
}

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
		return NewAs(c, Permissions(r.Context(), c), version, Who(r.Context(), c))
	}, &mcp.StreamableHTTPOptions{Stateless: true, JSONResponse: true})
}

// Serve runs an MCP server acting through c on transport t (stdio, for
// `brokoli mcp`) until the client disconnects or ctx ends. The caller's
// permissions are read once, when it starts.
func Serve(ctx context.Context, c Caller, version string, t mcp.Transport) error {
	return NewAs(c, Permissions(ctx, c), version, Who(ctx, c)).Run(ctx, t)
}
