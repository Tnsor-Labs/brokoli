package engine

import (
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Tnsor-Labs/brokoli/extensions"
	"github.com/Tnsor-Labs/brokoli/models"
	"github.com/Tnsor-Labs/brokoli/pkg/common"
	"github.com/Tnsor-Labs/brokoli/store"
)

const routingTestNode models.NodeType = "routing_test"

type routingTestExecutor struct {
	mu     sync.Mutex
	calls  map[string]int
	inputs map[string][]*common.DataSet
}

func newRoutingTestExecutor() *routingTestExecutor {
	return &routingTestExecutor{calls: make(map[string]int), inputs: make(map[string][]*common.DataSet)}
}

func (e *routingTestExecutor) Name() string { return "routing-test" }

func (e *routingTestExecutor) CanHandle(nodeType string) bool {
	return nodeType == string(routingTestNode)
}

func (e *routingTestExecutor) Execute(ctx extensions.ExecutionContext) (*extensions.ExecutionResult, error) {
	input, _ := ctx.InputData.(*common.DataSet)
	e.mu.Lock()
	e.calls[ctx.NodeID]++
	call := e.calls[ctx.NodeID]
	e.inputs[ctx.NodeID] = append(e.inputs[ctx.NodeID], input)
	e.mu.Unlock()

	if fail, _ := ctx.Config["fail"].(bool); fail {
		return nil, fmt.Errorf("%s executed", ctx.NodeID)
	}
	if failOnce, _ := ctx.Config["fail_once"].(bool); failOnce && call == 1 {
		return nil, fmt.Errorf("%s failed once", ctx.NodeID)
	}
	if failUntil, ok := ctx.Config["fail_until"].(float64); ok && call <= int(failUntil) {
		return nil, fmt.Errorf("%s failed on call %d", ctx.NodeID, call)
	}
	if source, _ := ctx.Config["source"].(bool); source {
		return &extensions.ExecutionResult{OutputData: &common.DataSet{
			Columns: []string{"id"},
			Rows: []common.DataRow{
				{"id": 1},
				{"id": 2},
			},
		}}, nil
	}
	return &extensions.ExecutionResult{OutputData: input}, nil
}

func (e *routingTestExecutor) callCount(nodeID string) int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.calls[nodeID]
}

func (e *routingTestExecutor) lastInput(nodeID string) *common.DataSet {
	e.mu.Lock()
	defer e.mu.Unlock()
	inputs := e.inputs[nodeID]
	if len(inputs) == 0 {
		return nil
	}
	return inputs[len(inputs)-1]
}

func boolPointer(value bool) *bool { return &value }

