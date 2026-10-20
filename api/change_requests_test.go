package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Tnsor-Labs/brokoli/engine"
	"github.com/Tnsor-Labs/brokoli/models"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Change requests (ADR-046 phase 3), through the real server.

func ordersPipeline() *models.Pipeline {
	return &models.Pipeline{
		ID: "p-orders", PipelineID: "orders", Name: "orders", Enabled: true, WorkspaceID: models.DefaultWorkspaceID,
		Schedule: "0 * * * *",
		Nodes: []models.Node{
			{ID: "fetch", Type: "source_api", Name: "Fetch", Position: models.Position{X: 40, Y: 60}, Config: map[string]interface{}{"url": "https://shop.example.com/v1/orders", "timeout": 15}},
			{ID: "clean", Type: "transform", Name: "Clean", Config: map[string]interface{}{"rules": []interface{}{}}},
		},
		Edges: []models.Edge{{From: "fetch", To: "clean"}},
	}
}

// seedPipeline creates p as an edit would: saved, with its first version.
func (b *agentBed) seedPipeline(p *models.Pipeline) {
	b.t.Helper()
	body, _ := json.Marshal(p)
	if code, _, raw := b.do(http.MethodPost, "/api/pipelines", b.tokens["ada"], string(body)); code != http.StatusCreated && code != http.StatusOK {
		b.t.Fatalf("create pipeline: %d %s", code, raw)
	}
}

func (b *agentBed) pipelineID(name string) string {
	b.t.Helper()
	_, _, raw := b.do(http.MethodGet, "/api/pipelines", b.tokens["ada"], "")
	var list []map[string]any
	if json.Unmarshal([]byte(raw), &list) != nil {
		var page struct{ Items []map[string]any }
		_ = json.Unmarshal([]byte(raw), &page)
		list = page.Items
	}
	for _, p := range list {
		if p["name"] == name {
			return p["id"].(string)
		}
	}
	b.t.Fatalf("no pipeline %s in %s", name, raw)
	return ""
}

// edited is the pipeline as stored, changed by edit.
func (b *agentBed) edited(id string, edit func(p *models.Pipeline)) []byte {
	b.t.Helper()
	p, err := b.s.GetPipeline(id)
	if err != nil {
		b.t.Fatal(err)
	}
	edit(p)
	data, _ := json.Marshal(p)
	return data
}

func (b *agentBed) propose(token, id string, def []byte, title string) (int, map[string]any, string) {
	b.t.Helper()
	body, _ := json.Marshal(map[string]any{"pipeline": json.RawMessage(def), "title": title, "description": "the shop moved its API"})
	return b.do(http.MethodPost, "/api/pipelines/"+id+"/changes", token, string(body))
}

