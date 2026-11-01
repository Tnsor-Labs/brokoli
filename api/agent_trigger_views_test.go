package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Tnsor-Labs/brokoli/models"
)

func TestTheEventTypesAreListedWithWhatTheyMean(t *testing.T) {
	b := newDispatchBed(t)
	list := func() []TriggerEventType {
		t.Helper()
		rec := serve(b.srv, http.MethodGet, "/api/agents/trigger-events", b.tokens["vic"], "")
		var out []TriggerEventType
		if err := json.Unmarshal(rec.Body.Bytes(), &out); rec.Code != http.StatusOK || err != nil {
			t.Fatalf("list: %d %s", rec.Code, rec.Body.String())
		}
		return out
	}
	got := list()
	if len(got) != 3 || got[0].Name != "run.failed" || got[1].Name != "run.blocked" || got[2].Name != "run.recovered" {
		t.Fatalf("event types = %+v, want the three built-in ones in order", got)
	}
	for _, e := range got {
		if e.Description == "" {
			t.Errorf("%s has no description", e.Name)
		}
	}

	// A distribution's own event type is listed, and a trigger may take it.
	before := TriggerEventTypes()
	t.Cleanup(func() { triggerEventsMu.Lock(); triggerEventTypes = before; triggerEventsMu.Unlock() })
	RegisterTriggerEvent(TriggerEventType{Name: "pipeline.deadline_missed", Description: "A pipeline did not finish by its deadline."})
	if got := list(); len(got) != 4 || got[3].Name != "pipeline.deadline_missed" {
		t.Fatalf("after registering: %+v", got)
	}
	b.pipelineWithNodes("orders", models.DefaultWorkspaceID)
	b.trigger(`{"events":["pipeline.deadline_missed","run.failed"],"url":"` + hookURL + `"}`)
	code, _, raw := b.do(http.MethodPost, "/api/agents/"+b.agentID+"/triggers", b.tokens["eve"], `{"events":["pipeline.nope"],"url":"`+hookURL+`"}`)
	if code != http.StatusBadRequest || !strings.Contains(raw, "pipeline.deadline_missed") {
		t.Errorf("an unknown event: %d %s, want 400 naming every event, the added one too", code, raw)
	}

	for name, bad := range map[string]TriggerEventType{
		"taken":          {Name: "run.failed", Description: "again"},
		"not dotted":     {Name: "deadline", Description: "x"},
		"upper case":     {Name: "Run.Late", Description: "x"},
		"no description": {Name: "run.late"},
	} {
		func() {
			defer func() {
				if recover() == nil {
					t.Errorf("%s: registered, want a panic", name)
				}
			}()
			RegisterTriggerEvent(bad)
		}()
	}
}

