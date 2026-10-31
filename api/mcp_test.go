package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Tnsor-Labs/brokoli/engine"
	"github.com/Tnsor-Labs/brokoli/models"
	"github.com/Tnsor-Labs/brokoli/pkg/mcpserver"
	"github.com/Tnsor-Labs/brokoli/store"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// MCP (ADR-046) through the real server: the client is the official SDK's,
// over HTTP, against the server's own router and middleware.

type bearerTransport struct{ token string }

func (b bearerTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	if b.token != "" {
		r.Header.Set("Authorization", "Bearer "+b.token)
	}
	return http.DefaultTransport.RoundTrip(r)
}

func mcpSession(t *testing.T, url, token string) (*mcp.ClientSession, error) {
	t.Helper()
	client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "1"}, nil)
	return client.Connect(context.Background(), &mcp.StreamableClientTransport{
		Endpoint: url + "/api/mcp", HTTPClient: &http.Client{Transport: bearerTransport{token}}, MaxRetries: -1,
	}, nil)
}

func callTool(t *testing.T, cs *mcp.ClientSession, name string, args map[string]any) (map[string]any, string) {
	t.Helper()
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	text := ""
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			text += tc.Text
		}
	}
	if res.IsError {
		return nil, text
	}
	var out map[string]any
	if err := json.Unmarshal([]byte(text), &out); err != nil {
		t.Fatalf("%s: not JSON: %v\n%s", name, err, text)
	}
	return out, text
}

