package api

import (
	"encoding/json"
	"net/http/httptest"
	"testing"

	"github.com/Tnsor-Labs/brokoli/models"
	"github.com/Tnsor-Labs/brokoli/store"
)

// Listing pipelines branched on the organization and then ignored the
// workspace, so every workspace in an organization showed the same
// pipelines and the dashboard counted them all. The owner found it by
// creating a second workspace and seeing his three pipelines in it.
//
// These tests seed two workspaces in ONE organization, because a filter
// that drops the workspace is invisible until a second workspace exists.
// The helpers come from pipeline_workspace_access_test.go, which covers
// the by-id half of the same boundary.

// listResponse pulls the summaries out of either shape List answers with:
// a CursorResult in an organization, a bare array in community mode.
func listResponse(t *testing.T, body []byte) []PipelineSummary {
	t.Helper()
	var cursor struct {
		Items []PipelineSummary `json:"items"`
	}
	if err := json.Unmarshal(body, &cursor); err == nil && cursor.Items != nil {
		return cursor.Items
	}
	var flat []PipelineSummary
	if err := json.Unmarshal(body, &flat); err != nil {
		t.Fatalf("unmarshal list response: %v; body=%s", err, body)
	}
	return flat
}

func listedIDs(summaries []PipelineSummary) map[string]bool {
	ids := make(map[string]bool, len(summaries))
	for _, s := range summaries {
		ids[s.ID] = true
	}
	return ids
}

