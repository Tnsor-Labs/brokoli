package mcpserver

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestRedactHidesCredentialsAndKeepsTheRest(t *testing.T) {
	in := map[string]any{
		"name":          "orders",
		"webhook_token": "whk-1",
		"config": map[string]any{
			"Password":      "hunter2",
			"apiKey":        "k-123",
			"client_secret": "s",
			"secret_ref":    "{{ secret.db }}", // names a secret, holds none
			"token_env":     "${SHOP_TOKEN}",
			"has_password":  true, // a flag holds nothing
			"private_key":   "",   // nothing to hide
			"headers":       map[string]any{"Authorization": "Basic dXNlcjpwYXNz", "Accept": "text/csv"},
			"dsn":           "mysql://app:s3cret@db:3306/x",
			"note":          "call with Bearer abcdef0123456789 please",
			"list":          []any{map[string]any{"cookie": "c=1"}, "https://u:p@host/x"},
			"timeout":       30.0,
		},
	}
	out := redact(in).(map[string]any)
	data, _ := json.Marshal(out)
	text := string(data)
	for _, leaked := range []string{"whk-1", "hunter2", "k-123", `"client_secret":"s"`, "dXNlcjpwYXNz", "s3cret", "abcdef0123456789", "c=1", "u:p@"} {
		if strings.Contains(text, leaked) {
			t.Errorf("leaks %q: %s", leaked, text)
		}
	}
	for _, kept := range []string{`"name":"orders"`, `{{ secret.db }}`, `${SHOP_TOKEN}`, `"has_password":true`, `"private_key":""`, `"Accept":"text/csv"`,
		"mysql://app:[redacted]@db:3306/x", "call with Bearer [redacted] please", "https://u:[redacted]@host/x", `"timeout":30`} {
		if !strings.Contains(text, kept) {
			t.Errorf("lacks %q: %s", kept, text)
		}
	}
	if in["webhook_token"] != "whk-1" {
		t.Error("the input was modified")
	}
}

func TestNeighbourhood(t *testing.T) {
	nodes := []lineageNode{
		{ID: "api:orders", Type: "api", Name: "orders API"},
		{ID: "table:orders", Type: "table", Name: "orders"},
		{ID: "table:revenue", Type: "table", Name: "revenue"},
		{ID: "p1:fetch", Type: "processing", Name: "Fetch", PipelineID: "p1"},
		{ID: "p2:sum", Type: "processing", Name: "Sum", PipelineID: "p2"},
		{ID: "p0:load", Type: "processing", Name: "Load", PipelineID: "p0"},
	}
	edges := []lineageEdge{
		{From: "p0:load", To: "api:orders", PipelineID: "p0", Pipeline: "loader"},
		{From: "api:orders", To: "p1:fetch", PipelineID: "p1", Pipeline: "orders"},
		{From: "p1:fetch", To: "table:orders", PipelineID: "p1", Pipeline: "orders"},
		{From: "table:orders", To: "p2:sum", PipelineID: "p2", Pipeline: "revenue"},
		{From: "p2:sum", To: "table:revenue", PipelineID: "p2", Pipeline: "revenue"},
	}
	got, _ := json.Marshal(neighbourhood("p1", nodes, edges))
	want := `{"downstream":[{"pipeline_id":"p2","pipeline":"revenue","via":"orders"}],` +
		`"reads":[{"id":"api:orders","type":"api","name":"orders API"}],` +
		`"upstream":[{"pipeline_id":"p0","pipeline":"loader","via":"orders API"}],` +
		`"writes":[{"id":"table:orders","type":"table","name":"orders"}]}`
	if string(got) != want {
		t.Fatalf("got  %s\nwant %s", got, want)
	}
}

func TestPipelinePageReadsBothShapes(t *testing.T) {
	all := `[{"id":"a","name":"A"},{"id":"b","name":"B"},{"id":"c","name":"C"}]`
	items, next, err := pipelinePage(json.RawMessage(all), "", 2)
	if err != nil || len(items) != 2 || next != "2" {
		t.Fatalf("first page: %v %q %v", items, next, err)
	}
	items, next, _ = pipelinePage(json.RawMessage(all), next, 2)
	if len(items) != 1 || items[0].ID != "c" || next != "" {
		t.Fatalf("second page: %v %q", items, next)
	}
	if items, _, _ := pipelinePage(json.RawMessage(all), "9", 2); len(items) != 0 {
		t.Fatalf("past the end: %v", items)
	}
	paged := `{"items":[{"id":"a","name":"A"}],"has_next":true,"cursor":"a"}`
	if items, next, _ := pipelinePage(json.RawMessage(paged), "", 1); len(items) != 1 || next != "a" {
		t.Fatalf("a page: %v %q", items, next)
	}
	last := `{"items":[],"has_next":false,"cursor":"a"}`
	if _, next, _ := pipelinePage(json.RawMessage(last), "", 1); next != "" {
		t.Fatalf("the last page has a next cursor %q", next)
	}
	if _, _, err := pipelinePage(json.RawMessage(`"x"`), "", 1); err == nil {
		t.Fatal("nonsense was read")
	}
}

