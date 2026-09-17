package engine

import (
	"strings"
	"testing"

	"github.com/Tnsor-Labs/brokoli/extensions"
	"github.com/Tnsor-Labs/brokoli/models"
	"github.com/Tnsor-Labs/brokoli/pkg/common"
)

// echoInput succeeds on every call and hands its input straight back, so a
// test can count exactly which nodes executed again without any node being
// flaky.
func echoInput() func(call int, input *common.DataSet) (*common.DataSet, error) {
	return func(call int, input *common.DataSet) (*common.DataSet, error) {
		return input, nil
	}
}

// chainPipeline is source -> a -> b -> c, one executor per node so each
// node's executions can be counted independently.
func chainPipeline(t *testing.T, id, csvPath string) (*models.Pipeline, *controllableExecutor, *controllableExecutor, *controllableExecutor) {
	t.Helper()
	a := &controllableExecutor{nodeType: "node-a", onCall: echoInput()}
	b := &controllableExecutor{nodeType: "node-b", onCall: echoInput()}
	c := &controllableExecutor{nodeType: "node-c", onCall: echoInput()}
	p := &models.Pipeline{
		ID: id, Name: "Resume From Node", Enabled: true,
		Nodes: []models.Node{
			{ID: "source", Type: models.NodeTypeSourceFile, Name: "Source", Config: map[string]interface{}{"path": csvPath, "format": "csv"}},
			{ID: "a", Type: models.NodeType("node-a"), Name: "A"},
			{ID: "b", Type: models.NodeType("node-b"), Name: "B"},
			{ID: "c", Type: models.NodeType("node-c"), Name: "C"},
		},
		Edges: []models.Edge{{From: "source", To: "a"}, {From: "a", To: "b"}, {From: "b", To: "c"}},
	}
	return p, a, b, c
}

// runSnapshot captures everything about a run that a resume must not
// disturb: its own status and the status of every node attempt it
// recorded.
func runSnapshot(t *testing.T, run *models.Run) (models.RunStatus, map[string]models.RunStatus) {
	t.Helper()
	nodes := make(map[string]models.RunStatus, len(run.NodeRuns))
	for _, nr := range run.NodeRuns {
		nodes[nodeRunKey(nr.NodeID, &nr.Attempt)] = nr.Status
	}
	return run.Status, nodes
}

func TestResumeRunFromNode_ReRunsChosenNodeAndItsDescendants(t *testing.T) {
	eng, s := newResumeTestEngine(t)
	dir := t.TempDir()
	csvPath := writeCSV(t, dir, "in.csv", "id,name\n1,brokoli\n2,sql\n")

	pipeline, a, b, c := chainPipeline(t, "p-from-node", csvPath)
	eng.Executors = []extensions.NodeExecutor{a, b, c}
	if err := s.CreatePipeline(pipeline); err != nil {
		t.Fatalf("create pipeline: %v", err)
	}

	first, err := eng.RunPipeline(pipeline.ID)
	if err != nil {
		t.Fatalf("run pipeline: %v", err)
	}
	if first.Status != models.RunStatusSuccess {
		t.Fatalf("first run status = %s, want success (error: %s)", first.Status, first.Error)
	}
	if a.callCount() != 1 || b.callCount() != 1 || c.callCount() != 1 {
		t.Fatalf("after first run a/b/c executed %d/%d/%d, want 1/1/1", a.callCount(), b.callCount(), c.callCount())
	}

	resumed, err := eng.ResumeRunFromNode(first.ID, "b")
	if err != nil {
		t.Fatalf("resume from node: %v", err)
	}
	if resumed.Status != models.RunStatusSuccess {
		t.Fatalf("resumed run status = %s, want success (error: %s)", resumed.Status, resumed.Error)
	}
	if a.callCount() != 1 {
		t.Errorf("a executed %d times, want 1: a node upstream of the chosen one must be reused, not re-executed", a.callCount())
	}
	if b.callCount() != 2 {
		t.Errorf("b executed %d times, want 2: the chosen node must run again", b.callCount())
	}
	if c.callCount() != 2 {
		t.Errorf("c executed %d times, want 2: a node downstream of the chosen one must run again", c.callCount())
	}
	if resumed.ResumedFromRunID != first.ID {
		t.Errorf("ResumedFromRunID = %q, want %q", resumed.ResumedFromRunID, first.ID)
	}
	if resumed.ID == first.ID {
		t.Error("resume returned the same run id: a re-run must append a new run, never reuse the old one")
	}
}

