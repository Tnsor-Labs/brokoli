package dbtmanifest

import (
	"path/filepath"
	"strings"
	"testing"
)

// A real project where one model fails and two sit downstream of it, built
// by real dbt. The shape:
//
//	broken                 -> error
//	downstream_of_broken   -> skipped   (refs broken)
//	two_hops_from_broken   -> skipped   (refs downstream_of_broken)
//	ok_root, unrelated     -> success
//
// Two hops matter: it is what distinguishes naming the nearest cause from
// naming whatever failed anywhere upstream.
const (
	skipManifest   = "skip-manifest-v12.json"
	skipRunResults = "skip-run-results-v6.json"
)

func loadSkipFixture(t *testing.T) ([]Outcome, map[string]Outcome) {
	t.Helper()
	p, err := ParseFile(filepath.Join("testdata", skipManifest))
	if err != nil {
		t.Fatal(err)
	}
	rr, err := ParseRunResultsFile(filepath.Join("testdata", skipRunResults))
	if err != nil {
		t.Fatal(err)
	}
	outcomes := Reconcile(p, rr)
	byName := map[string]Outcome{}
	for _, o := range outcomes {
		byName[o.Node.Name] = o
	}
	return outcomes, byName
}

// The property Phase 1 is for: one model failing does not blind the
// orchestrator to the rest. Every model gets its own outcome from a single
// invocation.
func TestOneInvocationYieldsAnOutcomePerModel(t *testing.T) {
	outcomes, byName := loadSkipFixture(t)

	for _, want := range []struct {
		name   string
		status OutcomeStatus
	}{
		{"broken", OutcomeFailed},
		{"downstream_of_broken", OutcomeSkipped},
		{"two_hops_from_broken", OutcomeSkipped},
		{"ok_root", OutcomeSucceeded},
		{"unrelated", OutcomeSucceeded},
	} {
		got, ok := byName[want.name]
		if !ok {
			t.Errorf("%s has no outcome at all", want.name)
			continue
		}
		if got.Status != want.status {
			t.Errorf("%s: status %q, want %q", want.name, got.Status, want.status)
		}
	}

	s := Summarize(outcomes)
	if s.Failed != 1 {
		t.Errorf("failed = %d, want 1", s.Failed)
	}
	if s.Skipped != 2 {
		t.Errorf("skipped = %d, want 2", s.Skipped)
	}
	if s.Succeeded < 2 {
		t.Errorf("succeeded = %d, want at least the two unrelated models", s.Succeeded)
	}

	// The models that do not depend on the failure carry real durations,
	// which is the evidence they actually ran rather than being reported
	// as an afterthought.
	if ok := byName["unrelated"]; ok.Result == nil || ok.Result.ExecutionTime <= 0 {
		t.Error("unrelated has no execution time; it should have genuinely run")
	}
}

// dbt says "skipped" and stops there. Brokoli has the graph, so it can say
// which model to go and fix -- the difference between a red box and an
// actionable one.
func TestASkipNamesTheFailureThatCausedIt(t *testing.T) {
	_, byName := loadSkipFixture(t)

	direct := byName["downstream_of_broken"]
	if len(direct.BlockedBy) != 1 {
		t.Fatalf("downstream_of_broken blocked by %v, want exactly the model that failed", direct.BlockedBy)
	}
	if !strings.HasSuffix(direct.BlockedBy[0], "broken") {
		t.Errorf("blocked by %q, want broken", direct.BlockedBy[0])
	}

	// Two hops away, the answer is still the failure itself: nothing
	// between them failed, so the nearest cause IS broken.
	twoHops := byName["two_hops_from_broken"]
	if len(twoHops.BlockedBy) != 1 || !strings.HasSuffix(twoHops.BlockedBy[0], "broken") {
		t.Errorf("two_hops_from_broken blocked by %v, want broken", twoHops.BlockedBy)
	}

	// Nothing that ran gets a cause attached.
	for _, name := range []string{"ok_root", "unrelated", "broken"} {
		if got := byName[name]; len(got.BlockedBy) != 0 {
			t.Errorf("%s (%s) has BlockedBy %v; only a skip should", name, got.Status, got.BlockedBy)
		}
	}
}

// The nearest cause, not any cause. With a failure at each of two distances,
// the one a person should look at first is the closer one.
func TestBlockedByNamesTheNearestFailure(t *testing.T) {
	p := &Project{Nodes: map[string]Node{
		"model.p.far":  {UniqueID: "model.p.far", Name: "far"},
		"model.p.near": {UniqueID: "model.p.near", Name: "near", DependsOn: []string{"model.p.far"}},
		"model.p.leaf": {UniqueID: "model.p.leaf", Name: "leaf", DependsOn: []string{"model.p.near"}},
	}}
	rr := &RunResults{Results: []NodeResult{
		{UniqueID: "model.p.far", Status: StatusError},
		{UniqueID: "model.p.near", Status: StatusError},
		{UniqueID: "model.p.leaf", Status: StatusSkipped},
	}}
	byName := map[string]Outcome{}
	for _, o := range Reconcile(p, rr) {
		byName[o.Node.Name] = o
	}
	leaf := byName["leaf"]
	if len(leaf.BlockedBy) != 1 {
		t.Fatalf("leaf blocked by %v, want only the nearest failure", leaf.BlockedBy)
	}
	if !strings.HasSuffix(leaf.BlockedBy[0], "near") {
		t.Errorf("leaf blocked by %q, want near — the immediate dependency is what to go and fix",
			leaf.BlockedBy[0])
	}
}