func TestRouterCallerForwardsOnlyIdentityAndNeverReachesMCP(t *testing.T) {
	var seen http.Header
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = r.Header.Clone()
		if r.Context().Value(ctxKey{}) != nil {
			t.Error("the outer request's context values reached the sub-request")
		}
		w.WriteHeader(http.StatusTeapot)
		_, _ = w.Write([]byte(`{"error":"short and stout"}`))
	})
	outer := httptest.NewRequest(http.MethodPost, "/api/mcp", nil)
	outer = outer.WithContext(context.WithValue(outer.Context(), ctxKey{}, "claims"))
	for k, v := range map[string]string{"Authorization": "Bearer t", "X-Workspace-ID": "ws", "Mcp-Session-Id": "s", "X-Forwarded-For": "1.2.3.4"} {
		outer.Header.Set(k, v)
	}
	c := NewRouterCaller(h, outer)
	status, data, err := c.Do(outer.Context(), http.MethodGet, "/api/runs/x", nil)
	if err != nil || status != http.StatusTeapot || !strings.Contains(string(data), "stout") {
		t.Fatalf("Do = %d %s %v", status, data, err)
	}
	if seen.Get("Authorization") != "Bearer t" || seen.Get("X-Workspace-ID") != "ws" || seen.Get("Mcp-Session-Id") != "" || seen.Get("X-Forwarded-For") != "" {
		t.Fatalf("headers = %v", seen)
	}
	for _, path := range []string{"/api/mcp", "/health", "/"} {
		if _, _, err := c.Do(context.Background(), http.MethodGet, path, nil); err == nil {
			t.Errorf("%s was called", path)
		}
	}
	err = call(context.Background(), c, http.MethodGet, "/api/x", nil, nil)
	if e, ok := err.(*APIError); !ok || e.Status != http.StatusTeapot || !strings.Contains(e.Error(), "short and stout") {
		t.Fatalf("error = %v", err)
	}
}

type ctxKey struct{}

func TestHTTPCallerSendsTheTokenAndWorkspace(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer tok" || r.Header.Get("X-Workspace-ID") != "ws" || r.URL.Path != "/api/pipelines" {
			http.Error(w, `{"error":"who are you"}`, http.StatusUnauthorized)
			return
		}
		_, _ = w.Write([]byte(`[]`))
	}))
	defer srv.Close()
	c := &HTTPCaller{Server: srv.URL + "/", Token: "tok", Workspace: "ws"}
	if status, _, err := c.Do(context.Background(), http.MethodGet, "/api/pipelines", nil); err != nil || status != http.StatusOK {
		t.Fatalf("Do = %d %v", status, err)
	}
	err := get(context.Background(), &HTTPCaller{Server: srv.URL}, "/api/pipelines", nil)
	if e, ok := err.(*APIError); !ok || !strings.HasPrefix(e.Error(), "not signed in to Brokoli: who are you") {
		t.Fatalf("error = %v", err)
	}
}

func TestPermissions(t *testing.T) {
	answer := func(status int, body string) Caller {
		return callerFunc(func() (int, []byte, error) { return status, []byte(body), nil })
	}
	held := Permissions(context.Background(), answer(200, `["runs.view"]`))
	if !held("runs.view") || held("pipelines.view") {
		t.Fatal("held permissions are not as answered")
	}
	// No accounts on the server: no permissions endpoint, everything holds.
	if !Permissions(context.Background(), answer(404, ``))("anything") {
		t.Fatal("open server")
	}
	for _, c := range []Caller{answer(401, `{}`), answer(200, `nonsense`)} {
		if Permissions(context.Background(), c)("runs.view") {
			t.Fatal("a failed answer granted a permission")
		}
	}
}

type callerFunc func() (int, []byte, error)

func (f callerFunc) Do(context.Context, string, string, any) (int, []byte, error) { return f() }

// fakeAPI answers paths with canned bodies and records what was asked.
type fakeAPI struct {
	answers map[string]string
	asked   []string
}