func runRoutingPipeline(t *testing.T, pipeline *models.Pipeline, executors ...extensions.NodeExecutor) (*models.Run, *store.SQLiteStore) {
	t.Helper()
	s, err := store.NewSQLiteStore(filepath.Join(t.TempDir(), "routing.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	if pipeline.ID == "" {
		pipeline.ID = "conditional-routing"
	}
	if pipeline.Name == "" {
		pipeline.Name = "Conditional Routing"
	}
	now := time.Now().UTC()
	pipeline.CreatedAt = now
	pipeline.UpdatedAt = now
	if err := s.CreatePipeline(pipeline); err != nil {
		t.Fatalf("create pipeline: %v", err)
	}

	runner := NewRunner(s, nil, pipeline, nil, nil, executors, nil, "", nil)
	run, runErr := runner.Execute()
	if runErr != nil {
		t.Fatalf("execute pipeline: %v", runErr)
	}
	return run, s
}

func basicConditionalPipeline(expression string) *models.Pipeline {
	return &models.Pipeline{
		IRVersion: models.ConditionalEdgesIRVersion,
		Nodes: []models.Node{
			{ID: "source", Type: routingTestNode, Name: "Source", Config: map[string]interface{}{"source": true}},
			{ID: "check", Type: models.NodeTypeCondition, Name: "Check", Config: map[string]interface{}{"expression": expression}},
			{ID: "yes", Type: routingTestNode, Name: "Yes", Config: map[string]interface{}{}},
			{ID: "no", Type: routingTestNode, Name: "No", Config: map[string]interface{}{}},
		},
		Edges: []models.Edge{
			{From: "source", To: "check"},
			{From: "check", To: "yes", Condition: boolPointer(true)},
			{From: "check", To: "no", Condition: boolPointer(false)},
		},
	}
}

func latestNodeRuns(nodeRuns []models.NodeRun) map[string]models.NodeRun {
	latest := make(map[string]models.NodeRun)
	for _, nr := range nodeRuns {
		current, ok := latest[nr.NodeID]
		if !ok || nr.Attempt >= current.Attempt {
			latest[nr.NodeID] = nr
		}
	}
	return latest
}

func TestConditionalRoutingSelectsOneBranchAndPreservesPayload(t *testing.T) {
	for _, test := range []struct {
		name       string
		expression string
		selected   string
		inactive   string
	}{
		{name: "true", expression: "always_true", selected: "yes", inactive: "no"},
		{name: "false", expression: "always_false", selected: "no", inactive: "yes"},
	} {
		t.Run(test.name, func(t *testing.T) {
			executor := newRoutingTestExecutor()
			run, s := runRoutingPipeline(t, basicConditionalPipeline(test.expression), executor)

			if executor.callCount(test.selected) != 1 {
				t.Fatalf("selected node %s calls = %d, want 1", test.selected, executor.callCount(test.selected))
			}
			if executor.callCount(test.inactive) != 0 {
				t.Fatalf("inactive node %s executed %d time(s)", test.inactive, executor.callCount(test.inactive))
			}
			input := executor.lastInput(test.selected)
			if input == nil || len(input.Rows) != 2 {
				t.Fatalf("selected branch input = %#v, want original two rows", input)
			}

			stored, err := s.GetRun(run.ID)
			if err != nil {
				t.Fatal(err)
			}
			latest := latestNodeRuns(stored.NodeRuns)
			if latest[test.selected].Status != models.RunStatusSuccess {
				t.Errorf("selected status = %q, want success", latest[test.selected].Status)
			}
			if latest[test.inactive].Status != models.RunStatusSkipped {
				t.Errorf("inactive status = %q, want skipped", latest[test.inactive].Status)
			}

			events, err := s.ListEventsByRun(run.ID)
			if err != nil {
				t.Fatal(err)
			}
			projected := ProjectRun(run.ID, events)
			projectedLatest := latestNodeRuns(projected.NodeRuns)
			if projectedLatest[test.inactive].Status != models.RunStatusSkipped {
				t.Errorf("projected inactive status = %q, want skipped", projectedLatest[test.inactive].Status)
			}
		})
	}
}

func TestConditionalRoutingPropagatesSkipsAndConverges(t *testing.T) {
	executor := newRoutingTestExecutor()
	pipeline := basicConditionalPipeline("always_true")
	pipeline.Nodes = append(pipeline.Nodes,
		models.Node{ID: "inactive-tail", Type: routingTestNode, Name: "Inactive Tail", Config: map[string]interface{}{}},
		models.Node{ID: "merge", Type: routingTestNode, Name: "Merge", Config: map[string]interface{}{}},
	)
	pipeline.Edges = append(pipeline.Edges,
		models.Edge{From: "no", To: "inactive-tail"},
		models.Edge{From: "yes", To: "merge"},
		models.Edge{From: "inactive-tail", To: "merge"},
	)

	run, s := runRoutingPipeline(t, pipeline, executor)
	if executor.callCount("yes") != 1 || executor.callCount("merge") != 1 {
		t.Fatalf("active calls: yes=%d merge=%d", executor.callCount("yes"), executor.callCount("merge"))
	}
	if executor.callCount("no") != 0 || executor.callCount("inactive-tail") != 0 {
		t.Fatalf("inactive chain executed: no=%d tail=%d", executor.callCount("no"), executor.callCount("inactive-tail"))
	}
	if input := executor.lastInput("merge"); input == nil || len(input.Rows) != 2 {
		t.Fatalf("merge input = %#v, want active branch payload", input)
	}

	stored, err := s.GetRun(run.ID)
	if err != nil {
		t.Fatal(err)
	}
	latest := latestNodeRuns(stored.NodeRuns)
	for _, nodeID := range []string{"no", "inactive-tail"} {
		if latest[nodeID].Status != models.RunStatusSkipped {
			t.Errorf("%s status = %q, want skipped", nodeID, latest[nodeID].Status)
		}
	}
}

func TestConditionalRoutingFanoutActivatesEveryMatchingEdge(t *testing.T) {
	executor := newRoutingTestExecutor()
	pipeline := basicConditionalPipeline("always_true")
	pipeline.Nodes = append(pipeline.Nodes, models.Node{ID: "yes-2", Type: routingTestNode, Name: "Yes 2", Config: map[string]interface{}{}})
	pipeline.Edges = append(pipeline.Edges, models.Edge{From: "check", To: "yes-2", Condition: boolPointer(true)})

	runRoutingPipeline(t, pipeline, executor)
	if executor.callCount("yes") != 1 || executor.callCount("yes-2") != 1 || executor.callCount("no") != 0 {
		t.Fatalf("fanout calls: yes=%d yes-2=%d no=%d", executor.callCount("yes"), executor.callCount("yes-2"), executor.callCount("no"))
	}
}

func TestConditionalRoutingRejectsInactiveJoinInput(t *testing.T) {
	executor := newRoutingTestExecutor()
	pipeline := basicConditionalPipeline("always_true")
	pipeline.Nodes = append(pipeline.Nodes, models.Node{
		ID: "join", Type: models.NodeTypeJoin, Name: "Join",
		Config: map[string]interface{}{"left_key": "id", "right_key": "id"},
	})
	pipeline.Edges = append(pipeline.Edges,
		models.Edge{From: "yes", To: "join"},
		models.Edge{From: "no", To: "join"},
	)

	s, err := store.NewSQLiteStore(filepath.Join(t.TempDir(), "join.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	pipeline.ID = "conditional-join"
	pipeline.Name = "Conditional Join"
	now := time.Now().UTC()
	pipeline.CreatedAt, pipeline.UpdatedAt = now, now
	if err := s.CreatePipeline(pipeline); err != nil {
		t.Fatal(err)
	}
	runner := NewRunner(s, nil, pipeline, nil, nil, []extensions.NodeExecutor{executor}, nil, "", nil)
	run, err := runner.Execute()
	if err == nil || !strings.Contains(err.Error(), "inactive required input") {
		t.Fatalf("error = %v, want inactive join input failure", err)
	}
	if run.Status != models.RunStatusFailed {
		t.Fatalf("run status = %q, want failed", run.Status)
	}
}

func TestIR20ConditionRetainsStaticEmptyDatasetBehavior(t *testing.T) {
	executor := newRoutingTestExecutor()
	pipeline := basicConditionalPipeline("always_false")
	pipeline.IRVersion = "2.0"
	for i := range pipeline.Edges {
		pipeline.Edges[i].Condition = nil
	}

	runRoutingPipeline(t, pipeline, executor)
	for _, nodeID := range []string{"yes", "no"} {
		if executor.callCount(nodeID) != 1 {
			t.Errorf("legacy branch %s calls = %d, want 1", nodeID, executor.callCount(nodeID))
		}
		if input := executor.lastInput(nodeID); input == nil || len(input.Rows) != 0 {
			t.Errorf("legacy branch %s input = %#v, want empty dataset", nodeID, input)
		}
	}
}

func TestRunnerFailsInsteadOfSucceedingWithUnresolvedNodes(t *testing.T) {
	s, err := store.NewSQLiteStore(filepath.Join(t.TempDir(), "stalled.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	now := time.Now().UTC()
	pipeline := &models.Pipeline{
		ID: "stalled", Name: "Stalled", IRVersion: models.ConditionalEdgesIRVersion,
		Nodes: []models.Node{
			{ID: "a", Type: routingTestNode, Name: "A", Config: map[string]interface{}{}},
			{ID: "b", Type: routingTestNode, Name: "B", Config: map[string]interface{}{}},
		},
		Edges:     []models.Edge{{From: "a", To: "b"}, {From: "b", To: "a"}},
		CreatedAt: now, UpdatedAt: now,
	}
	if err := s.CreatePipeline(pipeline); err != nil {
		t.Fatal(err)
	}
	runner := NewRunner(s, nil, pipeline, nil, nil, []extensions.NodeExecutor{newRoutingTestExecutor()}, nil, "", nil)
	run, err := runner.Execute()
	if err == nil || !strings.Contains(err.Error(), "scheduler stalled") {
		t.Fatalf("error = %v, want scheduler stalled", err)
	}
	if run.Status != models.RunStatusFailed {
		t.Fatalf("run status = %q, want failed", run.Status)
	}
}

func TestResumeRestoresConditionDecisionEvenIfExpressionChanges(t *testing.T) {
	eng, s := newResumeTestEngine(t)
	executor := newRoutingTestExecutor()
	eng.Executors = []extensions.NodeExecutor{executor}
	pipeline := basicConditionalPipeline("always_true")
	pipeline.ID = "conditional-resume"
	pipeline.Name = "Conditional Resume"
	pipeline.Nodes[2].Config["fail_once"] = true
	now := time.Now().UTC()
	pipeline.CreatedAt, pipeline.UpdatedAt = now, now
	if err := s.CreatePipeline(pipeline); err != nil {
		t.Fatal(err)
	}

	runner := NewRunner(s, nil, pipeline, nil, nil, eng.Executors, nil, "", nil)
	runner.artifactStore = eng.ArtifactStore
	failedRun, err := runner.Execute()
	if err == nil || failedRun.Status != models.RunStatusFailed {
		t.Fatalf("initial run = status %q error %v, want failed", failedRun.Status, err)
	}
	// If resume reevaluated this mutable live definition it would select the
	// opposite branch and repeat side effects there. The persisted decision
	// must keep routing to "yes".
	pipeline.Nodes[1].Config["expression"] = "always_false"
	if err := s.UpdatePipeline(pipeline); err != nil {
		t.Fatalf("change live condition: %v", err)
	}

	resumed, err := eng.ResumeRun(failedRun.ID)
	if err != nil {
		t.Fatalf("resume: %v", err)
	}
	if resumed.Status != models.RunStatusSuccess {
		t.Fatalf("resumed status = %q, want success", resumed.Status)
	}
	if executor.callCount("source") != 1 {
		t.Fatalf("source calls = %d, want one original execution", executor.callCount("source"))
	}
	if executor.callCount("yes") != 2 || executor.callCount("no") != 0 {
		t.Fatalf("branch calls after resume: yes=%d no=%d", executor.callCount("yes"), executor.callCount("no"))
	}

	stored, err := s.GetRun(resumed.ID)
	if err != nil {
		t.Fatal(err)
	}
	latest := latestNodeRuns(stored.NodeRuns)
	if latest["check"].Status != models.RunStatusSkipped || latest["check"].StartedAt != nil {
		t.Fatalf("condition executed instead of restoring its decision: %#v", latest["check"])
	}
	if latest["no"].Status != models.RunStatusSkipped {
		t.Fatalf("inactive branch status = %q, want skipped", latest["no"].Status)
	}
}

func TestConditionDecisionAndArtifactsSurviveRepeatedResumes(t *testing.T) {
	eng, s := newResumeTestEngine(t)
	executor := newRoutingTestExecutor()
	eng.Executors = []extensions.NodeExecutor{executor}
	pipeline := basicConditionalPipeline("always_true")
	pipeline.ID = "conditional-repeated-resume"
	pipeline.Name = "Conditional Repeated Resume"
	pipeline.Nodes = append(pipeline.Nodes, models.Node{
		ID: "later", Type: routingTestNode, Name: "Later",
		Config: map[string]interface{}{"fail_until": float64(2)},
	})
	pipeline.Edges = append(pipeline.Edges, models.Edge{From: "yes", To: "later"})
	now := time.Now().UTC()
	pipeline.CreatedAt, pipeline.UpdatedAt = now, now
	if err := s.CreatePipeline(pipeline); err != nil {
		t.Fatal(err)
	}

	runner := NewRunner(s, nil, pipeline, nil, nil, eng.Executors, nil, "", nil)
	runner.artifactStore = eng.ArtifactStore
	first, err := runner.Execute()
	if err == nil || first.Status != models.RunStatusFailed {
		t.Fatalf("first run = status %q error %v, want failed", first.Status, err)
	}
	pipeline.Nodes[1].Config["expression"] = "always_false"
	if err := s.UpdatePipeline(pipeline); err != nil {
		t.Fatal(err)
	}

	second, err := eng.ResumeRun(first.ID)
	if err == nil || second.Status != models.RunStatusFailed {
		t.Fatalf("second run = status %q error %v, want failed", second.Status, err)
	}
	third, err := eng.ResumeRun(second.ID)
	if err != nil {
		t.Fatalf("third run: %v", err)
	}
	if third.Status != models.RunStatusSuccess {
		t.Fatalf("third status = %q, want success", third.Status)
	}
	if executor.callCount("source") != 1 || executor.callCount("yes") != 1 || executor.callCount("no") != 0 {
		t.Fatalf("reused route calls: source=%d yes=%d no=%d", executor.callCount("source"), executor.callCount("yes"), executor.callCount("no"))
	}
	if executor.callCount("later") != 3 {
		t.Fatalf("later calls = %d, want 3", executor.callCount("later"))
	}
}

func TestResumeFindsAncestorAfterLineageCarryFails(t *testing.T) {
	dir := t.TempDir()
	real, err := store.NewSQLiteStore(filepath.Join(dir, "lineage-failure.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { real.Close() })
	fault := &faultInjectingStore{SQLiteStore: real}
	eng := NewEngine(fault)
	eng.ArtifactStore = NewLocalDiskArtifactStore(filepath.Join(dir, "artifacts"))
	executor := newRoutingTestExecutor()
	eng.Executors = []extensions.NodeExecutor{executor}
	pipeline := basicConditionalPipeline("always_true")
	pipeline.ID = "conditional-lineage-failure"
	pipeline.Name = "Conditional Lineage Failure"
	pipeline.Nodes[2].Config["fail_once"] = true
	now := time.Now().UTC()
	pipeline.CreatedAt, pipeline.UpdatedAt = now, now
	if err := real.CreatePipeline(pipeline); err != nil {
		t.Fatal(err)
	}

	runner := NewRunner(fault, nil, pipeline, nil, nil, eng.Executors, nil, "", nil)
	runner.artifactStore = eng.ArtifactStore
	first, err := runner.Execute()
	if err == nil || first.Status != models.RunStatusFailed {
		t.Fatalf("first run = status %q error %v, want failed", first.Status, err)
	}
	pipeline.Nodes[1].Config["expression"] = "always_false"
	if err := real.UpdatePipeline(pipeline); err != nil {
		t.Fatal(err)
	}

	fault.failAppendEventType = models.AttemptSkipped
	second, err := eng.ResumeRun(first.ID)
	if err == nil || second.Status != models.RunStatusFailed {
		t.Fatalf("second run = status %q error %v, want lineage carry failure", second.Status, err)
	}
	fault.failAppendEventType = ""
	third, err := eng.ResumeRun(second.ID)
	if err != nil {
		t.Fatalf("resume through failed carry: %v", err)
	}
	if third.Status != models.RunStatusSuccess {
		t.Fatalf("third status = %q, want success", third.Status)
	}
	if executor.callCount("source") != 1 || executor.callCount("yes") != 2 || executor.callCount("no") != 0 {
		t.Fatalf("calls after ancestor recovery: source=%d yes=%d no=%d", executor.callCount("source"), executor.callCount("yes"), executor.callCount("no"))
	}
}

type conditionClaimingExecutor struct {
	mu     sync.Mutex
	called bool
}

func (e *conditionClaimingExecutor) Name() string { return "condition-claiming" }
func (e *conditionClaimingExecutor) CanHandle(nodeType string) bool {
	return nodeType == string(models.NodeTypeCondition)
}
func (e *conditionClaimingExecutor) Execute(extensions.ExecutionContext) (*extensions.ExecutionResult, error) {
	e.mu.Lock()
	e.called = true
	e.mu.Unlock()
	return nil, fmt.Errorf("condition should not be dispatched")
}
func (e *conditionClaimingExecutor) wasCalled() bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.called
}

func TestConditionRoutingCannotBeReplacedByExternalExecutor(t *testing.T) {
	routing := newRoutingTestExecutor()
	claiming := &conditionClaimingExecutor{}
	runRoutingPipeline(t, basicConditionalPipeline("always_true"), routing, claiming)
	if claiming.wasCalled() {
		t.Fatal("condition node was dispatched to an output-only external executor")
	}
	if routing.callCount("yes") != 1 || routing.callCount("no") != 0 {
		t.Fatalf("built-in route calls: yes=%d no=%d", routing.callCount("yes"), routing.callCount("no"))
	}
}

func TestSkippedNodeAndEventRollbackTogether(t *testing.T) {
	dir := t.TempDir()
	real, err := store.NewSQLiteStore(filepath.Join(dir, "skip-rollback.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { real.Close() })
	pipeline := basicConditionalPipeline("always_true")
	pipeline.ID = "skip-rollback"
	pipeline.Name = "Skip Rollback"
	now := time.Now().UTC()
	pipeline.CreatedAt, pipeline.UpdatedAt = now, now
	if err := real.CreatePipeline(pipeline); err != nil {
		t.Fatal(err)
	}
	fault := &faultInjectingStore{SQLiteStore: real, failAppendEventType: models.AttemptSkipped}
	runner := NewRunner(fault, nil, pipeline, nil, nil, []extensions.NodeExecutor{newRoutingTestExecutor()}, nil, "", nil)
	run, err := runner.Execute()
	if err == nil || !strings.Contains(err.Error(), "persist skipped node") {
		t.Fatalf("error = %v, want skipped persistence failure", err)
	}

	nodeRuns, listErr := real.ListNodeRunsByRun(run.ID)
	if listErr != nil {
		t.Fatal(listErr)
	}
	for _, nr := range nodeRuns {
		if nr.NodeID == "no" {
			t.Fatalf("skipped NodeRun survived rolled-back event transaction: %#v", nr)
		}
	}
	events, listErr := real.ListEventsByRun(run.ID)
	if listErr != nil {
		t.Fatal(listErr)
	}
	for _, event := range events {
		if event.EventType == models.AttemptSkipped {
			t.Fatalf("skipped event survived rolled-back transaction: %#v", event)
		}
	}
}

func TestConditionSuccessAndDecisionRollbackTogether(t *testing.T) {
	dir := t.TempDir()
	real, err := store.NewSQLiteStore(filepath.Join(dir, "condition-rollback.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { real.Close() })
	pipeline := basicConditionalPipeline("always_true")
	// Remove the false branch so the injected transaction failure can only
	// target condition completion, not later skip persistence.
	pipeline.Nodes = pipeline.Nodes[:3]
	pipeline.Edges = pipeline.Edges[:2]
	pipeline.ID = "condition-rollback"
	pipeline.Name = "Condition Rollback"
	now := time.Now().UTC()
	pipeline.CreatedAt, pipeline.UpdatedAt = now, now
	if err := real.CreatePipeline(pipeline); err != nil {
		t.Fatal(err)
	}
	fault := &faultInjectingStore{SQLiteStore: real, failAppendEventType: models.AttemptCompleted}
	runner := NewRunner(fault, nil, pipeline, nil, nil, []extensions.NodeExecutor{newRoutingTestExecutor()}, nil, "", nil)
	run, err := runner.Execute()
	if err == nil || !strings.Contains(err.Error(), "with routing decision") {
		t.Fatalf("error = %v, want condition transaction failure", err)
	}

	nodeRuns, listErr := real.ListNodeRunsByRun(run.ID)
	if listErr != nil {
		t.Fatal(listErr)
	}
	latest := latestNodeRuns(nodeRuns)
	if latest["check"].Status == models.RunStatusSuccess {
		t.Fatalf("condition success survived rolled-back decision event: %#v", latest["check"])
	}
	events, listErr := real.ListEventsByRun(run.ID)
	if listErr != nil {
		t.Fatal(listErr)
	}
	for _, event := range events {
		if event.NodeID == "check" && event.EventType == models.AttemptCompleted {
			t.Fatalf("condition decision event survived rollback: %#v", event)
		}
	}
}

var _ extensions.NodeExecutor = (*routingTestExecutor)(nil)
var _ extensions.NodeExecutor = (*conditionClaimingExecutor)(nil)
