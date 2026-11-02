package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Tnsor-Labs/brokoli/engine"
	"github.com/Tnsor-Labs/brokoli/models"
	"github.com/Tnsor-Labs/brokoli/pkg/tenancy"
	"github.com/Tnsor-Labs/brokoli/store"
)

// ADR-049 phase 3: acting on an alert by id. The actions checked the
// organization only, so a member of one workspace could dismiss, assign,
// acknowledge or resolve another workspace's alert.
func TestAlertActionsKeepToTheCallersWorkspaces(t *testing.T) {
	actions := []struct{ method, path, body string }{
		{http.MethodPost, "/read", ""},
		{http.MethodPost, "/assign", `{"user_id":"alice"}`},
		{http.MethodPost, "/acknowledge", ""},
		{http.MethodPost, "/resolve", ""},
		{http.MethodDelete, "", ""},
	}
	for _, a := range actions {
		w := newScopeWorld(t)
		if rec := w.do(t, a.method, "/api/alerts/"+w.alertB+a.path, "alice", wsAlpha, a.body); rec.Code != http.StatusNotFound {
			t.Errorf("alice (alpha only) %s alert%s of beta: %d, want 404", a.method, a.path, rec.Code)
		}
		if got := alertByID(t, w.store, w.alertB); got.ID == "" || got.AssigneeUserID != "" || got.AcknowledgedAt != nil || got.ResolvedAt != nil {
			t.Errorf("%s alert%s by alice changed beta's alert (dismissed when missing): %+v", a.method, a.path, got)
		}
		var reads int
		_ = w.store.(*store.SQLiteStore).RawDB().(*sql.DB).QueryRow(`SELECT COUNT(*) FROM alert_reads WHERE alert_id = ?`, w.alertB).Scan(&reads)
		if reads != 0 {
			t.Errorf("%s alert%s by alice marked beta's alert read", a.method, a.path)
		}
		if rec := w.do(t, a.method, "/api/alerts/"+w.alertB+a.path, "carol", wsAlpha, a.body); rec.Code != http.StatusNoContent {
			t.Errorf("carol (both) %s alert%s of beta from alpha: %d, want 204", a.method, a.path, rec.Code)
		}
	}
}

// Mark all read means the current workspace's alerts, not every
// workspace's in the organization.
func TestMarkAllReadIsTheCurrentWorkspaces(t *testing.T) {
	w := newScopeWorld(t)
	if rec := w.do(t, http.MethodPost, "/api/alerts/read-all", "carol", wsAlpha, ""); rec.Code != http.StatusNoContent {
		t.Fatalf("read-all: %d %s", rec.Code, rec.Body.String())
	}
	for ws, want := range map[string]int{wsAlpha: 0, wsBeta: 1} {
		n, err := store.Scoped(w.store, scopeFor(wsOrg, ws, []string{wsAlpha, wsBeta}, tenancy.ActorPerson)).CountUnreadAlerts("carol")
		if err != nil || n != want {
			t.Errorf("carol's unread in %s after read-all in alpha: %d (%v), want %d", ws, n, err, want)
		}
	}
}