func TestAnAgentProposesAndAPersonApplies(t *testing.T) {
	b := newAgentBed(t)
	b.seedPipeline(ordersPipeline())
	id := b.pipelineID("orders")
	_, agent := b.agent("triage-bot", "", "ada", "ada", true)

	def := b.edited(id, func(p *models.Pipeline) {
		p.Nodes[0].Config["url"] = "https://shop.example.com/v2/orders"
		p.Nodes[0].Position = models.Position{} // sent without its layout
		p.Nodes = append(p.Nodes, models.Node{ID: "save", Type: "sink_file", Name: "Save", Config: map[string]interface{}{"path": "/tmp/o.csv", "format": "csv"}})
		p.Edges = append(p.Edges, models.Edge{From: "clean", To: "save"})
		p.Schedule = "*/30 * * * *"
		p.ID, p.Source = "something-else", "git" // identity is the pipeline's, whatever is sent
	})
	code, cr, raw := b.propose(agent, id, def, "Fetch orders from the v2 endpoint")
	if code != http.StatusCreated {
		t.Fatalf("propose: %d %s", code, raw)
	}
	crID := cr["id"].(string)
	for _, want := range []string{
		`"status":"open"`, `"stale":false`, `"valid":true`, `"created_by_kind":"agent"`, `"created_by_name":"triage-bot"`, `"on_behalf_of":"ada"`,
		`{"field":"schedule","before":"0 * * * *","after":"*/30 * * * *"}`,
		`"nodes_added":[{"id":"save","name":"Save","type":"sink_file"}]`,
		`{"field":"config.url","before":"https://shop.example.com/v1/orders","after":"https://shop.example.com/v2/orders"}`,
		`"edges_added":[{"from":"clean","to":"save"}]`,
	} {
		if !strings.Contains(raw, want) {
			t.Errorf("change request lacks %s:\n%s", want, raw)
		}
	}
	// Nothing that runs changed.
	if p, _ := b.s.GetPipeline(id); p.Schedule != "0 * * * *" || len(p.Nodes) != 2 {
		t.Fatalf("the pipeline changed on proposing: %+v", p)
	}

	// Listed, without the whole definition.
	_, _, list := b.do(http.MethodGet, "/api/pipelines/"+id+"/changes?status=open", b.tokens["vic"], "")
	if !strings.Contains(list, crID) || strings.Contains(list, `"proposed"`) {
		t.Fatalf("list = %s", list)
	}

	// The agent cannot apply, nor a viewer; an editor can.
	if code, _, _ := b.do(http.MethodPost, "/api/pipelines/"+id+"/changes/"+crID+"/apply", agent, ""); code != http.StatusForbidden {
		t.Fatalf("the agent applied: %d", code)
	}
	if code, _, _ := b.do(http.MethodPost, "/api/pipelines/"+id+"/changes/"+crID+"/apply", b.tokens["vic"], ""); code != http.StatusForbidden {
		t.Fatalf("a viewer applied: %d", code)
	}
	code, res, raw := b.do(http.MethodPost, "/api/pipelines/"+id+"/changes/"+crID+"/apply", b.tokens["eve"], "")
	if code != http.StatusOK {
		t.Fatalf("apply: %d %s", code, raw)
	}
	p, _ := b.s.GetPipeline(id)
	if p.Schedule != "*/30 * * * *" || len(p.Nodes) != 3 || p.ID != id || p.Source == "git" {
		t.Fatalf("applied pipeline = %+v", p)
	}
	// The canvas layout survives a proposal made without it (as over MCP).
	if p.Nodes[0].Position != (models.Position{X: 40, Y: 60}) {
		t.Fatalf("fetch moved to %+v", p.Nodes[0].Position)
	}
	versions, _ := b.s.ListPipelineVersions(id)
	latest := versions[0]
	for _, v := range versions {
		if v.Version > latest.Version {
			latest = v
		}
	}
	if int(res["version"].(float64)) != latest.Version || latest.Message != "change request: Fetch orders from the v2 endpoint" {
		t.Fatalf("version %v, latest %+v", res["version"], latest)
	}
	_, got, raw := b.do(http.MethodGet, "/api/pipelines/"+id+"/changes/"+crID, b.tokens["ada"], "")
	if got["status"] != ChangeApplied || got["decided_by_name"] != "eve" || int(got["applied_version"].(float64)) != latest.Version {
		t.Fatalf("applied request = %s", raw)
	}
	// Once.
	if code, _, _ := b.do(http.MethodPost, "/api/pipelines/"+id+"/changes/"+crID+"/apply", b.tokens["eve"], ""); code != http.StatusConflict {
		t.Fatalf("applied twice: %d", code)
	}
}