// The run page names the agents told about a run, and what each then
// proposed: the first change request it made to the pipeline after.
func TestARunShowsTheAgentsToldAboutIt(t *testing.T) {
	b := newDispatchBed(t)
	rc := newReceiver(t)
	b.pipelineWithNodes("orders", models.DefaultWorkspaceID)
	b.trigger(`{"events":["run.failed"],"url":"` + rc.srv.URL + `","cooldown_seconds":0}`)
	quiet := b.runEvent("orders", models.RunEventTerminal, models.RunStatusSuccess, "")
	propose := func(id, by, kind string, at time.Time) {
		t.Helper()
		if err := changeRequests.create(&ChangeRequest{ID: id, PipelineID: "orders", Title: id, Status: "open",
			CreatedBy: by, CreatedByName: by, CreatedByKind: kind, CreatedAt: at}); err != nil {
			t.Fatal(err)
		}
	}
	propose("cr-before", b.agentID, "agent", time.Now().UTC().Add(-time.Hour)) // made before it was told
	failed := b.runEvent("orders", models.RunEventTerminal, models.RunStatusFailed, "boom")
	b.pass()
	propose("cr-person", b.ids["eve"], "person", time.Now().UTC().Add(time.Minute))
	propose("cr-other-agent", "another-agent-id", "agent", time.Now().UTC().Add(90*time.Second))
	propose("cr-fix", b.agentID, "agent", time.Now().UTC().Add(2*time.Minute))
	propose("cr-later", b.agentID, "agent", time.Now().UTC().Add(3*time.Minute))

	notices := func(run, token string) (int, []RunNotice, string) {
		rec := serve(b.srv, http.MethodGet, "/api/runs/"+run+"/agents", token, "")
		var out []RunNotice
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
		return rec.Code, out, rec.Body.String()
	}
	code, got, raw := notices(failed, b.tokens["vic"]) // a viewer sees it
	if code != http.StatusOK || len(got) != 1 {
		t.Fatalf("notices: %d %s, want one", code, raw)
	}
	n := got[0]
	if n.AgentID != b.agentID || n.AgentName != "triage-bot" || n.Event != "run.failed" || n.Status != DeliveryDelivered || n.FinishedAt == nil {
		t.Errorf("notice = %+v", n)
	}
	if n.Proposal == nil || n.Proposal.ID != "cr-fix" || n.Proposal.Title != "cr-fix" || n.Proposal.Status != "open" {
		t.Errorf("proposal = %+v, want cr-fix: the agent's first after it was told", n.Proposal)
	}
	if strings.Contains(raw, rc.srv.URL) {
		t.Error("the notices show the webhook URL")
	}

	if code, got, raw := notices(quiet, b.tokens["vic"]); code != http.StatusOK || len(got) != 0 || strings.TrimSpace(raw) != "[]" {
		t.Errorf("a run nobody was told about: %d %s, want []", code, raw)
	}
	if code, _, _ := notices("run-nope", b.tokens["vic"]); code != http.StatusNotFound {
		t.Errorf("an unknown run: %d", code)
	}
}

// Where there are workspaces, a run's notices and a pipeline's triggers
// are seen only from the workspace they are in, by the check every other
// pipeline read makes. Community has one tenant, so the check passes there.
func TestTriggerViewsStayInTheirWorkspace(t *testing.T) {
	b := newDispatchBed(t)
	multiTenant(t, map[string][]string{"alice": {wsAlpha}})
	seedPipeline(t, b.s, "alpha-p", wsAlpha)
	seedPipeline(t, b.s, "beta-p", wsBeta)
	foreign := seedPipeline(t, b.s, "foreign-p", wsAlpha)
	foreign.OrgID = "org-other"
	if err := b.s.UpdatePipeline(foreign); err != nil {
		t.Fatal(err)
	}
	runIn := func(pipeline string) string {
		rid := "run-" + generateID()[:10]
		if err := b.s.CreateRun(&models.Run{ID: rid, PipelineID: pipeline, Status: models.RunStatusFailed}); err != nil {
			t.Fatal(err)
		}
		return rid
	}
	h := &triggersAPI{agents: &agentsAPI{users: b.users, pc: NewPermissionChecker(b.s)}, ts: b.triggerStore()}
	for _, c := range []struct {
		name    string
		handler http.HandlerFunc
		id      string
		want    int
	}{
		{"notices, own workspace", h.runNotices, runIn("alpha-p"), http.StatusOK},
		{"notices, another workspace", h.runNotices, runIn("beta-p"), http.StatusNotFound},
		{"triggers, own workspace", h.pipelineTriggers, "alpha-p", http.StatusOK},
		{"triggers, another workspace", h.pipelineTriggers, "beta-p", http.StatusNotFound},
		{"notices, another organization", h.runNotices, runIn("foreign-p"), http.StatusNotFound},
		{"triggers, another organization", h.pipelineTriggers, "foreign-p", http.StatusNotFound},
	} {
		rec := httptest.NewRecorder()
		c.handler(rec, request(http.MethodGet, "/", c.id, "alice", wsAlpha, nil))
		if rec.Code != c.want {
			t.Errorf("%s: %d %s, want %d", c.name, rec.Code, rec.Body.String(), c.want)
		}
	}
}