func TestResumeRunFromNode_LeafHasNoDescendants(t *testing.T) {
	eng, s := newResumeTestEngine(t)
	dir := t.TempDir()
	csvPath := writeCSV(t, dir, "in.csv", "id,name\n1,brokoli\n")

	pipeline, a, b, c := chainPipeline(t, "p-from-leaf", csvPath)
	eng.Executors = []extensions.NodeExecutor{a, b, c}
	if err := s.CreatePipeline(pipeline); err != nil {
		t.Fatalf("create pipeline: %v", err)
	}

	first, err := eng.RunPipeline(pipeline.ID)
	if err != nil {
		t.Fatalf("run pipeline: %v", err)
	}
	if first.Status != models.RunStatusSuccess {
		t.Fatalf("first run status = %s, want success (error: %s)", first.Status, first.Error)
	}

	if _, err := eng.ResumeRunFromNode(first.ID, "c"); err != nil {
		t.Fatalf("resume from leaf: %v", err)
	}
	if a.callCount() != 1 || b.callCount() != 1 {
		t.Errorf("a/b executed %d/%d, want 1/1: resuming from a leaf must not re-run anything upstream", a.callCount(), b.callCount())
	}
	if c.callCount() != 2 {
		t.Errorf("c executed %d times, want 2", c.callCount())
	}
}

func TestResumeRunFromNode_RefusesAnUnknownNode(t *testing.T) {
	eng, s := newResumeTestEngine(t)
	dir := t.TempDir()
	csvPath := writeCSV(t, dir, "in.csv", "id,name\n1,brokoli\n")

	pipeline, a, b, c := chainPipeline(t, "p-unknown-node", csvPath)
	eng.Executors = []extensions.NodeExecutor{a, b, c}
	if err := s.CreatePipeline(pipeline); err != nil {
		t.Fatalf("create pipeline: %v", err)
	}
	first, err := eng.RunPipeline(pipeline.ID)
	if err != nil {
		t.Fatalf("run pipeline: %v", err)
	}

	_, err = eng.ResumeRunFromNode(first.ID, "not-a-node")
	if err == nil {
		t.Fatal("resuming from a node that does not exist must be refused")
	}
	if !strings.Contains(err.Error(), "not-a-node") {
		t.Errorf("error must name the node it refused, got: %v", err)
	}
}

func TestResumeRunFromNode_RefusesAnEmptyNodeID(t *testing.T) {
	eng, s := newResumeTestEngine(t)
	dir := t.TempDir()
	csvPath := writeCSV(t, dir, "in.csv", "id,name\n1,brokoli\n")

	pipeline, a, b, c := chainPipeline(t, "p-empty-node", csvPath)
	eng.Executors = []extensions.NodeExecutor{a, b, c}
	if err := s.CreatePipeline(pipeline); err != nil {
		t.Fatalf("create pipeline: %v", err)
	}
	first, err := eng.RunPipeline(pipeline.ID)
	if err != nil {
		t.Fatalf("run pipeline: %v", err)
	}

	// The run id must be real, and the message must be the one the guard
	// produces. An invented run id passes this test even with no guard at
	// all, because the store's "no such run" error arrives first -- which
	// is exactly how the earlier version of this test managed to survive
	// having its guard deleted.
	_, err = eng.ResumeRunFromNode(first.ID, "")
	if err == nil {
		t.Fatal("an empty node id must be refused")
	}
	if !strings.Contains(err.Error(), "node id is required") {
		t.Errorf("refusal must name the missing node id, got: %v", err)
	}
}