func (f *fakeAPI) Do(_ context.Context, _ string, path string, _ any) (int, []byte, error) {
	f.asked = append(f.asked, path)
	for prefix, body := range f.answers {
		if strings.HasPrefix(path, prefix) {
			return http.StatusOK, []byte(body), nil
		}
	}
	return http.StatusNotFound, []byte(`{"error":"no such thing"}`), nil
}

func session(t *testing.T, c Caller) *mcp.ClientSession {
	t.Helper()
	st, ct := mcp.NewInMemoryTransports()
	ctx := context.Background()
	go func() { _ = New(c, nil, "test").Run(ctx, st) }()
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "t", Version: "1"}, nil).Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cs.Close() })
	return cs
}

func toolText(t *testing.T, cs *mcp.ClientSession, name string, args map[string]any) string {
	t.Helper()
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatal(err)
	}
	return res.Content[0].(*mcp.TextContent).Text
}

func TestPagesAreBounded(t *testing.T) {
	api := &fakeAPI{answers: map[string]string{"/api/pipelines/p/runs": `{"items":[],"has_next":false}`}}
	cs := session(t, api)
	toolText(t, cs, "list_runs", map[string]any{"pipeline": "p", "limit": 5000})
	toolText(t, cs, "list_runs", map[string]any{"pipeline": "p"})
	if len(api.asked) != 2 || !strings.Contains(api.asked[0], "limit=100") || !strings.Contains(api.asked[1], "limit=50") {
		t.Fatalf("asked %v", api.asked)
	}
}

func TestLogWindows(t *testing.T) {
	api := &fakeAPI{answers: map[string]string{"/api/runs/r/logs": `[{"message":"a"},{"message":"b"},{"message":"c"}]`}}
	cs := session(t, api)
	for _, c := range []struct {
		args map[string]any
		want string
	}{
		{map[string]any{"run": "r", "offset": 99}, `{"lines":[],`},
		{map[string]any{"run": "r", "offset": -4, "limit": 1}, `"message":"a"`},
		{map[string]any{"run": "r", "limit": 5000}, `"offset":0,"total":3`},
	} {
		if got := toolText(t, cs, "get_run_logs", c.args); !strings.Contains(got, c.want) {
			t.Errorf("%v: %s", c.args, got)
		}
	}
}

func TestExplainNamesOnlyTheFailingNodesAndTheLatestEarlierSuccess(t *testing.T) {
	api := &fakeAPI{answers: map[string]string{
		"/api/runs/r3/logs": `[]`,
		"/api/runs/r3": `{"id":"r3","pipeline_id":"p","status":"failed","started_at":"2026-10-20T10:00:00Z","pipeline_version":4,
			"node_runs":[{"node_id":"a","status":"success"},{"node_id":"b","status":"failed","error":"boom"},{"node_id":"c","status":"skipped"}]}`,
		"/api/pipelines/p/runs": `{"items":[
			{"id":"r9","status":"success","started_at":"2026-10-20T11:00:00Z","pipeline_version":4},
			{"id":"r1","status":"success","started_at":"2026-10-20T08:00:00Z","pipeline_version":2},
			{"id":"r3","status":"failed","started_at":"2026-10-20T10:00:00Z","pipeline_version":4},
			{"id":"r2","status":"success","started_at":"2026-10-20T09:00:00Z","pipeline_version":3},
			{"id":"r0","status":"failed","started_at":"2026-10-20T09:30:00Z","pipeline_version":3}]}`,
		"/api/pipelines/p/versions": `[{"version":4,"message":"four"},{"version":3,"message":"three"},{"version":2,"message":"two"}]`,
		"/api/pipelines/p":          `{"name":"P","nodes":[{"id":"b","name":"B","type":"transform"}]}`,
	}}
	ex, err := explainRun(context.Background(), api, "r3")
	if err != nil {
		t.Fatal(err)
	}
	if len(ex.Failures) != 1 || ex.Failures[0].Node != "b" || ex.Failures[0].Name != "B" {
		t.Fatalf("failures = %+v", ex.Failures)
	}
	// r9 is later, r0 failed, r1 is older than r2.
	if ex.LastGood == nil || ex.LastGood.ID != "r2" {
		t.Fatalf("last good = %+v", ex.LastGood)
	}
	if len(ex.Changes) != 1 || ex.Changes[0].Message != "four" {
		t.Fatalf("changes = %+v", ex.Changes)
	}
	if !strings.Contains(ex.Summary, `Node "B" (b, transform) failed: boom.`) {
		t.Fatalf("summary = %s", ex.Summary)
	}
}
