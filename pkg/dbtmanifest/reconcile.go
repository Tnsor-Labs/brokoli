package dbtmanifest

import "sort"

// Turning one dbt invocation into per-model outcomes (ADR-025, #353).
//
// #353 measured that invoking dbt per model costs 30x a single build, and
// found that it is unnecessary: one invocation reports every node
// individually. This is the join that turns that file into something the
// engine can hang a node run on.
//
// dbt already does the hard part correctly. Given a project where `broken`
// fails and `downstream_of_broken` refs it, a single `dbt build` reports:
//
//	broken                 error
//	downstream_of_broken   skipped
//	ok_root                success
//	unrelated              success
//
// So "a failed model fails its node, and models that do not depend on it
// still run" is dbt's behaviour, reported per node. What dbt does NOT say is
// *why* something was skipped -- the file records the status and nothing
// linking it to the failure that caused it. Brokoli has the dependency graph
// from the manifest, so it can answer that, which is the difference between
// showing someone a red box and telling them which model to go fix.

// OutcomeStatus is what happened to one model, from Brokoli's point of view
// rather than dbt's per-resource vocabulary.
type OutcomeStatus string

const (
	// OutcomeSucceeded covers dbt's success and pass.
	OutcomeSucceeded OutcomeStatus = "succeeded"
	// OutcomeFailed covers error, fail and runtime error.
	OutcomeFailed OutcomeStatus = "failed"
	// OutcomeWarned is a test dbt flagged without failing the run. Kept
	// separate because collapsing it into either neighbour loses the
	// thing dbt's severity model exists to express.
	OutcomeWarned OutcomeStatus = "warned"
	// OutcomeSkipped is a node dbt chose not to run, which in practice
	// means something it depends on failed. BlockedBy says which.
	OutcomeSkipped OutcomeStatus = "skipped"
	// OutcomeNotSelected is a node in the project that this invocation was
	// never asked to run. Distinct from skipped: nothing went wrong, it
	// simply was not in scope, and presenting it as a skip would imply a
	// failure that did not happen.
	OutcomeNotSelected OutcomeStatus = "not_selected"
)

// Outcome is one model's result, ready to become a node run.
type Outcome struct {
	Node Node
	// Result is what dbt reported, or nil when this invocation did not
	// cover the node.
	Result *NodeResult
	Status OutcomeStatus
	// BlockedBy names the failed nodes that explain a skip, nearest cause
	// first by dependency distance. Empty for anything else, and empty for
	// a skip whose cause is outside this invocation -- which is honest:
	// better to say nothing than to name the wrong model.
	BlockedBy []string
}

// Reconcile joins a manifest against one invocation's results, producing an
// outcome for every node in the project.
//
// Every node gets an entry, including ones dbt never reported. A caller
// building pipeline nodes needs to know a model exists and was not run,
// which is different information from it not existing.
func Reconcile(p *Project, rr *RunResults) []Outcome {
	byID := map[string]NodeResult{}
	if rr != nil {
		byID = rr.ByUniqueID()
	}

	failed := map[string]bool{}
	for id, res := range byID {
		if res.Status.Failed() {
			failed[id] = true
		}
	}

	out := make([]Outcome, 0, len(p.Nodes))
	ids := make([]string, 0, len(p.Nodes))
	for id := range p.Nodes {
		ids = append(ids, id)
	}
	// Stable order, so a pipeline's node list does not vary between runs
	// of the same project.
	sort.Strings(ids)

	for _, id := range ids {
		n := p.Nodes[id]
		o := Outcome{Node: n}
		res, reported := byID[id]
		if !reported {
			o.Status = OutcomeNotSelected
			out = append(out, o)
			continue
		}
		o.Result = &res
		switch {
		case res.Status.Succeeded():
			o.Status = OutcomeSucceeded
		case res.Status.Failed():
			o.Status = OutcomeFailed
		case res.Status == StatusWarn:
			o.Status = OutcomeWarned
		case res.Status == StatusSkipped:
			o.Status = OutcomeSkipped
			o.BlockedBy = blockingFailures(p, id, failed)
		default:
			// A status this build does not recognise. Treating it as a
			// success would be the dangerous guess; treating it as a
			// failure is the safe one, and it will be visible.
			o.Status = OutcomeFailed
		}
		out = append(out, o)
	}
	return out
}

// blockingFailures finds the failed nodes upstream of id, nearest first.
//
// Breadth-first, so the model a person should look at -- the immediate
// dependency that failed -- comes before whatever failed further back. A
// skip caused by something outside this invocation returns nothing rather
// than reaching for a plausible-looking cause.
func blockingFailures(p *Project, id string, failed map[string]bool) []string {
	if len(failed) == 0 {
		return nil
	}
	var found []string
	seen := map[string]bool{id: true}
	frontier := append([]string(nil), p.Nodes[id].DependsOn...)
	sort.Strings(frontier)

	for len(frontier) > 0 {
		var next []string
		for _, dep := range frontier {
			if seen[dep] {
				continue
			}
			seen[dep] = true
			if failed[dep] {
				// Found a cause at this distance. Do not walk past it:
				// whatever that model depends on is its problem, not this
				// node's explanation.
				found = append(found, dep)
				continue
			}
			if n, ok := p.Nodes[dep]; ok {
				next = append(next, n.DependsOn...)
			}
		}
		if len(found) > 0 {
			sort.Strings(found)
			return found
		}
		sort.Strings(next)
		frontier = next
	}
	return nil
}

// Summary counts outcomes by status, for a one-line report of what an
// invocation did.
type Summary struct {
	Succeeded   int
	Failed      int
	Warned      int
	Skipped     int
	NotSelected int
}

// Summarize counts a set of outcomes.
func Summarize(outcomes []Outcome) Summary {
	var s Summary
	for _, o := range outcomes {
		switch o.Status {
		case OutcomeSucceeded:
			s.Succeeded++
		case OutcomeFailed:
			s.Failed++
		case OutcomeWarned:
			s.Warned++
		case OutcomeSkipped:
			s.Skipped++
		case OutcomeNotSelected:
			s.NotSelected++
		}
	}
	return s
}