func TestResumeRunFromNode_RefusesARunStillInFlight(t *testing.T) {
	eng, s := newResumeTestEngine(t)
	dir := t.TempDir()
	csvPath := writeCSV(t, dir, "in.csv", "id,name\n1,brokoli\n")

	pipeline, a, b, c := chainPipeline(t, "p-inflight", csvPath)
	eng.Executors = []extensions.NodeExecutor{a, b, c}
	if err := s.CreatePipeline(pipeline); err != nil {
		t.Fatalf("create pipeline: %v", err)
	}
	first, err := eng.RunPipeline(pipeline.ID)
	if err != nil {
		t.Fatalf("run pipeline: %v", err)
	}

	// Put the finished run back into each non-terminal status in turn and
	// confirm every one is refused by name. A live run's node outcomes are
	// still being written, so reusing them would race the writer.
	for _, status := range []models.RunStatus{
		models.RunStatusRunning,
		models.RunStatusPending,
		models.RunStatusWaiting,
		models.RunStatusBlocked,
		models.RunStatusSkipped,
	} {
		live, err := s.GetRun(first.ID)
		if err != nil {
			t.Fatalf("get run: %v", err)
		}
		live.Status = status
		if err := s.UpdateRun(live); err != nil {
			t.Fatalf("update run to %s: %v", status, err)
		}
		_, err = eng.ResumeRunFromNode(first.ID, "b")
		if err == nil {
			t.Fatalf("resuming from a node in a %s run must be refused", status)
		}
		if !strings.Contains(err.Error(), string(status)) {
			t.Errorf("refusal for %s must name the status, got: %v", status, err)
		}
	}
}

// TestResumeRunFromNode_AcceptsACancelledRun covers the accepted half of
// the status gate. Every other status test asserts a refusal, so without
// this one the accepted list could be narrowed to nothing observable:
// a cancelled run left settled node outcomes behind, which is the whole
// input a resume reuses, and re-running a branch of it is legitimate.
func TestResumeRunFromNode_AcceptsACancelledRun(t *testing.T) {
	eng, s := newResumeTestEngine(t)
	dir := t.TempDir()
	csvPath := writeCSV(t, dir, "in.csv", "id,name\n1,brokoli\n")

	pipeline, a, b, c := chainPipeline(t, "p-cancelled", csvPath)
	eng.Executors = []extensions.NodeExecutor{a, b, c}
	if err := s.CreatePipeline(pipeline); err != nil {
		t.Fatalf("create pipeline: %v", err)
	}
	first, err := eng.RunPipeline(pipeline.ID)
	if err != nil {
		t.Fatalf("run pipeline: %v", err)
	}

	live, err := s.GetRun(first.ID)
	if err != nil {
		t.Fatalf("get run: %v", err)
	}
	live.Status = models.RunStatusCancelled
	if err := s.UpdateRun(live); err != nil {
		t.Fatalf("mark cancelled: %v", err)
	}

	resumed, err := eng.ResumeRunFromNode(first.ID, "b")
	if err != nil {
		t.Fatalf("a cancelled run must be resumable from a node: %v", err)
	}
	if resumed.Status != models.RunStatusSuccess {
		t.Errorf("resumed run status = %s, want success (error: %s)", resumed.Status, resumed.Error)
	}
	if b.callCount() != 2 {
		t.Errorf("b executed %d times, want 2", b.callCount())
	}
}

func TestResumeRunFromNode_LeavesTheOriginalRunUntouched(t *testing.T) {
	eng, s := newResumeTestEngine(t)
	dir := t.TempDir()
	csvPath := writeCSV(t, dir, "in.csv", "id,name\n1,brokoli\n2,sql\n")

	pipeline, a, b, c := chainPipeline(t, "p-immutable", csvPath)
	eng.Executors = []extensions.NodeExecutor{a, b, c}
	if err := s.CreatePipeline(pipeline); err != nil {
		t.Fatalf("create pipeline: %v", err)
	}
	first, err := eng.RunPipeline(pipeline.ID)
	if err != nil {
		t.Fatalf("run pipeline: %v", err)
	}

	before, err := s.GetRun(first.ID)
	if err != nil {
		t.Fatalf("get run before: %v", err)
	}
	wantStatus, wantNodes := runSnapshot(t, before)

	if _, err := eng.ResumeRunFromNode(first.ID, "b"); err != nil {
		t.Fatalf("resume from node: %v", err)
	}

	after, err := s.GetRun(first.ID)
	if err != nil {
		t.Fatalf("get run after: %v", err)
	}
	gotStatus, gotNodes := runSnapshot(t, after)

	// ADR-028 rejects Airflow's clear-and-mutate: the earlier attempt stays
	// visible exactly as it was.
	if gotStatus != wantStatus {
		t.Errorf("original run status changed from %s to %s", wantStatus, gotStatus)
	}
	if len(gotNodes) != len(wantNodes) {
		t.Fatalf("original run node attempts changed from %d to %d", len(wantNodes), len(gotNodes))
	}
	for key, want := range wantNodes {
		if got := gotNodes[key]; got != want {
			t.Errorf("original run node %q status changed from %s to %s", key, want, got)
		}
	}
}