// listPipelines calls the handler as userID, filed under currentWS.
func listPipelines(t *testing.T, s store.Store, userID, currentWS string) []PipelineSummary {
	t.Helper()
	h := NewPipelineHandler(s, nil)
	rec := httptest.NewRecorder()
	h.List(rec, request("GET", "/api/pipelines", "", userID, currentWS, nil))
	if rec.Code != 200 {
		t.Fatalf("List status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	return listResponse(t, rec.Body.Bytes())
}

func TestPipelineList_ShowsOnlyTheCurrentWorkspace(t *testing.T) {
	multiTenant(t, map[string][]string{"alice": {wsAlpha, wsBeta}})
	s := newWorkspaceAccessStore(t)
	seedPipeline(t, s, "in-alpha", wsAlpha)
	seedPipeline(t, s, "in-beta", wsBeta)

	got := listedIDs(listPipelines(t, s, "alice", wsAlpha))
	if !got["in-alpha"] {
		t.Errorf("alpha's own pipeline missing from its list: %v", got)
	}
	if got["in-beta"] {
		t.Errorf("a pipeline from the sibling workspace is listed under alpha: %v; "+
			"listing by org alone is what made every workspace look the same", got)
	}
	if len(got) != 1 {
		t.Errorf("listed %d pipelines, want 1", len(got))
	}
}

func TestPipelineList_SwitchingWorkspaceChangesTheList(t *testing.T) {
	multiTenant(t, map[string][]string{"alice": {wsAlpha, wsBeta}})
	s := newWorkspaceAccessStore(t)
	seedPipeline(t, s, "in-alpha", wsAlpha)
	seedPipeline(t, s, "in-beta", wsBeta)

	alpha := listedIDs(listPipelines(t, s, "alice", wsAlpha))
	beta := listedIDs(listPipelines(t, s, "alice", wsBeta))
	if !alpha["in-alpha"] || alpha["in-beta"] {
		t.Errorf("alpha listed %v, want only in-alpha", alpha)
	}
	if !beta["in-beta"] || beta["in-alpha"] {
		t.Errorf("beta listed %v, want only in-beta; the two workspaces must not agree", beta)
	}
}

// A session that has not named a workspace sends the default. Filtering on
// that literally would show an empty page while the person's work sits one
// header away, so the resolver's first owned workspace stands in.
func TestPipelineList_DefaultHeaderResolvesToAnOwnedWorkspace(t *testing.T) {
	multiTenant(t, map[string][]string{"alice": {wsBeta}})
	s := newWorkspaceAccessStore(t)
	seedPipeline(t, s, "in-beta", wsBeta)

	got := listedIDs(listPipelines(t, s, "alice", models.DefaultWorkspaceID))
	if !got["in-beta"] {
		t.Errorf("listed %v, want the caller's own workspace to stand in for the default header", got)
	}
}

func TestPipelineList_CallerOwningNoWorkspaceSeesNothing(t *testing.T) {
	multiTenant(t, map[string][]string{"alice": {wsAlpha}})
	s := newWorkspaceAccessStore(t)
	seedPipeline(t, s, "in-alpha", wsAlpha)

	got := listedIDs(listPipelines(t, s, "nobody", models.DefaultWorkspaceID))
	if len(got) != 0 {
		t.Errorf("listed %v for a member of no workspace, want nothing; "+
			"an empty membership must not fall through to the whole org", got)
	}
}

// The previous test passes even when a caller owning no workspace is
// scoped to "default", because nothing sits there. On a real instance
// "default" is a workspace like any other and holds rows, so the empty
// membership has to answer "nothing" rather than "whatever is in
// default". This seeds that row and pins the difference.
func TestPipelineList_CallerOwningNoWorkspaceDoesNotSeeTheDefaultWorkspace(t *testing.T) {
	multiTenant(t, map[string][]string{"alice": {wsAlpha}})
	s := newWorkspaceAccessStore(t)
	seedPipeline(t, s, "in-alpha", wsAlpha)
	seedPipeline(t, s, "in-default", models.DefaultWorkspaceID)

	got := listedIDs(listPipelines(t, s, "nobody", models.DefaultWorkspaceID))
	if len(got) != 0 {
		t.Errorf("listed %v for a member of no workspace, want nothing; "+
			"falling back to the default workspace shows rows that happen to sit there", got)
	}
}

// Enterprise installs the org resolver and the workspace resolver at two
// separate points, so they are independent globals rather than one switch.
// With org context set and no workspace resolver there is no owned set to
// consult, and the early return is what keeps effectiveWorkspace from
// calling a nil function value: without it this panics rather than
// answering.
func TestPipelineList_OrgWithoutWorkspaceResolverStillLists(t *testing.T) {
	origWS, origOrg := UserWorkspaceResolverFunc, OrgResolverFunc
	UserWorkspaceResolverFunc = nil
	OrgResolverFunc = func(userID string) string { return wsOrg }
	t.Cleanup(func() { UserWorkspaceResolverFunc, OrgResolverFunc = origWS, origOrg })

	s := newWorkspaceAccessStore(t)
	seedPipeline(t, s, "in-default", models.DefaultWorkspaceID)

	got := listedIDs(listPipelines(t, s, "alice", models.DefaultWorkspaceID))
	if !got["in-default"] {
		t.Errorf("listed %v, want the default workspace's pipeline when no workspace resolver is installed", got)
	}
}

// Community mode has no resolvers and one workspace. It must keep listing
// by workspace exactly as before.
func TestPipelineList_CommunityModeUnaffected(t *testing.T) {
	origWS, origOrg := UserWorkspaceResolverFunc, OrgResolverFunc
	UserWorkspaceResolverFunc, OrgResolverFunc = nil, nil
	t.Cleanup(func() { UserWorkspaceResolverFunc, OrgResolverFunc = origWS, origOrg })

	s := newWorkspaceAccessStore(t)
	p := &models.Pipeline{
		ID: "solo", PipelineID: "solo", Name: "solo",
		WorkspaceID: models.DefaultWorkspaceID, Enabled: true,
		Nodes: []models.Node{{ID: "src", Type: models.NodeTypeSourceAPI, Name: "src",
			Config: map[string]interface{}{"url": "/api/samples/data/employees.json", "method": "GET"}}},
		Edges: []models.Edge{},
	}
	if err := s.CreatePipeline(p); err != nil {
		t.Fatalf("CreatePipeline: %v", err)
	}

	h := NewPipelineHandler(s, nil)
	rec := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/api/pipelines", nil)
	h.List(rec, r)
	if rec.Code != 200 {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	if got := listedIDs(listResponse(t, rec.Body.Bytes())); !got["solo"] {
		t.Errorf("community mode listed %v, want the default workspace's pipeline", got)
	}
}

// The scoped cursor must page like the org one: id descending, one extra
// row deciding has_next, and the sibling workspace never appearing.
func TestPipelineList_CursorPagesWithinTheWorkspace(t *testing.T) {
	multiTenant(t, map[string][]string{"alice": {wsAlpha, wsBeta}})
	s := newWorkspaceAccessStore(t)
	for _, id := range []string{"a1", "a2", "a3"} {
		seedPipeline(t, s, id, wsAlpha)
	}
	seedPipeline(t, s, "b1", wsBeta)

	h := NewPipelineHandler(s, nil)
	rec := httptest.NewRecorder()
	h.List(rec, request("GET", "/api/pipelines?limit=2", "", "alice", wsAlpha, nil))
	if rec.Code != 200 {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var page struct {
		Items   []PipelineSummary `json:"items"`
		HasNext bool              `json:"has_next"`
		Cursor  string            `json:"cursor"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &page); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(page.Items) != 2 {
		t.Fatalf("page held %d items, want 2", len(page.Items))
	}
	if !page.HasNext {
		t.Error("has_next = false with a third pipeline in this workspace")
	}
	for _, item := range page.Items {
		if item.ID == "b1" {
			t.Error("the sibling workspace's pipeline appeared in a scoped page")
		}
	}

	rec2 := httptest.NewRecorder()
	h.List(rec2, request("GET", "/api/pipelines?limit=2&after="+page.Cursor, "", "alice", wsAlpha, nil))
	var second struct {
		Items   []PipelineSummary `json:"items"`
		HasNext bool              `json:"has_next"`
	}
	if err := json.Unmarshal(rec2.Body.Bytes(), &second); err != nil {
		t.Fatalf("unmarshal page 2: %v", err)
	}
	if len(second.Items) != 1 || second.HasNext {
		t.Errorf("page 2 held %d items (has_next=%v), want the last 1 and no more",
			len(second.Items), second.HasNext)
	}
	if second.Items[0].ID == "b1" {
		t.Error("the sibling workspace's pipeline appeared on page 2")
	}
}

// listPipelinesForRequest feeds several views, so it is scoped directly
// rather than only through a handler.
func TestListPipelinesForRequest_ScopedToTheWorkspace(t *testing.T) {
	multiTenant(t, map[string][]string{"alice": {wsAlpha, wsBeta}})
	s := newWorkspaceAccessStore(t)
	seedPipeline(t, s, "in-alpha", wsAlpha)
	seedPipeline(t, s, "in-beta", wsBeta)

	got, err := listPipelinesForRequest(s, request("GET", "/x", "", "alice", wsAlpha, nil))
	if err != nil {
		t.Fatalf("listPipelinesForRequest: %v", err)
	}
	if len(got) != 1 || got[0].ID != "in-alpha" {
		t.Errorf("returned %d pipelines %v, want only in-alpha", len(got), pipelineIDsOf(got))
	}
}

func TestListPipelinesForRequest_NoWorkspaceReturnsNothing(t *testing.T) {
	multiTenant(t, map[string][]string{"alice": {wsAlpha}})
	s := newWorkspaceAccessStore(t)
	seedPipeline(t, s, "in-alpha", wsAlpha)

	got, err := listPipelinesForRequest(s, request("GET", "/x", "", "nobody", models.DefaultWorkspaceID, nil))
	if err != nil {
		t.Fatalf("listPipelinesForRequest: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("returned %v for a member of no workspace, want nothing", pipelineIDsOf(got))
	}
}

func pipelineIDsOf(ps []models.Pipeline) []string {
	ids := make([]string, 0, len(ps))
	for _, p := range ps {
		ids = append(ids, p.ID)
	}
	return ids
}

// The dashboard derives every counter from the same read, so a wrong scope
// there is a wrong number rather than just a longer list.
func TestDashboard_CountsOnlyTheCurrentWorkspace(t *testing.T) {
	multiTenant(t, map[string][]string{"alice": {wsAlpha, wsBeta}})
	s := newWorkspaceAccessStore(t)
	seedPipeline(t, s, "in-alpha", wsAlpha)
	seedPipeline(t, s, "in-beta-1", wsBeta)
	seedPipeline(t, s, "in-beta-2", wsBeta)

	count := func(currentWS string) int {
		rec := httptest.NewRecorder()
		dashboardHandler(s)(rec, request("GET", "/api/dashboard", "", "alice", currentWS, nil))
		if rec.Code != 200 {
			t.Fatalf("dashboard status = %d, want 200; body=%s", rec.Code, rec.Body.String())
		}
		var body struct {
			Pipelines []struct {
				ID string `json:"id"`
			} `json:"pipelines"`
			TotalPipelines int `json:"total_pipelines"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatalf("unmarshal dashboard: %v; body=%s", err, rec.Body.String())
		}
		if body.TotalPipelines > 0 {
			return body.TotalPipelines
		}
		return len(body.Pipelines)
	}

	alpha, beta := count(wsAlpha), count(wsBeta)
	if alpha == beta {
		t.Fatalf("the dashboard reported the same figure (%d) for both workspaces; "+
			"one holds 1 pipeline and the other 2", alpha)
	}
	if alpha != 1 {
		t.Errorf("alpha's dashboard counted %d, want 1", alpha)
	}
	if beta != 2 {
		t.Errorf("beta's dashboard counted %d, want 2", beta)
	}
}