// A skip whose cause is not in this invocation names nothing, rather than
// reaching for a plausible-looking model.
func TestSkipWithNoKnownCauseNamesNothing(t *testing.T) {
	p := &Project{Nodes: map[string]Node{
		"model.p.a": {UniqueID: "model.p.a", Name: "a", DependsOn: []string{"model.other.x"}},
	}}
	rr := &RunResults{Results: []NodeResult{{UniqueID: "model.p.a", Status: StatusSkipped}}}
	got := Reconcile(p, rr)
	if len(got) != 1 {
		t.Fatalf("outcomes = %d", len(got))
	}
	if got[0].Status != OutcomeSkipped {
		t.Errorf("status = %q", got[0].Status)
	}
	if len(got[0].BlockedBy) != 0 {
		t.Errorf("BlockedBy = %v, want nothing — the cause is outside this invocation", got[0].BlockedBy)
	}
}

// A model the invocation never covered is not a skip. Presenting it as one
// would imply a failure that did not happen.
func TestNotSelectedIsDistinctFromSkipped(t *testing.T) {
	p, err := ParseFile(filepath.Join("testdata", skipManifest))
	if err != nil {
		t.Fatal(err)
	}
	// An invocation that ran only one model, as `--select` would produce.
	rr := &RunResults{Results: []NodeResult{}}
	for id := range p.Nodes {
		if strings.HasSuffix(id, "ok_root") {
			rr.Results = append(rr.Results, NodeResult{UniqueID: id, Status: StatusSuccess})
			break
		}
	}
	outcomes := Reconcile(p, rr)
	s := Summarize(outcomes)
	if s.Succeeded != 1 {
		t.Errorf("succeeded = %d, want 1", s.Succeeded)
	}
	if s.Skipped != 0 {
		t.Errorf("skipped = %d, want 0 — nothing was skipped, the rest was never asked for", s.Skipped)
	}
	if s.NotSelected != len(p.Nodes)-1 {
		t.Errorf("not selected = %d, want %d", s.NotSelected, len(p.Nodes)-1)
	}
	for _, o := range outcomes {
		if o.Status == OutcomeNotSelected && o.Result != nil {
			t.Errorf("%s was not selected but carries a result", o.Node.Name)
		}
	}
}

// A status this build does not know must not be read as success. Treating
// an unrecognised outcome as "it worked" is the one guess that loses data.
func TestUnknownStatusIsTreatedAsFailure(t *testing.T) {
	p := &Project{Nodes: map[string]Node{"model.p.a": {UniqueID: "model.p.a", Name: "a"}}}
	rr := &RunResults{Results: []NodeResult{{UniqueID: "model.p.a", Status: NodeStatus("something-dbt-added-in-2030")}}}
	got := Reconcile(p, rr)
	if got[0].Status != OutcomeFailed {
		t.Errorf("status = %q, want failed — an unrecognised outcome must not read as success", got[0].Status)
	}
}

// Ordering must not depend on Go's map iteration, or the pipeline's node
// list would vary between runs of one project.
func TestReconcileOrderIsStable(t *testing.T) {
	p, err := ParseFile(filepath.Join("testdata", skipManifest))
	if err != nil {
		t.Fatal(err)
	}
	rr, err := ParseRunResultsFile(filepath.Join("testdata", skipRunResults))
	if err != nil {
		t.Fatal(err)
	}
	first := Reconcile(p, rr)
	for i := 0; i < 20; i++ {
		again := Reconcile(p, rr)
		if len(again) != len(first) {
			t.Fatalf("outcome count changed: %d then %d", len(first), len(again))
		}
		for j := range first {
			if first[j].Node.UniqueID != again[j].Node.UniqueID {
				t.Fatalf("order is not stable at %d: %s then %s",
					j, first[j].Node.UniqueID, again[j].Node.UniqueID)
			}
		}
	}
}

// Reconciling runs once per invocation, over every node in the project.
func BenchmarkReconcile(b *testing.B) {
	p, err := ParseFile(filepath.Join("testdata", skipManifest))
	if err != nil {
		b.Fatal(err)
	}
	rr, err := ParseRunResultsFile(filepath.Join("testdata", skipRunResults))
	if err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		if got := Reconcile(p, rr); len(got) == 0 {
			b.Fatal("no outcomes")
		}
	}
}