func TestResumeRunFromNode_RecordsTheChosenNodeAndReplayIgnoresIt(t *testing.T) {
	eng, s := newResumeTestEngine(t)
	dir := t.TempDir()
	csvPath := writeCSV(t, dir, "in.csv", "id,name\n1,brokoli\n")

	pipeline, a, b, c := chainPipeline(t, "p-event", csvPath)
	eng.Executors = []extensions.NodeExecutor{a, b, c}
	if err := s.CreatePipeline(pipeline); err != nil {
		t.Fatalf("create pipeline: %v", err)
	}
	first, err := eng.RunPipeline(pipeline.ID)
	if err != nil {
		t.Fatalf("run pipeline: %v", err)
	}

	resumed, err := eng.ResumeRunFromNode(first.ID, "b")
	if err != nil {
		t.Fatalf("resume from node: %v", err)
	}

	events, err := s.ListEventsByRun(resumed.ID)
	if err != nil {
		t.Fatalf("list events: %v", err)
	}

	found := 0
	for _, ev := range events {
		if ev.EventType != models.RunEventResumedFromNode {
			continue
		}
		found++
		if ev.NodeID != "b" {
			t.Errorf("recorded chosen node = %q, want \"b\"", ev.NodeID)
		}
		if ev.Attempt != nil {
			t.Errorf("the chosen-node event describes a decision, not an execution, so it must carry no attempt; got %d", *ev.Attempt)
		}
	}
	if found != 1 {
		t.Fatalf("found %d chosen-node events, want exactly 1", found)
	}

	// Replay must be unaffected. The event carries no state to project, so
	// folding it in must not invent a node_runs row or change the run's
	// projected status.
	withEvent := ProjectRun(resumed.ID, events)
	filtered := make([]models.RunEvent, 0, len(events))
	for _, ev := range events {
		if ev.EventType == models.RunEventResumedFromNode {
			continue
		}
		filtered = append(filtered, ev)
	}
	without := ProjectRun(resumed.ID, filtered)

	if withEvent.Status != without.Status {
		t.Errorf("projected status differs with the new event (%s) and without it (%s)", withEvent.Status, without.Status)
	}
	if len(withEvent.NodeRuns) != len(without.NodeRuns) {
		t.Errorf("projection produced %d node runs with the new event and %d without: replay must ignore it", len(withEvent.NodeRuns), len(without.NodeRuns))
	}
}

func TestResumeRun_PlainPathStillRefusesASucceededRun(t *testing.T) {
	eng, s := newResumeTestEngine(t)
	dir := t.TempDir()
	csvPath := writeCSV(t, dir, "in.csv", "id,name\n1,brokoli\n")

	pipeline, a, b, c := chainPipeline(t, "p-plain-unchanged", csvPath)
	eng.Executors = []extensions.NodeExecutor{a, b, c}
	if err := s.CreatePipeline(pipeline); err != nil {
		t.Fatalf("create pipeline: %v", err)
	}
	first, err := eng.RunPipeline(pipeline.ID)
	if err != nil {
		t.Fatalf("run pipeline: %v", err)
	}
	if first.Status != models.RunStatusSuccess {
		t.Fatalf("first run status = %s, want success", first.Status)
	}

	// The node-scoped path accepting a succeeded run must not have widened
	// the plain path, which stays failed-only.
	if _, err := eng.ResumeRun(first.ID); err == nil {
		t.Fatal("plain ResumeRun must still refuse a run that succeeded")
	}
}