func TestAStaleChangeRequestCannotBeApplied(t *testing.T) {
	b := newAgentBed(t)
	b.seedPipeline(ordersPipeline())
	id := b.pipelineID("orders")
	_, cr, raw := b.propose(b.tokens["eve"], id, b.edited(id, func(p *models.Pipeline) { p.Schedule = "*/5 * * * *" }), "Every five minutes")
	if cr == nil || cr["id"] == nil {
		t.Fatalf("propose: %s", raw)
	}
	crID := cr["id"].(string)
	// Someone edits the pipeline meanwhile.
	if code, _, raw := b.do(http.MethodPut, "/api/pipelines/"+id, b.tokens["ada"], string(b.edited(id, func(p *models.Pipeline) { p.Description = "edited" }))); code != http.StatusOK {
		t.Fatalf("edit: %d %s", code, raw)
	}
	_, _, list := b.do(http.MethodGet, "/api/pipelines/"+id+"/changes", b.tokens["ada"], "")
	if !strings.Contains(list, `"stale":true`) {
		t.Fatalf("not stale: %s", list)
	}
	code, _, raw := b.do(http.MethodPost, "/api/pipelines/"+id+"/changes/"+crID+"/apply", b.tokens["ada"], "")
	if code != http.StatusConflict || !strings.Contains(raw, "has changed since") {
		t.Fatalf("a stale apply: %d %s", code, raw)
	}
	if p, _ := b.s.GetPipeline(id); p.Description != "edited" || p.Schedule != "0 * * * *" {
		t.Fatalf("the stale request was applied: %+v", p)
	}
	// Declined, with the reason; then settled.
	if code, _, _ := b.do(http.MethodPost, "/api/pipelines/"+id+"/changes/"+crID+"/decline", b.tokens["ada"], `{"reason":"stale"}`); code != http.StatusNoContent {
		t.Fatalf("decline: %d", code)
	}
	_, got, _ := b.do(http.MethodGet, "/api/pipelines/"+id+"/changes/"+crID, b.tokens["ada"], "")
	if got["status"] != ChangeDeclined || got["reason"] != "stale" || got["stale"] != false {
		t.Fatalf("declined = %v", got)
	}
	if code, _, _ := b.do(http.MethodPost, "/api/pipelines/"+id+"/changes/"+crID+"/decline", b.tokens["ada"], ``); code != http.StatusConflict {
		t.Fatalf("declined twice: %d", code)
	}
}

func TestWhatAChangeRequestRefuses(t *testing.T) {
	b := newAgentBed(t)
	b.seedPipeline(ordersPipeline())
	id := b.pipelineID("orders")
	_, agent := b.agent("bot", "", "ada", "ada", false)

	// A viewer cannot propose; the same definition is nothing to propose;
	// a title is needed.
	same := b.edited(id, func(*models.Pipeline) {})
	if code, _, _ := b.propose(b.tokens["vic"], id, b.edited(id, func(p *models.Pipeline) { p.Schedule = "" }), "x"); code != http.StatusForbidden {
		t.Fatalf("a viewer proposed: %d", code)
	}
	if code, _, raw := b.propose(agent, id, same, "nothing"); code != http.StatusUnprocessableEntity {
		t.Fatalf("an empty change: %d %s", code, raw)
	}
	if code, _, _ := b.propose(agent, id, b.edited(id, func(p *models.Pipeline) { p.Schedule = "" }), " "); code != http.StatusBadRequest {
		t.Fatalf("no title: %d", code)
	}

	// An invalid proposal is recorded with why, and cannot be applied.
	_, cr, raw := b.propose(agent, id, b.edited(id, func(p *models.Pipeline) { p.Edges = append(p.Edges, models.Edge{From: "clean", To: "fetch"}) }), "A cycle")
	if cr["validation"].(map[string]any)["valid"] != false {
		t.Fatalf("a cycle is valid: %s", raw)
	}
	if code, _, raw := b.do(http.MethodPost, "/api/pipelines/"+id+"/changes/"+cr["id"].(string)+"/apply", b.tokens["ada"], ""); code != http.StatusBadRequest {
		t.Fatalf("an invalid request applied: %d %s", code, raw)
	}

	// The author withdraws their own; someone else without edit cannot.
	_, cr, _ = b.propose(agent, id, b.edited(id, func(p *models.Pipeline) { p.Description = "x" }), "Describe it")
	path := "/api/pipelines/" + id + "/changes/" + cr["id"].(string) + "/decline"
	if code, _, _ := b.do(http.MethodPost, path, b.tokens["vic"], ""); code != http.StatusForbidden {
		t.Fatalf("a viewer declined: %d", code)
	}
	if code, _, _ := b.do(http.MethodPost, path, agent, ""); code != http.StatusNoContent {
		t.Fatalf("the author withdrew: %d", code)
	}
	// Another pipeline's request is not found here.
	if code, _, _ := b.do(http.MethodGet, "/api/pipelines/nope/changes/"+cr["id"].(string), b.tokens["ada"], ""); code != http.StatusNotFound {
		t.Fatalf("a missing pipeline: %d", code)
	}
}

