package engine

// Tests for the physical planner (ADR-015, #90 M2). The plan must mirror
// the runner's real staging and read the same config the handlers do, so
// its explanations are truthful before execution.

import (
	"testing"

	"github.com/Tnsor-Labs/brokoli/models"
)

func node(id, typ string, cfg map[string]interface{}) models.Node {
	return models.Node{ID: id, Type: models.NodeType(typ), Name: id, Config: cfg}
}

func unitByNode(plan *models.PhysicalPlan, id string) *models.PhysicalWorkUnit {
	for i := range plan.Stages {
		for j := range plan.Stages[i].WorkUnits {
			if plan.Stages[i].WorkUnits[j].LogicalNodeID == id {
				return &plan.Stages[i].WorkUnits[j]
			}
		}
	}
	return nil
}

func stageOf(plan *models.PhysicalPlan, id string) int {
	for i := range plan.Stages {
		for _, u := range plan.Stages[i].WorkUnits {
			if u.LogicalNodeID == id {
				return i
			}
		}
	}
	return -1
}

func TestPlanStagesFollowDependencyWaves(t *testing.T) {
	p := &models.Pipeline{
		PipelineID: "waves",
		Nodes: []models.Node{
			node("a", "source_file", map[string]interface{}{"path": "/a"}),
			node("b", "source_file", map[string]interface{}{"path": "/b"}),
			node("j", "join", map[string]interface{}{"left_key": "id"}),
			node("s", "sink_file", map[string]interface{}{"path": "/o"}),
		},
		Edges: []models.Edge{
			{From: "a", To: "j"}, {From: "b", To: "j"}, {From: "j", To: "s"},
		},
	}
	plan, err := PlanPipeline(p)
	if err != nil {
		t.Fatal(err)
	}
	if stageOf(plan, "a") != 0 || stageOf(plan, "b") != 0 {
		t.Fatalf("sources not in stage 0: a=%d b=%d", stageOf(plan, "a"), stageOf(plan, "b"))
	}
	if stageOf(plan, "j") != 1 {
		t.Fatalf("join not in stage 1: %d", stageOf(plan, "j"))
	}
	if stageOf(plan, "s") != 2 {
		t.Fatalf("sink not in stage 2: %d", stageOf(plan, "s"))
	}
	if plan.StaticInstanceCount != 4 || plan.DynamicNodes != 0 {
		t.Fatalf("static=%d dynamic=%d, want 4/0", plan.StaticInstanceCount, plan.DynamicNodes)
	}
}

func TestPlanSingleNodeShape(t *testing.T) {
	p := &models.Pipeline{PipelineID: "s", Nodes: []models.Node{
		node("x", "transform", map[string]interface{}{}),
	}}
	plan, _ := PlanPipeline(p)
	u := unitByNode(plan, "x")
	if u.Kind != models.WorkUnitSingle || u.InstanceKeyTemplate != "x" ||
		u.StaticInstanceCount != 1 || u.RuntimeResolved || u.RetryScope != models.RetryScopeNode {
		t.Fatalf("unexpected single unit: %+v", u)
	}
}

func TestPlanExpansionIsRuntimeResolvedWorkUnitRetry(t *testing.T) {
	p := &models.Pipeline{PipelineID: "exp", Nodes: []models.Node{
		node("src", "source_file", map[string]interface{}{"path": "/a"}),
		node("fan", "code", map[string]interface{}{
			"script": "x",
			"expansion": map[string]interface{}{
				"over":          map[string]interface{}{"item": "src"},
				"max_instances": float64(8),
			},
		}),
	}, Edges: []models.Edge{{From: "src", To: "fan"}}}
	plan, _ := PlanPipeline(p)
	u := unitByNode(plan, "fan")
	if u.Kind != models.WorkUnitExpansion || !u.RuntimeResolved ||
		u.StaticInstanceCount != 0 || u.RetryScope != models.RetryScopeWorkUnit {
		t.Fatalf("expansion shape wrong: %+v", u)
	}
	if u.InstanceKeyTemplate != "fan[<item>]" || u.MaxConcurrency != 8 {
		t.Fatalf("expansion key/concurrency wrong: %+v", u)
	}
	if plan.DynamicNodes != 1 {
		t.Fatalf("dynamic nodes=%d, want 1", plan.DynamicNodes)
	}
	// The static source still counts; the dynamic node does not.
	if plan.StaticInstanceCount != 1 {
		t.Fatalf("static=%d, want 1 (only the source)", plan.StaticInstanceCount)
	}
}

func TestPlanPaginationConcurrencyFromExecutionPolicy(t *testing.T) {
	p := &models.Pipeline{PipelineID: "pag", Nodes: []models.Node{
		node("api", "source_api", map[string]interface{}{
			"url":        "https://x",
			"pagination": map[string]interface{}{"strategy": "offset"},
			"execution":  map[string]interface{}{"max_concurrency": float64(6)},
		}),
	}}
	plan, _ := PlanPipeline(p)
	u := unitByNode(plan, "api")
	if u.Kind != models.WorkUnitPagination || u.MaxConcurrency != 6 ||
		u.InstanceKeyTemplate != "api#page-<n>" || u.RetryScope != models.RetryScopeWorkUnit {
		t.Fatalf("pagination shape wrong: %+v", u)
	}
}

func TestPlanPaginationDefaultConcurrency(t *testing.T) {
	// No execution policy -> default max_concurrency 1 (matches the fetcher).
	p := &models.Pipeline{PipelineID: "pag", Nodes: []models.Node{
		node("api", "source_api", map[string]interface{}{
			"url":        "https://x",
			"pagination": map[string]interface{}{"strategy": "offset"},
		}),
	}}
	u := unitByNode(mustPlan(t, p), "api")
	if u.MaxConcurrency != 1 {
		t.Fatalf("default pagination concurrency=%d, want 1", u.MaxConcurrency)
	}
}

func TestPlanRejectsCycle(t *testing.T) {
	p := &models.Pipeline{PipelineID: "cyc", Nodes: []models.Node{
		node("a", "transform", nil), node("b", "transform", nil),
	}, Edges: []models.Edge{{From: "a", To: "b"}, {From: "b", To: "a"}}}
	if _, err := PlanPipeline(p); err == nil {
		t.Fatal("planner accepted a cyclic graph")
	}
}

func mustPlan(t *testing.T, p *models.Pipeline) *models.PhysicalPlan {
	t.Helper()
	plan, err := PlanPipeline(p)
	if err != nil {
		t.Fatal(err)
	}
	return plan
}
