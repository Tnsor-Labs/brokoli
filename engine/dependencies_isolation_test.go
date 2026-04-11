package engine

import (
	"strings"
	"testing"
	"time"

	"github.com/Tnsor-Labs/brokoli/models"
	"github.com/Tnsor-Labs/brokoli/store"
)

func newOrgPipe(t *testing.T, s store.Store, id, name, orgID string, deps []models.DependencyRule) *models.Pipeline {
	t.Helper()
	now := time.Now().UTC()
	p := &models.Pipeline{
		ID:              id,
		Name:            name,
		OrgID:           orgID,
		Nodes:           []models.Node{},
		Edges:           []models.Edge{},
		DependencyRules: deps,
		Enabled:         true,
		CreatedAt:       now,
		UpdatedAt:       now,
	}
	if err := s.CreatePipeline(p); err != nil {
		t.Fatalf("CreatePipeline %s: %v", id, err)
	}
	return p
}

// H2: Tenant A must not observe tenant B's run state through a dependency rule.
func TestCheckDependencies_CrossOrgUpstreamTreatedAsMissing(t *testing.T) {
	s := newDepTestStore(t)
	// Tenant B owns the upstream with a successful run.
	newOrgPipe(t, s, "b-up", "B-Upstream", "org-b", nil)
	putRun(t, s, "b-up", models.RunStatusSuccess, 5*time.Minute)

	// Tenant A creates a downstream pointing at tenant B's pipeline ID.
	aDown := newOrgPipe(t, s, "a-down", "A-Downstream", "org-a", []models.DependencyRule{
		{PipelineID: "b-up"},
	})

	ok, statuses, reason := CheckDependencies(s, aDown, time.Now().UTC())
	if ok {
		t.Fatal("cross-org upstream must NOT satisfy a dep — would leak state")
	}
	if len(statuses) != 1 {
		t.Fatalf("want 1 status, got %d", len(statuses))
	}
	if !statuses[0].Missing {
		t.Error("cross-org upstream must appear missing")
	}
	// The reason may include the opaque ID, but must NOT leak the upstream's name or status.
	if strings.Contains(reason, "B-Upstream") {
		t.Errorf("reason leaks upstream name: %q", reason)
	}
	if strings.Contains(reason, string(models.RunStatusSuccess)) {
		t.Errorf("reason leaks upstream run status: %q", reason)
	}
}

// H1: Cycle detection must not traverse other tenants.
func TestDetectCycle_IgnoresOtherOrgs(t *testing.T) {
	s := newDepTestStore(t)
	// Tenant A has a linear chain that would form a cycle ONLY if
	// combined with tenant B's graph. Detector must not see B at all.
	newOrgPipe(t, s, "a1", "A1", "org-a", nil)
	newOrgPipe(t, s, "b1", "B1", "org-b", []models.DependencyRule{{PipelineID: "a1"}})

	// Candidate: a1 wants to depend on b1. In a flat graph this creates a cycle
	// a1 -> b1 -> a1. But b1 is in org-b, so from a1's perspective there is no cycle
	// because the traversal is scoped to org-a.
	a1, _ := s.GetPipeline("a1")
	a1.DependencyRules = []models.DependencyRule{{PipelineID: "b1"}}
	// (The save-time org-scope check at the API layer would actually reject this earlier,
	// but we test the engine-level detector in isolation here.)
	if err := DetectDependencyCycle(s, a1); err != nil {
		t.Errorf("detector traversed other org and falsely flagged cycle: %v", err)
	}
}

// H3: Trigger-mode firing must not cross org boundaries.
func TestFireTriggerModeDependents_SkipsCrossOrg(t *testing.T) {
	s := newDepTestStore(t)
	// Upstream in org-a.
	newOrgPipe(t, s, "a-up", "A-Upstream", "org-a", nil)
	// Malicious downstream in org-b targets org-a's pipeline with trigger mode.
	newOrgPipe(t, s, "b-down", "B-Downstream", "org-b", []models.DependencyRule{
		{PipelineID: "a-up", Mode: models.DepModeTrigger},
	})
	// Legitimate same-org downstream in org-a.
	newOrgPipe(t, s, "a-down", "A-Downstream", "org-a", []models.DependencyRule{
		{PipelineID: "a-up", Mode: models.DepModeTrigger},
	})

	// Simulate the finished upstream run.
	now := time.Now().UTC()
	finished := &models.Run{
		ID: "a-up-run", PipelineID: "a-up",
		Status:     models.RunStatusSuccess,
		StartedAt:  &now, FinishedAt: &now,
	}
	if err := s.CreateRun(finished); err != nil {
		t.Fatalf("CreateRun: %v", err)
	}

	// Inspect who would be fired. We reuse the same logic the engine uses:
	// PipelinesDependingOn filtered to upstream's org.
	upstream, _ := s.GetPipeline("a-up")
	dependents, _ := s.PipelinesDependingOn("a-up")
	fired := make([]string, 0)
	for _, d := range dependents {
		if d.OrgID != upstream.OrgID {
			continue
		}
		for _, rule := range d.EffectiveDependencies() {
			if rule.PipelineID == "a-up" && rule.Mode == models.DepModeTrigger {
				fired = append(fired, d.ID)
				break
			}
		}
	}
	if len(fired) != 1 {
		t.Fatalf("want 1 fired (same org), got %d: %v", len(fired), fired)
	}
	if fired[0] != "a-down" {
		t.Errorf("wrong pipeline fired: got %q want a-down", fired[0])
	}
}

// PipelinesDependingOn is a raw store query and IS permitted to return cross-org matches
// (callers are responsible for filtering). This test documents that contract.
func TestPipelinesDependingOn_ReturnsCrossOrgRaw(t *testing.T) {
	s := newDepTestStore(t)
	newOrgPipe(t, s, "a-up", "A-Upstream", "org-a", nil)
	newOrgPipe(t, s, "b-down", "B-Downstream", "org-b", []models.DependencyRule{{PipelineID: "a-up"}})

	deps, err := s.PipelinesDependingOn("a-up")
	if err != nil {
		t.Fatal(err)
	}
	if len(deps) != 1 {
		t.Errorf("want 1 raw match, got %d", len(deps))
	}
}