// A pipeline deployed from a repository takes change requests only where
// a distribution delivers them, and is never changed by applying one.
func TestChangeRequestsForARepositoryPipeline(t *testing.T) {
	b := newAgentBed(t)
	p := ordersPipeline()
	p.ID, p.Name, p.Source = "p-git", "from-git", models.PipelineSourceGit
	if err := b.s.CreatePipeline(p); err != nil {
		t.Fatal(err)
	}
	if _, err := b.s.SavePipelineVersion(p.ID, "{}", "deployed"); err != nil {
		t.Fatal(err)
	}
	def := b.edited(p.ID, func(p *models.Pipeline) { p.Schedule = "*/5 * * * *" })
	if code, _, raw := b.propose(b.tokens["eve"], p.ID, def, "Faster"); code != http.StatusConflict || !strings.Contains(raw, "repository") {
		t.Fatalf("no deliverer: %d %s", code, raw)
	}

	prev := ChangeRequestDeliverFunc
	t.Cleanup(func() { ChangeRequestDeliverFunc = prev })
	var delivered *ChangeRequest
	ChangeRequestDeliverFunc = func(_ *http.Request, cr *ChangeRequest, _ *models.Pipeline) (*ChangeDelivery, error) {
		delivered = cr
		return &ChangeDelivery{Kind: "pull_request", URL: "https://example.com/pr/7", Ref: "7"}, nil
	}
	code, cr, raw := b.propose(b.tokens["eve"], p.ID, def, "Faster")
	if code != http.StatusCreated || delivered == nil || delivered.Proposed.Schedule != "*/5 * * * *" || !strings.Contains(raw, `"url":"https://example.com/pr/7"`) {
		t.Fatalf("delivered: %d %s", code, raw)
	}
	crID := cr["id"].(string)
	if code, _, raw := b.do(http.MethodPost, "/api/pipelines/"+p.ID+"/changes/"+crID+"/apply", b.tokens["ada"], ""); code != http.StatusConflict || !strings.Contains(raw, "merging it there") {
		t.Fatalf("applied in place: %d %s", code, raw)
	}
	if err := SetChangeRequestOutcome(crID, true, "ada on GitHub", ""); err != nil {
		t.Fatal(err)
	}
	_, got, _ := b.do(http.MethodGet, "/api/pipelines/"+p.ID+"/changes/"+crID, b.tokens["ada"], "")
	if got["status"] != ChangeApplied || got["decided_by_name"] != "ada on GitHub" {
		t.Fatalf("outcome = %v", got)
	}

	// A delivery that fails declines the request and says why.
	ChangeRequestDeliverFunc = func(*http.Request, *ChangeRequest, *models.Pipeline) (*ChangeDelivery, error) {
		return nil, errors.New("GitHub refused")
	}
	if code, _, raw := b.propose(b.tokens["eve"], p.ID, def, "Again"); code != http.StatusBadGateway || !strings.Contains(raw, "GitHub refused") {
		t.Fatalf("failed delivery: %d %s", code, raw)
	}
}