// TestResumeRunFromNode_ConditionNodeDecidesAgain pins the difference
// between the two resume paths.
//
// On a plain resume a condition node that already succeeded is skipped and
// its durable IR 2.1 routing decision is restored, deliberately: a retry
// must not silently select a different branch. Choosing that condition
// node explicitly is the operator saying the opposite, so it must execute
// again and record a new decision of its own rather than replay the old
// one.
func TestResumeRunFromNode_ConditionNodeDecidesAgain(t *testing.T) {
	eng, s := newResumeTestEngine(t)
	dir := t.TempDir()
	csvPath := writeCSV(t, dir, "in.csv", "id,name\n1,brokoli\n2,sql\n")
	executor := newRoutingTestExecutor()
	eng.Executors = []extensions.NodeExecutor{executor}

	// The conditional shape is basicConditionalPipeline's, but with a real
	// source node: that helper can use a test node for its source only
	// because it runs through NewRunner directly, and Engine.RunPipeline
	// requires a genuine source type.
	pipeline := &models.Pipeline{
		ID: "p-condition-rerun", Name: "Condition Rerun", Enabled: true,
		IRVersion: models.ConditionalEdgesIRVersion,
		Nodes: []models.Node{
			{ID: "source", Type: models.NodeTypeSourceFile, Name: "Source", Config: map[string]interface{}{"path": csvPath, "format": "csv"}},
			{ID: "check", Type: models.NodeTypeCondition, Name: "Check", Config: map[string]interface{}{"expression": "always_true"}},
			{ID: "yes", Type: routingTestNode, Name: "Yes", Config: map[string]interface{}{}},
			{ID: "no", Type: routingTestNode, Name: "No", Config: map[string]interface{}{}},
		},
		Edges: []models.Edge{
			{From: "source", To: "check"},
			{From: "check", To: "yes", Condition: boolPointer(true)},
			{From: "check", To: "no", Condition: boolPointer(false)},
		},
	}
	if err := s.CreatePipeline(pipeline); err != nil {
		t.Fatalf("create pipeline: %v", err)
	}

	first, err := eng.RunPipeline(pipeline.ID)
	if err != nil {
		t.Fatalf("run pipeline: %v", err)
	}
	if first.Status != models.RunStatusSuccess {
		t.Fatalf("first run status = %s, want success (error: %s)", first.Status, first.Error)
	}
	if executor.callCount("yes") != 1 || executor.callCount("no") != 0 {
		t.Fatalf("first run: yes=%d no=%d, want 1/0", executor.callCount("yes"), executor.callCount("no"))
	}

	resumed, err := eng.ResumeRunFromNode(first.ID, "check")
	if err != nil {
		t.Fatalf("resume from condition node: %v", err)
	}
	if resumed.Status != models.RunStatusSuccess {
		t.Fatalf("resumed run status = %s, want success (error: %s)", resumed.Status, resumed.Error)
	}
	// The decision came out the same way, so the inactive branch must
	// still never have run. A re-decided condition that silently flipped
	// would show up here.
	if executor.callCount("no") != 0 {
		t.Errorf("no executed %d times, want 0: the re-made decision selected the same branch", executor.callCount("no"))
	}
	if executor.callCount("yes") != 2 {
		t.Errorf("yes executed %d times, want 2: it is downstream of the chosen node and must run again", executor.callCount("yes"))
	}

	// A restored decision arrives as an AttemptSkipped carrying Reused; a
	// freshly made one as a completed attempt carrying its own
	// ConditionResult. The chosen node must show the latter.
	events, err := s.ListEventsByRun(resumed.ID)
	if err != nil {
		t.Fatalf("list events: %v", err)
	}
	freshDecision := false
	for _, ev := range events {
		if ev.NodeID != "check" {
			continue
		}
		if ev.EventType == models.AttemptSkipped && ev.Payload.Reused {
			t.Error("the chosen condition node was reused from the earlier run instead of deciding again")
		}
		if ev.EventType == models.AttemptCompleted && ev.Payload.ConditionResult != nil {
			freshDecision = true
		}
	}
	if !freshDecision {
		t.Error("the re-executed condition node recorded no new routing decision")
	}
}

