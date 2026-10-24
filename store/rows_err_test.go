package store

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// A list that stops on a driver error partway through used to come back
// short and without an error: 22 store functions ranged over rows.Next()
// and never asked rows.Err() why it stopped. This reads the package and
// fails for any function that iterates rows without checking it.
func TestEveryRowsLoopChecksRowsErr(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	var missing []string
	loops := 0
	for _, name := range files {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			nexts := map[string]token.Pos{}
			checked := map[string]bool{}
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				sel, ok := call.Fun.(*ast.SelectorExpr)
				if !ok {
					return true
				}
				id, ok := sel.X.(*ast.Ident)
				if !ok {
					return true
				}
				switch sel.Sel.Name {
				case "Next":
					if _, seen := nexts[id.Name]; !seen && len(call.Args) == 0 {
						nexts[id.Name] = call.Pos()
					}
				case "Err":
					checked[id.Name] = true
				}
				return true
			})
			for v, pos := range nexts {
				if v != "rows" && !strings.HasSuffix(v, "Rows") {
					continue
				}
				loops++
				if !checked[v] {
					missing = append(missing, fset.Position(pos).String()+" "+fn.Name.Name)
				}
			}
		}
	}
	if loops == 0 {
		t.Fatal("found no rows loops; this test is no longer looking at the right thing")
	}
	sort.Strings(missing)
	for _, m := range missing {
		t.Errorf("%s iterates rows without checking rows.Err()", m)
	}
}