// Over MCP, propose_change is how an agent changes a pipeline.
func TestAnAgentProposesOverMCP(t *testing.T) {
	b := newAgentBed(t)
	b.seedPipeline(ordersPipeline())
	id := b.pipelineID("orders")
	_, agent := b.agent("triage-bot", "", "ada", "ada", false)
	hs := httptest.NewServer(b.srv.router)
	defer hs.Close()
	cs, err := mcpSession(t, hs.URL, agent)
	if err != nil {
		t.Fatal(err)
	}
	defer cs.Close()
	var def map[string]any
	_ = json.Unmarshal(b.edited(id, func(p *models.Pipeline) { p.Schedule = "*/10 * * * *" }), &def)
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "propose_change", Arguments: map[string]any{
		"pipeline": id, "definition": def, "title": "Every ten minutes", "description": "the shop updates more often",
	}})
	if err != nil || res.IsError {
		t.Fatalf("propose_change: %v %+v", err, res)
	}
	text := res.Content[0].(*mcp.TextContent).Text
	if !strings.Contains(text, `"status":"open"`) || strings.Contains(text, `"proposed"`) || !strings.Contains(text, `"after":"*/10 * * * *"`) {
		t.Fatalf("propose_change = %s", text)
	}
	if p, _ := b.s.GetPipeline(id); p.Schedule != "0 * * * *" {
		t.Fatal("proposing changed the pipeline")
	}
}

// The guards that other checks usually shadow, each on its own.
func TestChangeRequestGuardsHoldOnTheirOwn(t *testing.T) {
	b := newAgentBed(t)
	b.seedPipeline(ordersPipeline())
	id := b.pipelineID("orders")

	// An agent holding pipelines.edit (role editor) still cannot apply.
	_, editorAgent := b.agent("editor-bot", "editor", "ada", "ada", false)
	_, cr, raw := b.propose(editorAgent, id, b.edited(id, func(p *models.Pipeline) {
		p.Schedule = "*/15 * * * *"
		p.ID = "not-this-pipeline"
	}), "Quarter hour")
	if cr == nil || cr["id"] == nil {
		t.Fatalf("propose: %s", raw)
	}
	crID := cr["id"].(string)
	if code, _, raw := b.do(http.MethodPost, "/api/pipelines/"+id+"/changes/"+crID+"/apply", editorAgent, ""); code != http.StatusForbidden || !strings.Contains(raw, "an agent cannot apply") {
		t.Fatalf("an editor agent applied: %d %s", code, raw)
	}
	// The proposal is this pipeline's, whatever id it was sent with.
	_, got, _ := b.do(http.MethodGet, "/api/pipelines/"+id+"/changes/"+crID, b.tokens["ada"], "")
	if got["proposed"].(map[string]any)["id"] != id {
		t.Fatalf("proposed id = %v", got["proposed"].(map[string]any)["id"])
	}

	// A declined request is not applied, and the pipeline is untouched.
	if code, _, _ := b.do(http.MethodPost, "/api/pipelines/"+id+"/changes/"+crID+"/decline", b.tokens["ada"], ""); code != http.StatusNoContent {
		t.Fatalf("decline: %d", code)
	}
	if code, _, _ := b.do(http.MethodPost, "/api/pipelines/"+id+"/changes/"+crID+"/apply", b.tokens["ada"], ""); code != http.StatusConflict {
		t.Fatalf("a declined request applied: %d", code)
	}
	if p, _ := b.s.GetPipeline(id); p.Schedule != "0 * * * *" {
		t.Fatalf("a declined request changed the pipeline: %s", p.Schedule)
	}

	// Empty and absent are the same: no change to propose.
	if code, _, raw := b.propose(b.tokens["eve"], id, b.edited(id, func(p *models.Pipeline) {
		// Stored without params or tags; sent with empty ones.
		if len(p.Params) == 0 {
			p.Params = map[string]string{}
		} else {
			p.Params = nil
		}
		if len(p.Tags) == 0 {
			p.Tags = []string{}
		} else {
			p.Tags = nil
		}
	}), "Nothing really"); code != http.StatusUnprocessableEntity {
		t.Fatalf("empty versus absent: %d %s", code, raw)
	}
}

