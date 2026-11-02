package sodp

import (
	"testing"
	"time"

	"github.com/Tnsor-Labs/brokoli/pkg/tenancy"
)

// ADR-049: where workspaces are kept apart, a connection watches only the
// dashboards and runs of workspaces its person belongs to. It checked the
// organization only, so the organization's dashboard listed every
// workspace's runs and their ids opened their live logs.
func TestLiveStateKeepsToTheSessionsWorkspaces(t *testing.T) {
	srv := NewServer()
	workspaces := map[string]string{"pa": "ws-alpha", "pb": "ws-beta"}
	srv.PipelineWorkspace = func(id string) string { return workspaces[id] }
	runWS := map[string]string{"ra": "ws-alpha", "rb": "ws-beta"}
	srv.RunVisible = func(scope tenancy.Scope, runID string) bool { return scope.Sees("acme", runWS[runID]) }

	now := time.Now()
	// Beta's run first, so alpha's dashboard is computed with it present.
	bridgeEvent(srv, BridgeEvent{Type: "run.started", RunID: "rb", PipelineID: "pb", PipelineName: "beta-secret", OrgID: "acme", Timestamp: now})
	bridgeEvent(srv, BridgeEvent{Type: "run.started", RunID: "ra", PipelineID: "pa", PipelineName: "alpha", OrgID: "acme", Timestamp: now})

	alice := NewSession("a", "acme")
	alice.MarkAuthenticated()
	alice.Scope, alice.HasScope = tenancy.New("acme", "ws-alpha", []string{"ws-alpha"}, tenancy.ActorPerson), true

	for key, want := range map[string]bool{
		"dashboard.acme":          false,
		"dashboard.acme.ws-alpha": true,
		"dashboard.acme.ws-beta":  false,
		"runs.ra":                 true,
		"runs.ra.logs":            true,
		"runs.rb":                 false,
		"runs.rb.logs":            false,
	} {
		if got := srv.keyAllowedForSession(alice, key); got != want {
			t.Errorf("alice (alpha only) may watch %s: %v, want %v", key, got, want)
		}
	}

	// Without workspaces kept apart the organization's keys stay open.
	legacy := NewSession("l", "acme")
	legacy.MarkAuthenticated()
	if !srv.keyAllowedForSession(legacy, "dashboard.acme") || !srv.keyAllowedForSession(legacy, "runs.rb") {
		t.Error("a session with no scope lost the organization's dashboard or runs")
	}

	val, _ := srv.State.Get("dashboard.acme.ws-alpha")
	snap, _ := val.(map[string]any)
	recent, _ := snap["recent_runs"].([]map[string]any)
	if len(recent) != 1 || recent[0]["run_id"] != "ra" {
		t.Errorf("alpha's dashboard: %v, want only run ra", snap["recent_runs"])
	}
	if ids, _ := snap["running_run_ids"].([]string); len(ids) != 1 || ids[0] != "ra" {
		t.Errorf("alpha's running runs: %v, want only ra", snap["running_run_ids"])
	}
}
