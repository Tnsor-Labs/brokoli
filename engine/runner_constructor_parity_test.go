package engine

import (
	"go/ast"
	"go/parser"
	"go/token"
	"sort"
	"testing"
)

// Two functions build a Runner from an Engine: runPipelineAsync (the
// in-process/API path) and ExecuteQueuedRun (the path a --mode worker
// takes for every job it claims). Each copies a list of engine fields
// onto the runner, and the lists have to agree.
//
// They did not. dataCapIssuer was added to runPipelineAsync and missed
// on ExecuteQueuedRun, so a worker's runner had no capability issuer.
// The visible symptom was a task input over the inline row cap being
// refused with "this server issues no data capabilities" -- in exactly
// the deployment where remote dispatch is the only thing that runs, and
// nowhere else. Unit tests and CI were green throughout.
//
// This reads the source rather than exercising behaviour on purpose: the
// failure is an ABSENT assignment, and no behavioural test can be
// written for a line nobody remembered to add. It fails loudly the next
// time someone extends one constructor and not the other.
func TestRunnerConstructorsPropagateTheSameEngineFields(t *testing.T) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "engine.go", nil, 0)
	if err != nil {
		t.Fatalf("parse engine.go: %v", err)
	}

	// assignedFields collects `runner.<field> = e.<something>` targets in
	// one function body.
	assignedFields := func(fn *ast.FuncDecl) map[string]bool {
		out := map[string]bool{}
		ast.Inspect(fn, func(n ast.Node) bool {
			as, ok := n.(*ast.AssignStmt)
			if !ok || len(as.Lhs) != 1 || len(as.Rhs) != 1 {
				return true
			}
			sel, ok := as.Lhs[0].(*ast.SelectorExpr)
			if !ok {
				return true
			}
			recv, ok := sel.X.(*ast.Ident)
			if !ok || recv.Name != "runner" {
				return true
			}
			// Only assignments sourced from the Engine: params and
			// per-run values legitimately differ between the two paths.
			rhs, ok := as.Rhs[0].(*ast.SelectorExpr)
			if !ok {
				return true
			}
			if base, ok := rhs.X.(*ast.Ident); !ok || base.Name != "e" {
				return true
			}
			out[sel.Sel.Name] = true
			return true
		})
		return out
	}

	bodies := map[string]map[string]bool{}
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok {
			continue
		}
		if fn.Name.Name == "runPipelineAsync" || fn.Name.Name == "ExecuteQueuedRun" {
			bodies[fn.Name.Name] = assignedFields(fn)
		}
	}
	for _, name := range []string{"runPipelineAsync", "ExecuteQueuedRun"} {
		if len(bodies[name]) == 0 {
			t.Fatalf("found no `runner.X = e.Y` assignments in %s; this test is no longer looking at the right thing", name)
		}
	}

	missing := func(from, in string) []string {
		var out []string
		for f := range bodies[from] {
			if !bodies[in][f] {
				out = append(out, f)
			}
		}
		sort.Strings(out)
		return out
	}

	if got := missing("runPipelineAsync", "ExecuteQueuedRun"); len(got) > 0 {
		t.Errorf("ExecuteQueuedRun (the worker path) does not propagate %v.\n"+
			"A worker's runner would silently lack these. dataCapIssuer was exactly this bug.", got)
	}
	if got := missing("ExecuteQueuedRun", "runPipelineAsync"); len(got) > 0 {
		t.Errorf("runPipelineAsync does not propagate %v, which ExecuteQueuedRun does", got)
	}
}