func TestADeliveryThatFailsDeclinesTheRequest(t *testing.T) {
	b := newAgentBed(t)
	p := ordersPipeline()
	p.ID, p.Name, p.Source = "p-git", "from-git", models.PipelineSourceGit
	if err := b.s.CreatePipeline(p); err != nil {
		t.Fatal(err)
	}
	prev := ChangeRequestDeliverFunc
	t.Cleanup(func() { ChangeRequestDeliverFunc = prev })
	ChangeRequestDeliverFunc = func(*http.Request, *ChangeRequest, *models.Pipeline) (*ChangeDelivery, error) {
		return nil, errors.New("GitHub refused")
	}
	b.propose(b.tokens["eve"], p.ID, b.edited(p.ID, func(p *models.Pipeline) { p.Schedule = "" }), "No schedule")
	_, _, list := b.do(http.MethodGet, "/api/pipelines/"+p.ID+"/changes", b.tokens["ada"], "")
	if !strings.Contains(list, `"status":"declined"`) || !strings.Contains(list, "could not be delivered: GitHub refused") {
		t.Fatalf("list = %s", list)
	}
}

// Another organization's pipeline is not there at all.
func TestChangeRequestsStayInTheirOrganization(t *testing.T) {
	prev := OrgResolverFunc
	OrgResolverFunc = func(string) string { return "org-a" }
	t.Cleanup(func() { OrgResolverFunc = prev })
	b := newAgentBed(t) // tokens now carry org-a
	p := ordersPipeline()
	p.OrgID = "org-b"
	if err := b.s.CreatePipeline(p); err != nil {
		t.Fatal(err)
	}
	def := b.edited(p.ID, func(p *models.Pipeline) { p.Schedule = "" })
	if code, _, raw := b.propose(b.tokens["ada"], p.ID, def, "Across"); code != http.StatusNotFound {
		t.Fatalf("proposed to another organization's pipeline: %d %s", code, raw)
	}
	if code, _, _ := b.do(http.MethodGet, "/api/pipelines/"+p.ID+"/changes", b.tokens["ada"], ""); code != http.StatusNotFound {
		t.Fatalf("listed another organization's requests: %d", code)
	}
}

// A person tests a change request: the current and proposed pipelines are
// previewed side by side, nothing is written, no run is recorded, and an
// agent cannot start it.
func TestTestingAChangeRequest(t *testing.T) {
	b := newAgentBed(t)
	dir := t.TempDir()
	in, out := filepath.Join(dir, "orders.csv"), filepath.Join(dir, "out.csv")
	if err := os.WriteFile(in, []byte("id,price,qty\n1,10,2\n2,20,3\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	b.seedPipeline(&models.Pipeline{
		ID: "p-calc", Name: "calc", Enabled: true, WorkspaceID: models.DefaultWorkspaceID,
		Nodes: []models.Node{
			{ID: "src", Type: "source_file", Name: "Orders", Config: map[string]interface{}{"path": in, "format": "csv"}},
			{ID: "tf", Type: "transform", Name: "Totals", Config: map[string]interface{}{"rules": []interface{}{}}},
			{ID: "dst", Type: "sink_file", Name: "Save", Config: map[string]interface{}{"path": out, "format": "csv"}},
		},
		Edges: []models.Edge{{From: "src", To: "tf"}, {From: "tf", To: "dst"}},
	})
	id := b.pipelineID("calc")
	_, agent := b.agent("triage-bot", "", "ada", "ada", true)
	def := b.edited(id, func(p *models.Pipeline) {
		p.Nodes[1].Config["rules"] = []interface{}{map[string]interface{}{"type": "add_column", "name": "total", "expression": "price * qty"}}
	})
	code, cr, raw := b.propose(agent, id, def, "Add the order total")
	if code != http.StatusCreated {
		t.Fatalf("propose: %d %s", code, raw)
	}
	path := "/api/pipelines/" + id + "/changes/" + cr["id"].(string) + "/test"

	if code, _, raw := b.do(http.MethodPost, path, agent, ""); code != http.StatusForbidden || !strings.Contains(raw, "a person") {
		t.Fatalf("an agent tested its change: %d %s", code, raw)
	}
	if code, _, _ := b.do(http.MethodPost, path, b.tokens["vic"], ""); code != http.StatusForbidden {
		t.Fatalf("a viewer tested a change: %d", code)
	}
	code, _, raw = b.do(http.MethodPost, path, b.tokens["eve"], `{"rows":5}`)
	if code != http.StatusOK {
		t.Fatalf("test: %d %s", code, raw)
	}
	var res struct {
		Current, Proposed engine.Preview
	}
	if err := json.Unmarshal([]byte(raw), &res); err != nil {
		t.Fatal(err)
	}
	node := func(pv engine.Preview, id string) engine.PreviewNode {
		for _, n := range pv.Nodes {
			if n.NodeID == id {
				return n
			}
		}
		t.Fatalf("no node %s in %+v", id, pv)
		return engine.PreviewNode{}
	}
	if tf := node(res.Current, "tf"); len(tf.Rows) != 2 || tf.Rows[0]["total"] != nil {
		t.Fatalf("current totals = %+v", tf)
	}
	if tf := node(res.Proposed, "tf"); len(tf.Rows) != 2 || tf.Rows[0]["total"] == nil {
		t.Fatalf("proposed totals = %+v", tf)
	}
	if d := node(res.Proposed, "dst"); d.Status != engine.PreviewHeld {
		t.Fatalf("sink = %+v, want held", d)
	}
	if _, err := os.Stat(out); !os.IsNotExist(err) {
		t.Fatal("testing a change wrote the sink")
	}
	if runs, _ := b.s.ListRunsByPipeline(id, 10); len(runs) != 0 {
		t.Fatalf("testing a change left %d runs", len(runs))
	}
	if p, _ := b.s.GetPipeline(id); len(p.Nodes[1].Config["rules"].([]interface{})) != 0 {
		t.Fatal("testing a change changed the pipeline")
	}
}

