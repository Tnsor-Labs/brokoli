package store

import (
	"testing"
	"time"

	"github.com/Tnsor-Labs/brokoli/models"
)

func TestListPipelineDepsByOrg_ScopedAndProjected(t *testing.T) {
	s := newTestStore(t)
	now := time.Now().UTC()

	pipelines := []*models.Pipeline{
		{ID: "a1", Name: "A1", OrgID: "org-a", Nodes: []models.Node{}, Edges: []models.Edge{},
			DependencyRules: []models.DependencyRule{}, CreatedAt: now, UpdatedAt: now},
		{ID: "a2", Name: "A2", OrgID: "org-a", Nodes: []models.Node{}, Edges: []models.Edge{},
			DependencyRules: []models.DependencyRule{{PipelineID: "a1", State: models.DepStateSucceeded, Mode: models.DepModeGate}},
			DependsOn:       []string{"legacy-ignored"},
			CreatedAt:       now, UpdatedAt: now},
		{ID: "b1", Name: "B1", OrgID: "org-b", Nodes: []models.Node{}, Edges: []models.Edge{},
			DependencyRules: []models.DependencyRule{}, CreatedAt: now, UpdatedAt: now},
	}
	for _, p := range pipelines {
		if err := s.CreatePipeline(p); err != nil {
			t.Fatalf("create %s: %v", p.ID, err)
		}
	}

	summaries, err := s.ListPipelineDepsByOrg("org-a")
	if err != nil {
		t.Fatal(err)
	}
	if len(summaries) != 2 {
		t.Fatalf("want 2 summaries for org-a, got %d", len(summaries))
	}
	// Summary must carry dep fields but need not load nodes/edges.
	var a2 *models.PipelineDepSummary
	for i := range summaries {
		if summaries[i].ID == "a2" {
			a2 = &summaries[i]
		}
	}
	if a2 == nil {
		t.Fatal("a2 not in summaries")
	}
	if len(a2.DependencyRules) != 1 || a2.DependencyRules[0].PipelineID != "a1" {
		t.Errorf("rich rules not preserved: %+v", a2.DependencyRules)
	}
	if len(a2.DependsOn) != 1 || a2.DependsOn[0] != "legacy-ignored" {
		t.Errorf("legacy DependsOn not preserved: %+v", a2.DependsOn)
	}
	// Cross-org leakage check.
	for _, sum := range summaries {
		if sum.OrgID != "org-a" {
			t.Errorf("leaked org-b pipeline %s", sum.ID)
		}
	}
}

func TestGetLatestRunsByPipelineIDs_BatchAndDistinct(t *testing.T) {
	s := newTestStore(t)
	now := time.Now().UTC()

	for _, id := range []string{"p1", "p2", "p3"} {
		p := &models.Pipeline{ID: id, Name: id, Nodes: []models.Node{}, Edges: []models.Edge{},
			CreatedAt: now, UpdatedAt: now}
		if err := s.CreatePipeline(p); err != nil {
			t.Fatal(err)
		}
	}

	// Put 3 runs for p1: first failed, second success, latest cancelled.
	addRun := func(id, runID string, status models.RunStatus, ago time.Duration) {
		t.Helper()
		ts := now.Add(-ago)
		if err := s.CreateRun(&models.Run{
			ID: runID, PipelineID: id, Status: status,
			StartedAt: &ts, FinishedAt: &ts,
		}); err != nil {
			t.Fatal(err)
		}
	}
	addRun("p1", "r1a", models.RunStatusFailed, 30*time.Minute)
	addRun("p1", "r1b", models.RunStatusSuccess, 20*time.Minute)
	addRun("p1", "r1c", models.RunStatusCancelled, 5*time.Minute)
	addRun("p2", "r2a", models.RunStatusSuccess, 10*time.Minute)
	// p3: no runs.

	latest, err := s.GetLatestRunsByPipelineIDs([]string{"p1", "p2", "p3", "missing"})
	if err != nil {
		t.Fatal(err)
	}
	if len(latest) != 2 {
		t.Errorf("want 2 entries (p1, p2), got %d: %v", len(latest), latest)
	}
	if latest["p1"] == nil || latest["p1"].Status != models.RunStatusCancelled {
		t.Errorf("p1 latest wrong: %+v", latest["p1"])
	}
	if latest["p2"] == nil || latest["p2"].Status != models.RunStatusSuccess {
		t.Errorf("p2 latest wrong: %+v", latest["p2"])
	}
	if _, ok := latest["p3"]; ok {
		t.Error("p3 has no runs and must not appear in result")
	}
}

func TestGetLatestRunsByPipelineIDs_EmptyInput(t *testing.T) {
	s := newTestStore(t)
	out, err := s.GetLatestRunsByPipelineIDs(nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 0 {
		t.Errorf("empty input should return empty map, got %d entries", len(out))
	}
	out, err = s.GetLatestRunsByPipelineIDs([]string{"", ""})
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 0 {
		t.Errorf("all-empty ids should return empty map, got %d entries", len(out))
	}
}
