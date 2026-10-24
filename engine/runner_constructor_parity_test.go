package engine

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// Every run builds its Runner in newRunRunner, which gives it everything
// the engine provides to a run. This reads the source on purpose: the
// failure it guards against is an ABSENT assignment, which no behavioural
// test can be written for.
//
// History, twice over. First, dataCapIssuer was added to the async path
// and missed on ExecuteQueuedRun, so a worker refused large task inputs
// with "this server issues no data capabilities"; a test then compared
// those two constructors. It did not look at the other three, and the same
// field was missing from RunPipelineOpts (scheduled, backfill, webhook and
// trigger runs), resumeRun and WakeParkedRun; WakeParkedRun also skipped
// configureRunner. Comparing copies cannot keep up with new copies, so
// there is now one copy and this test keeps it the only one.
func TestRunnersAreBuiltInOnePlace(t *testing.T) {
	fset := token.NewFileSet()
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	// preview builds a Runner over a scratch store and deliberately takes
	// only configureRunner: a preview writes nothing, so it gets no
	// artifact store, checkpoint store or capability issuer.
	allowedNewRunner := map[string]bool{"newRunRunner": true, "PreviewPipeline": true}
	var newRunnerCalls, engineCopies, issuerInHelper int
	var problems []string
	for _, name := range files {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		src, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		file, err := parser.ParseFile(fset, name, src, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				switch x := n.(type) {
				case *ast.CallExpr:
					if id, ok := x.Fun.(*ast.Ident); ok && id.Name == "NewRunner" {
						newRunnerCalls++
						if !allowedNewRunner[fn.Name.Name] {
							problems = append(problems, fset.Position(x.Pos()).String()+": "+fn.Name.Name+" calls NewRunner; build the runner with e.newRunRunner")
						}
					}
				case *ast.AssignStmt:
					if len(x.Lhs) != 1 || len(x.Rhs) != 1 {
						return true
					}
					lhs, ok := x.Lhs[0].(*ast.SelectorExpr)
					if !ok {
						return true
					}
					if recv, ok := lhs.X.(*ast.Ident); !ok || recv.Name != "runner" {
						return true
					}
					if !fromEngine(x.Rhs[0]) {
						return true
					}
					if fn.Name.Name == "newRunRunner" {
						engineCopies++
						if lhs.Sel.Name == "dataCapIssuer" {
							issuerInHelper++
						}
						return true
					}
					problems = append(problems, fset.Position(x.Pos()).String()+": "+fn.Name.Name+" copies an engine field onto its runner (runner."+lhs.Sel.Name+"); do that in newRunRunner so every run gets it")
				}
				return true
			})
		}
	}
	if newRunnerCalls == 0 || engineCopies == 0 {
		t.Fatalf("found %d NewRunner calls and %d engine copies in newRunRunner; this test is no longer looking at the right thing", newRunnerCalls, engineCopies)
	}
	if issuerInHelper != 1 {
		t.Errorf("newRunRunner sets dataCapIssuer %d times, want 1", issuerInHelper)
	}
	sort.Strings(problems)
	for _, p := range problems {
		t.Error(p)
	}
}

// fromEngine reports whether an expression reads the engine: e.X or e.f().
func fromEngine(expr ast.Expr) bool {
	if call, ok := expr.(*ast.CallExpr); ok {
		expr = call.Fun
	}
	sel, ok := expr.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	base, ok := sel.X.(*ast.Ident)
	return ok && base.Name == "e"
}