// TestDropReusedOutcomes pins the contract that the three maps
// reusableOutcomes produces are cleared together for every node that is
// going to execute again.
//
// This is tested directly rather than only end to end on purpose. Against
// today's call graph only the succeeded deletion has observable effect,
// because the other two maps are subsets of it and are read only behind
// the skipNodes branch a dropped node no longer enters. An end-to-end test
// therefore cannot distinguish the other two deletions from nothing at
// all, which is precisely why the invariant is stated and checked here.
func TestDropReusedOutcomes(t *testing.T) {
	reRun := map[string]bool{"b": true, "c": true}
	succeeded := map[string]bool{"a": true, "b": true, "c": true}
	// "b" decided false. Presence is checked with the two-value form
	// throughout, because a stored false is a real decision and plain
	// truthiness would pass here whether or not the entry was removed.
	conditionResults := map[string]bool{"a": true, "b": false}
	artifactSourceRunIDs := map[string]string{"a": "run-1", "b": "run-1", "c": "run-1"}

	dropReusedOutcomes(reRun, succeeded, conditionResults, artifactSourceRunIDs)

	for _, id := range []string{"b", "c"} {
		if _, ok := succeeded[id]; ok {
			t.Errorf("succeeded still holds %q: it would be restored instead of executing again", id)
		}
		if _, ok := conditionResults[id]; ok {
			t.Errorf("conditionResults still holds %q: a re-executing condition node must not carry the earlier run's routing decision", id)
		}
		if _, ok := artifactSourceRunIDs[id]; ok {
			t.Errorf("artifactSourceRunIDs still holds %q: a re-executing node must not resolve its output to an ancestor run", id)
		}
	}

	// Nodes outside the re-run set keep every entry they had, so this
	// cannot pass by clearing the maps wholesale.
	if _, ok := succeeded["a"]; !ok {
		t.Error("succeeded lost \"a\", which is upstream of the chosen node and must still be reused")
	}
	if _, ok := conditionResults["a"]; !ok {
		t.Error("conditionResults lost \"a\": an untouched condition node keeps its decision")
	}
	if artifactSourceRunIDs["a"] != "run-1" {
		t.Errorf("artifactSourceRunIDs[\"a\"] = %q, want \"run-1\"", artifactSourceRunIDs["a"])
	}
}

func TestDescendantsInclusive(t *testing.T) {
	// A diamond: d is reachable from a by two paths and must appear once.
	// The self-edge and the back edge make this graph cyclic, which
	// Validate refuses at persistence -- the visited set is what stops a
	// corrupted graph from hanging here rather than a case expected to
	// arise.
	pipe := &models.Pipeline{
		Nodes: []models.Node{
			{ID: "root"}, {ID: "a"}, {ID: "b"}, {ID: "c"}, {ID: "d"}, {ID: "island"},
		},
		Edges: []models.Edge{
			{From: "root", To: "a"},
			{From: "a", To: "b"},
			{From: "a", To: "c"},
			{From: "b", To: "d"},
			{From: "c", To: "d"},
			{From: "d", To: "a"},     // back edge
			{From: "b", To: "b"},     // self edge
			{From: "a", To: "ghost"}, // dangling: endpoint not in the node set
			{From: "ghost", To: "a"}, // dangling the other way
		},
	}

	got := descendantsInclusive(pipe, "a")
	want := map[string]bool{"a": true, "b": true, "c": true, "d": true}
	if len(got) != len(want) {
		t.Fatalf("closure from a = %v, want %v", got, want)
	}
	for id := range want {
		if !got[id] {
			t.Errorf("closure from a missing %q", id)
		}
	}
	if got["root"] {
		t.Error("closure must not walk backwards to an upstream node")
	}
	if got["island"] {
		t.Error("closure must not include an unconnected node")
	}
	if got["ghost"] {
		t.Error("closure must not follow an edge to a node that does not exist")
	}

	if leaf := descendantsInclusive(pipe, "d"); !leaf["d"] {
		t.Error("closure must always include the chosen node itself")
	}

	unknown := descendantsInclusive(pipe, "nope")
	if len(unknown) != 1 || !unknown["nope"] {
		t.Errorf("closure from an unknown node = %v, want just itself (the caller checks existence)", unknown)
	}
}