// The workspace's change requests, across its pipelines: what a person has
// to review, with each pipeline's name, and nothing from another
// organization.
func TestTheWorkspacesChangeRequests(t *testing.T) {
	prev := OrgResolverFunc
	OrgResolverFunc = func(string) string { return "org-a" }
	t.Cleanup(func() { OrgResolverFunc = prev })
	b := newAgentBed(t)
	mine, theirs, other := ordersPipeline(), ordersPipeline(), ordersPipeline()
	mine.OrgID = "org-a"
	other.ID, other.PipelineID, other.Name, other.OrgID = "p-other", "other", "other", "org-a"
	theirs.ID, theirs.PipelineID, theirs.Name, theirs.OrgID = "p-theirs", "theirs", "theirs", "org-b"
	for _, p := range []*models.Pipeline{mine, other, theirs} {
		if err := b.s.CreatePipeline(p); err != nil {
			t.Fatal(err)
		}
	}
	add := func(id, pipeline, status string) {
		t.Helper()
		cr := &ChangeRequest{ID: id, PipelineID: pipeline, Title: id, Status: status, Proposed: mine,
			Validation: ChangeValidation{Valid: true, Errors: []string{}}, CreatedByKind: UserKindAgent,
			CreatedByName: "triage-bot", CreatedAt: time.Now().UTC()}
		if err := changeRequests.create(cr); err != nil {
			t.Fatal(err)
		}
	}
	add("cr-mine", mine.ID, ChangeOpen)
	add("cr-other-declined", other.ID, ChangeDeclined)
	add("cr-theirs", theirs.ID, ChangeOpen)

	code, _, raw := b.do(http.MethodGet, "/api/changes?status=open", b.tokens["ada"], "")
	var list []map[string]any
	if code != http.StatusOK || json.Unmarshal([]byte(raw), &list) != nil {
		t.Fatalf("list: %d %s", code, raw)
	}
	if len(list) != 1 || list[0]["id"] != "cr-mine" || list[0]["pipeline_name"] != "orders" || list[0]["proposed"] != nil {
		t.Fatalf("open changes = %s", raw)
	}
	_, _, raw = b.do(http.MethodGet, "/api/changes", b.tokens["ada"], "")
	if !strings.Contains(raw, "cr-other-declined") || strings.Contains(raw, "cr-theirs") {
		t.Fatalf("all changes = %s", raw)
	}
	if code, _, _ := b.do(http.MethodGet, "/api/changes?status=nope", b.tokens["ada"], ""); code != http.StatusBadRequest {
		t.Fatalf("a bad status: %d", code)
	}
}