func TestAPipelineShowsTheTriggersThatWatchIt(t *testing.T) {
	b := newDispatchBed(t)
	b.pipelineWithNodes("orders", models.DefaultWorkspaceID)
	b.pipelineWithNodes("billing", models.DefaultWorkspaceID)
	mine, _ := b.trigger(`{"events":["run.failed"],"url":"` + hookURL + `","pipeline_id":"orders"}`)
	everywhere, _ := b.trigger(`{"events":["run.failed","run.recovered"],"url":"` + hookURL + `"}`)
	b.trigger(`{"events":["run.failed"],"url":"` + hookURL + `","pipeline_id":"billing"}`)
	if code, _, raw := b.do(http.MethodPatch, "/api/agents/"+b.agentID+"/triggers/"+everywhere, b.tokens["eve"], `{"enabled":false}`); code != http.StatusOK {
		t.Fatalf("disable: %d %s", code, raw)
	}

	list := func(token string) []PipelineTrigger {
		t.Helper()
		rec := serve(b.srv, http.MethodGet, "/api/pipelines/orders/triggers", token, "")
		var out []PipelineTrigger
		if err := json.Unmarshal(rec.Body.Bytes(), &out); rec.Code != http.StatusOK || err != nil {
			t.Fatalf("list: %d %s", rec.Code, rec.Body.String())
		}
		return out
	}
	for _, who := range []string{"eve", "ada"} { // the owner, and whoever manages every agent
		got := list(b.tokens[who])
		if len(got) != 2 || got[0].ID != mine || got[1].ID != everywhere {
			t.Fatalf("%s: %+v, want the orders trigger and the workspace one", who, got)
		}
		if got[0].Scope != "pipeline" || got[1].Scope != "workspace" || got[1].Enabled || got[0].AgentName != "triage-bot" {
			t.Errorf("%s: %+v", who, got)
		}
		if got[0].URL != hookURL || !got[0].MayManage {
			t.Errorf("%s may manage the agent, but sees url %q may_manage %v", who, got[0].URL, got[0].MayManage)
		}
	}
	got := list(b.tokens["vic"])
	if len(got) != 2 {
		t.Fatalf("vic: %+v", got)
	}
	for _, tr := range got {
		if tr.URL != "" || tr.MayManage {
			t.Errorf("vic may not manage the agent, but sees %+v", tr)
		}
	}
}

// A distribution scopes agents to a tenant (AgentHooks.Visible). An agent
// the caller may not know of is left out of a run's notices and a
// pipeline's triggers, even when the run or pipeline is the caller's.
func TestTriggerViewsLeaveOutAgentsTheCallerMayNotSee(t *testing.T) {
	b := newDispatchBed(t)
	rc := newReceiver(t)
	b.pipelineWithNodes("orders", models.DefaultWorkspaceID)
	b.trigger(`{"events":["run.failed"],"url":"` + rc.srv.URL + `","cooldown_seconds":0}`)
	failed := b.runEvent("orders", models.RunEventTerminal, models.RunStatusFailed, "boom")
	b.pass()

	count := func(path string) int {
		t.Helper()
		rec := serve(b.srv, http.MethodGet, path, b.tokens["vic"], "")
		var out []map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &out); rec.Code != http.StatusOK || err != nil {
			t.Fatalf("%s: %d %s", path, rec.Code, rec.Body.String())
		}
		return len(out)
	}
	if count("/api/runs/"+failed+"/agents") != 1 || count("/api/pipelines/orders/triggers") != 1 {
		t.Fatal("before hiding the agent, each view should list it once")
	}
	prev := AgentHooks.Visible
	t.Cleanup(func() { AgentHooks.Visible = prev })
	AgentHooks.Visible = func(*http.Request, *Agent) bool { return false }
	if n := count("/api/runs/" + failed + "/agents"); n != 0 {
		t.Errorf("run notices list %d agents the caller may not see", n)
	}
	if n := count("/api/pipelines/orders/triggers"); n != 0 {
		t.Errorf("pipeline triggers list %d triggers of agents the caller may not see", n)
	}
}