func mcpBed(t *testing.T) (url string, tokens map[string]string, s store.Store) {
	t.Helper()
	sq, err := store.NewSQLiteStore(filepath.Join(t.TempDir(), "mcp.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { sq.Close() })
	users := newTestUserStore(t)
	tokens = map[string]string{}
	for name, role := range map[string]Role{"ada": RoleAdmin, "vic": RoleViewer} {
		u, err := users.CreateUser(name, "Str0ng!Passw0rd", role)
		if err != nil {
			t.Fatal(err)
		}
		if tokens[name], err = GenerateToken(u); err != nil {
			t.Fatal(err)
		}
	}
	// A role that may see pipelines but not runs.
	if err := sq.CreateRole(&models.Role{ID: "catalog", Name: "catalog", Permissions: []models.Permission{models.PermPipelinesView}}); err != nil {
		t.Fatal(err)
	}
	u, err := users.CreateUser("cat", "Str0ng!Passw0rd", Role("catalog"))
	if err != nil {
		t.Fatal(err)
	}
	if tokens["cat"], err = GenerateToken(u); err != nil {
		t.Fatal(err)
	}
	srv := NewServer(0, sq, engine.NewEngine(sq), nil, nil, users, nil, nil)
	t.Cleanup(func() { close(srv.evictDone) })
	hs := httptest.NewServer(srv.router)
	t.Cleanup(hs.Close)
	return hs.URL, tokens, sq
}

// seedOrders is a pipeline with credentials in it, a successful run on its
// first version and a failed run on its second.
func seedOrders(t *testing.T, s store.Store) *models.Pipeline {
	t.Helper()
	p := &models.Pipeline{
		ID: "p-orders", PipelineID: "orders", Name: "Orders", Enabled: true, WorkspaceID: models.DefaultWorkspaceID, WebhookToken: "whk-very-secret",
		Nodes: []models.Node{
			{ID: "fetch", Type: "source_api", Name: "Fetch orders", Position: models.Position{X: 10, Y: 20}, Config: map[string]interface{}{
				"url":      "https://shop.example.com/v2/orders",
				"headers":  map[string]interface{}{"Authorization": "Bearer abcdefghijklmnop", "Accept": "application/json"},
				"password": "hunter2",
				"dsn":      "postgres://shop:pa55word@db.example.com/shop",
				"api_key":  "{{ secret.shop_key }}",
			}},
			{ID: "clean", Type: "transform", Name: "Clean", Config: map[string]interface{}{}},
		},
		Edges: []models.Edge{{From: "fetch", To: "clean"}},
	}
	if err := s.CreatePipeline(p); err != nil {
		t.Fatal(err)
	}
	for _, msg := range []string{"first version", "fetch from the v2 endpoint"} {
		if _, err := s.SavePipelineVersion(p.ID, "{}", msg); err != nil {
			t.Fatal(err)
		}
	}
	at := func(m int) *time.Time { v := time.Date(2026, 10, 20, 6, m, 0, 0, time.UTC); return &v }
	runs := []*models.Run{
		{ID: "r-good", PipelineID: p.ID, Status: models.RunStatusSuccess, StartedAt: at(0), FinishedAt: at(1), PipelineVersion: 1},
		{ID: "r-bad", PipelineID: p.ID, Status: models.RunStatusFailed, StartedAt: at(10), FinishedAt: at(11), PipelineVersion: 2,
			Error: "node fetch: GET https://shop.example.com/v2/orders: 404 Not Found"},
	}
	for _, r := range runs {
		if err := s.CreateRun(r); err != nil {
			t.Fatal(err)
		}
	}
	for _, nr := range []*models.NodeRun{
		{ID: "nr-1", RunID: "r-bad", NodeID: "fetch", Status: models.RunStatusFailed, Attempt: 2, StartedAt: at(10),
			Error: "GET https://shop.example.com/v2/orders: 404 Not Found\nresponse: {\"error\":\"no such route\"}"},
		{ID: "nr-2", RunID: "r-bad", NodeID: "clean", Status: models.RunStatusSkipped},
	} {
		if err := s.CreateNodeRun(nr); err != nil {
			t.Fatal(err)
		}
	}
	for i, l := range []models.LogEntry{
		{NodeID: "fetch", Level: models.LogLevelInfo, Message: "GET https://shop.example.com/v2/orders with Authorization: Bearer abcdefghijklmnop"},
		{NodeID: "fetch", Level: models.LogLevelWarning, Message: "attempt 1 failed, retrying"},
		{NodeID: "fetch", Level: models.LogLevelError, Message: "request failed: 404 Not Found"},
		{NodeID: "clean", Level: models.LogLevelInfo, Message: "skipped: upstream failed"},
	} {
		l.RunID, l.Timestamp = "r-bad", time.Date(2026, 10, 20, 6, 10, i, 0, time.UTC)
		if err := s.AppendLog(&l); err != nil {
			t.Fatal(err)
		}
	}
	return p
}

func toolNames(t *testing.T, cs *mcp.ClientSession) []string {
	t.Helper()
	res, err := cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	names := []string{}
	for _, tl := range res.Tools { // listed in name order
		names = append(names, tl.Name)
		// Proposing, running and cancelling act (ADR-046: marked so a
		// client asks the person first); reading the trigger feed
		// acknowledges what was read (ADR-047), which changes the agent's
		// own queue and nothing else; everything else only reads.
		acts := tl.Name == "propose_change" || tl.Name == "trigger_run" || tl.Name == "cancel_run" || tl.Name == "next_events"
		if tl.Annotations == nil || tl.Annotations.ReadOnlyHint == acts {
			t.Errorf("%s is marked read only: %v", tl.Name, tl.Annotations != nil && tl.Annotations.ReadOnlyHint)
		}
	}
	return names
}

func TestMCPServesTheRuntimeToAgentsThroughTheAPI(t *testing.T) {
	url, tokens, s := mcpBed(t)
	seedOrders(t, s)

	// Not signed in: no session at all.
	if _, err := mcpSession(t, url, ""); err == nil {
		t.Fatal("connected without credentials")
	}

	cs, err := mcpSession(t, url, tokens["ada"])
	if err != nil {
		t.Fatal(err)
	}
	defer cs.Close()
	want := "cancel_run dry_run_pipeline explain_run get_lineage get_pipeline get_run get_run_logs list_change_requests list_pipelines list_runs next_events propose_change trigger_run validate_pipeline"
	if got := toolNames(t, cs); strings.Join(got, " ") != want {
		t.Fatalf("tools = %v", got)
	}

	// The list carries no webhook token.
	list, text := callTool(t, cs, "list_pipelines", nil)
	if strings.Contains(text, "whk-very-secret") || len(list["pipelines"].([]any)) != 1 {
		t.Fatalf("list_pipelines = %s", text)
	}

	// The definition, with every credential redacted and references kept.
	_, text = callTool(t, cs, "get_pipeline", map[string]any{"pipeline": "p-orders"})
	for _, leaked := range []string{"hunter2", "abcdefghijklmnop", "pa55word", "whk-very-secret"} {
		if strings.Contains(text, leaked) {
			t.Errorf("get_pipeline leaks %q:\n%s", leaked, text)
		}
	}
	for _, kept := range []string{"{{ secret.shop_key }}", "postgres://shop:[redacted]@db.example.com/shop", `"Accept":"application/json"`, "https://shop.example.com/v2/orders"} {
		if !strings.Contains(text, kept) {
			t.Errorf("get_pipeline lacks %q:\n%s", kept, text)
		}
	}
	if strings.Contains(text, `"position"`) {
		t.Errorf("positions are in the definition:\n%s", text)
	}

	// Logs: the last lines by default, a window with an offset; bearer
	// credentials in a line are redacted.
	logs, text := callTool(t, cs, "get_run_logs", map[string]any{"run": "r-bad", "limit": 2})
	if logs["total"].(float64) != 4 || logs["offset"].(float64) != 2 || logs["more"] == nil {
		t.Fatalf("get_run_logs = %s", text)
	}
	_, text = callTool(t, cs, "get_run_logs", map[string]any{"run": "r-bad", "offset": 0, "limit": 1})
	if !strings.Contains(text, "Bearer [redacted]") || strings.Contains(text, "abcdefghijklmnop") {
		t.Fatalf("first line = %s", text)
	}

	// The explanation: the failing node with its error and logs, the last
	// good run, and what changed since.
	ex, text := callTool(t, cs, "explain_run", map[string]any{"run": "r-bad"})
	summary, _ := ex["summary"].(string)
	for _, want := range []string{
		`Node "Fetch orders" (fetch, source_api) failed after 2 retries: GET https://shop.example.com/v2/orders: 404 Not Found.`,
		"The last successful run was r-good (version 1); the pipeline changed 1 time(s) since.",
	} {
		if !strings.Contains(summary, want) {
			t.Errorf("summary lacks %q:\n%s", want, summary)
		}
	}
	if !strings.Contains(text, "fetch from the v2 endpoint") || !strings.Contains(text, "request failed: 404 Not Found") {
		t.Errorf("explain_run lacks the change or the error line:\n%s", text)
	}

	// Validation without saving.
	v, text := callTool(t, cs, "validate_pipeline", map[string]any{"definition": map[string]any{"name": "x", "nodes": []any{
		map[string]any{"id": "a", "type": "no_such_type", "name": "A", "config": map[string]any{}}}, "edges": []any{}}})
	if v["valid"] != false {
		t.Fatalf("validate_pipeline = %s", text)
	}

	// Lineage, runs, a run: answered, and errors say what is wrong.
	if _, text := callTool(t, cs, "get_run", map[string]any{"run": "nope"}); !strings.Contains(text, "not found") {
		t.Fatalf("a missing run: %s", text)
	}
	if runs, text := callTool(t, cs, "list_runs", map[string]any{"pipeline": "p-orders"}); len(runs["runs"].([]any)) != 2 {
		t.Fatalf("list_runs = %s", text)
	}
	if _, text := callTool(t, cs, "get_lineage", map[string]any{"pipeline": "p-orders"}); !strings.Contains(text, `"reads"`) {
		t.Fatalf("get_lineage = %s", text)
	}
}

// Tools are listed by the caller's permissions: a role that cannot see runs
// is offered no run tools.
func TestMCPListsOnlyTheToolsTheCallerMayUse(t *testing.T) {
	url, tokens, _ := mcpBed(t)
	for who, want := range map[string]string{
		"vic": "explain_run get_lineage get_pipeline get_run get_run_logs list_change_requests list_pipelines list_runs next_events validate_pipeline",
		"cat": "get_lineage get_pipeline list_change_requests list_pipelines validate_pipeline",
	} {
		cs, err := mcpSession(t, url, tokens[who])
		if err != nil {
			t.Fatalf("%s: %v", who, err)
		}
		if got := strings.Join(toolNames(t, cs), " "); got != want {
			t.Errorf("%s: tools = %s", who, got)
		}
		if who == "cat" {
			if _, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "get_run", Arguments: map[string]any{"run": "x"}}); err == nil {
				t.Error("cat called a tool it is not offered")
			}
		}
		cs.Close()
	}
}

// `brokoli mcp`: the same tools over stdio, acting on a server over HTTP.
func TestMCPOverStdioActsOnAServerAsTheSignedInUser(t *testing.T) {
	url, tokens, s := mcpBed(t)
	seedOrders(t, s)
	serverSide, clientSide := mcp.NewInMemoryTransports()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- mcpserver.Serve(ctx, &mcpserver.HTTPCaller{Server: url, Token: tokens["cat"]}, "test", serverSide)
	}()
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "1"}, nil).Connect(ctx, clientSide, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(toolNames(t, cs), " "); got != "get_lineage get_pipeline list_change_requests list_pipelines validate_pipeline" {
		t.Fatalf("tools = %s", got)
	}
	if p, text := callTool(t, cs, "get_pipeline", map[string]any{"pipeline": "p-orders"}); p["name"] != "Orders" || strings.Contains(text, "hunter2") {
		t.Fatalf("get_pipeline = %s", text)
	}
	cs.Close()
	if err := <-done; err != nil && ctx.Err() == nil && !strings.Contains(err.Error(), "closed") {
		t.Fatalf("serve: %v", err)
	}
}