func alertByID(t *testing.T, s store.Store, id string) models.Alert {
	t.Helper()
	all, err := s.QueryAlerts(store.AlertQuery{OrgID: wsOrg, Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range all {
		if a.ID == id {
			return a
		}
	}
	return models.Alert{}
}

// pipelineBody is an update body for pipeline id with these dependencies.
func pipelineBody(name string, dependsOn ...string) []byte {
	body, _ := json.Marshal(map[string]any{
		"name":       name,
		"enabled":    true,
		"depends_on": dependsOn,
		"nodes": []map[string]any{
			{"id": "src", "type": "source_api", "name": "src",
				"config": map[string]any{"url": "/api/samples/data/employees.json", "method": "GET"}},
		},
		"edges": []any{},
	})
	return body
}

func dependOn(t *testing.T, s store.Store, id string, upstream ...string) {
	t.Helper()
	p, err := s.GetPipeline(id)
	if err != nil {
		t.Fatal(err)
	}
	p.DependsOn = upstream
	if err := s.UpdatePipeline(p); err != nil {
		t.Fatal(err)
	}
}

// Deleting with resolve=cascade deleted every dependent in the
// organization, so a member of one workspace deleted another workspace's
// pipelines; the refusal without resolve named them.
func TestDeleteCascadeKeepsToTheCallersWorkspaces(t *testing.T) {
	multiTenant(t, map[string][]string{"alice": {wsAlpha}, "carol": {wsAlpha, wsBeta}})
	s := newWorkspaceAccessStore(t)
	seedPipeline(t, s, "upstream", wsAlpha)
	seedPipeline(t, s, "beta-secret-name", wsBeta)
	dependOn(t, s, "beta-secret-name", "upstream")
	h := NewPipelineHandler(s, nil)

	rec := httptest.NewRecorder()
	h.Delete(rec, request("DELETE", "/api/pipelines/upstream", "upstream", "alice", wsAlpha, nil))
	if rec.Code != http.StatusConflict || strings.Contains(rec.Body.String(), "beta-secret-name") || !strings.Contains(rec.Body.String(), `"dependents_elsewhere":1`) {
		t.Errorf("delete without resolve, alice: %d %s; want 409 counting one dependent elsewhere, not naming it", rec.Code, rec.Body.String())
	}

	rec = httptest.NewRecorder()
	h.Delete(rec, request("DELETE", "/api/pipelines/upstream?resolve=cascade", "upstream", "alice", wsAlpha, nil))
	if rec.Code != http.StatusConflict {
		t.Errorf("cascade by alice (alpha only): %d %s, want 409", rec.Code, rec.Body.String())
	}
	for _, id := range []string{"upstream", "beta-secret-name"} {
		if _, err := s.GetPipeline(id); err != nil {
			t.Errorf("alice's refused cascade deleted %s", id)
		}
	}

	rec = httptest.NewRecorder()
	h.Delete(rec, request("DELETE", "/api/pipelines/upstream?resolve=cascade", "upstream", "carol", wsAlpha, nil))
	if rec.Code != http.StatusOK && rec.Code != http.StatusNoContent {
		t.Fatalf("cascade by carol (both): %d %s", rec.Code, rec.Body.String())
	}
	if _, err := s.GetPipeline("beta-secret-name"); err == nil {
		t.Error("carol's cascade left the dependent in beta")
	}
}

// A new dependency must be on a pipeline the caller may see; one already
// saved does not block editing.
func TestNewDependenciesKeepToTheCallersWorkspaces(t *testing.T) {
	multiTenant(t, map[string][]string{"alice": {wsAlpha}, "carol": {wsAlpha, wsBeta}})
	s := newWorkspaceAccessStore(t)
	seedPipeline(t, s, "mine", wsAlpha)
	seedPipeline(t, s, "linked", wsAlpha)
	seedPipeline(t, s, "theirs", wsBeta)
	dependOn(t, s, "linked", "theirs")
	h := NewPipelineHandler(s, nil)

	rec := httptest.NewRecorder()
	h.Update(rec, request("PUT", "/api/pipelines/mine", "mine", "alice", wsAlpha, pipelineBody("mine", "theirs")))
	if rec.Code != http.StatusBadRequest {
		t.Errorf("alice adds a dependency on beta's pipeline: %d %s, want 400", rec.Code, rec.Body.String())
	}
	if p, _ := s.GetPipeline("mine"); len(p.DependsOn) != 0 {
		t.Errorf("refused update still saved dependencies %v", p.DependsOn)
	}

	rec = httptest.NewRecorder()
	h.Update(rec, request("PUT", "/api/pipelines/linked", "linked", "alice", wsAlpha, pipelineBody("linked renamed", "theirs")))
	if rec.Code != http.StatusOK {
		t.Errorf("alice edits a pipeline whose saved dependency is in beta: %d %s, want 200", rec.Code, rec.Body.String())
	}

	rec = httptest.NewRecorder()
	h.Update(rec, request("PUT", "/api/pipelines/mine", "mine", "carol", wsAlpha, pipelineBody("mine", "theirs")))
	if rec.Code != http.StatusOK {
		t.Errorf("carol (both) adds the dependency: %d %s, want 200", rec.Code, rec.Body.String())
	}
}

// The dependency view of a pipeline names its upstreams; one in a
// workspace the caller does not belong to keeps only its id.
func TestDependencyViewHidesOtherWorkspacesUpstreams(t *testing.T) {
	multiTenant(t, map[string][]string{"alice": {wsAlpha}, "carol": {wsAlpha, wsBeta}})
	s := newWorkspaceAccessStore(t)
	seedPipeline(t, s, "down", wsAlpha)
	up := seedPipeline(t, s, "up", wsBeta)
	up.Name = "beta-secret-name"
	if err := s.UpdatePipeline(up); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	if err := s.CreateRun(&models.Run{ID: "r1", PipelineID: "up", OrgID: wsOrg, Status: models.RunStatusFailed, StartedAt: &now}); err != nil {
		t.Fatal(err)
	}
	dependOn(t, s, "down", "up")

	for user, wantName := range map[string]bool{"alice": false, "carol": true} {
		rec := httptest.NewRecorder()
		pipelineDepsHandler(s)(rec, request("GET", "/api/pipelines/down/deps", "down", user, wsAlpha, nil))
		body := rec.Body.String()
		if rec.Code != http.StatusOK || strings.Contains(body, "beta-secret-name") != wantName || strings.Contains(body, `"last_status"`) != wantName {
			t.Errorf("deps of down for %s: %d %s; want upstream name and status shown=%v", user, rec.Code, body, wantName)
		}
	}
}

// The scheduler holds every pipeline on the server; its status lists only
// the request's.
func TestSchedulerStatusIsTheRequestsPipelines(t *testing.T) {
	multiTenant(t, map[string][]string{"alice": {wsAlpha}})
	s := newWorkspaceAccessStore(t)
	seedPipeline(t, s, "alpha-cron", wsAlpha)
	seedPipeline(t, s, "beta-cron", wsBeta)
	other := &models.Pipeline{ID: "other-org-cron", Name: "other-org-cron", OrgID: "org-other", WorkspaceID: "ws-other",
		Nodes: []models.Node{}, Edges: []models.Edge{}}
	if err := s.CreatePipeline(other); err != nil {
		t.Fatal(err)
	}
	eng := engine.NewEngine(s)
	t.Cleanup(func() { _ = eng.Close(context.Background()) })
	sched := engine.NewScheduler(eng, s, nil)
	for _, id := range []string{"alpha-cron", "beta-cron", "other-org-cron"} {
		if err := sched.Register(id, id, "0 * * * *", ""); err != nil {
			t.Fatal(err)
		}
	}
	rec := httptest.NewRecorder()
	schedulerStatusHandler(sched, s)(rec, request("GET", "/api/scheduler/status", "", "alice", wsAlpha, nil))
	var infos []engine.ScheduleInfo
	_ = json.Unmarshal(rec.Body.Bytes(), &infos)
	if rec.Code != http.StatusOK || len(infos) != 1 || infos[0].PipelineID != "alpha-cron" {
		t.Errorf("scheduler status for alice in alpha: %d %+v, want only alpha-cron", rec.Code, infos)
	}
}

// Purge by organization deleted every workspace's run history for an admin
// of any one of them; it is the current workspace's now.
func TestPurgeIsTheCurrentWorkspaces(t *testing.T) {
	multiTenant(t, map[string][]string{"alice": {wsAlpha}})
	s := newWorkspaceAccessStore(t)
	seedPipeline(t, s, "pa", wsAlpha)
	seedPipeline(t, s, "pb", wsBeta)
	old := time.Now().AddDate(0, 0, -90)
	for id, pipe := range map[string]string{"old-alpha": "pa", "old-beta": "pb"} {
		if err := s.CreateRun(&models.Run{ID: id, PipelineID: pipe, OrgID: wsOrg, Status: models.RunStatusSuccess, StartedAt: &old}); err != nil {
			t.Fatal(err)
		}
	}
	rec := httptest.NewRecorder()
	systemPurge(s, nil)(rec, request("POST", "/api/system/purge", "", "alice", wsAlpha, []byte(`{"days":30}`)))
	if rec.Code != http.StatusOK {
		t.Fatalf("purge: %d %s", rec.Code, rec.Body.String())
	}
	if _, err := s.GetRun("old-alpha"); err == nil {
		t.Error("purge in alpha kept alpha's old run")
	}
	if _, err := s.GetRun("old-beta"); err != nil {
		t.Error("purge in alpha deleted beta's run")
	}
}

// The dashboard counts and the run calendar were the organization's in
// every workspace, while the pipeline list beside them was the
// workspace's.
func TestDashboardAndCalendarAreTheCurrentWorkspaces(t *testing.T) {
	multiTenant(t, map[string][]string{"carol": {wsAlpha, wsBeta}})
	s := newWorkspaceAccessStore(t)
	seedPipeline(t, s, "pa", wsAlpha)
	seedPipeline(t, s, "pb", wsBeta)
	now := time.Now()
	for i, pipe := range []string{"pa", "pb", "pb"} {
		status := models.RunStatusSuccess
		if pipe == "pb" {
			status = models.RunStatusRunning
		}
		if err := s.CreateRun(&models.Run{ID: fmt.Sprintf("run-%d", i), PipelineID: pipe, OrgID: wsOrg, Status: status, StartedAt: &now}); err != nil {
			t.Fatal(err)
		}
	}
	rec := httptest.NewRecorder()
	dashboardHandler(s)(rec, request("GET", "/api/dashboard", "", "carol", wsAlpha, nil))
	var dash struct {
		RunsToday int      `json:"runs_today"`
		Running   []string `json:"running_run_ids"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &dash)
	if rec.Code != http.StatusOK || dash.RunsToday != 1 || len(dash.Running) != 0 {
		t.Errorf("dashboard in alpha: %d runs_today=%d running=%v, want 1 and none of beta's", rec.Code, dash.RunsToday, dash.Running)
	}

	rec = httptest.NewRecorder()
	calendarHandler(s)(rec, request("GET", "/api/runs/calendar?days=7", "", "carol", wsBeta, nil))
	var cal []store.CalendarDay
	_ = json.Unmarshal(rec.Body.Bytes(), &cal)
	total := 0
	for _, d := range cal {
		total += d.Total
	}
	if rec.Code != http.StatusOK || total != 2 {
		t.Errorf("calendar in beta: %d total=%d, want beta's 2 runs", rec.Code, total)
	}
}
