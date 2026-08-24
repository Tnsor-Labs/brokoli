// Package netguard defines a static analyzer that enforces the use of
// pkg/netguard for server-initiated outbound HTTP requests.
package netguard

import (
	"go/ast"
	"go/types"
	"strings"

	"golang.org/x/tools/go/analysis"
)

const allowDirective = "netguard:allow"

// Analyzer reports direct uses of net/http clients that bypass pkg/netguard.
var Analyzer = &analysis.Analyzer{
	Name: "netguard",
	Doc:  "require server-side outbound HTTP requests to use pkg/netguard",
	Run:  run,
}

func run(pass *analysis.Pass) (any, error) {
	if isExcludedPackage(pass.Pkg.Path()) {
		return nil, nil
	}
	for _, file := range pass.Files {
		filename := pass.Fset.Position(file.Pos()).Filename
		if strings.HasSuffix(filename, "_test.go") {
			continue
		}

		consumedDirectives := make(map[*ast.Comment]bool)

		ast.Inspect(file, func(node ast.Node) bool {
			switch expression := node.(type) {
			case *ast.CompositeLit:
				if isNetHTTPClient(pass.TypesInfo.TypeOf(expression.Type)) &&
					!consumeAllowDirective(pass, file, expression, consumedDirectives) {
					pass.Reportf(
						expression.Pos(),
						"direct construction of net/http.Client bypasses pkg/netguard",
					)
				}

			case *ast.CallExpr:
				if isNewNetHTTPClient(pass, expression) &&
					!consumeAllowDirective(pass, file, expression, consumedDirectives) {
					pass.Reportf(
						expression.Pos(),
						"direct construction of net/http.Client bypasses pkg/netguard",
					)
				}
			case *ast.SelectorExpr:
				name, blocked := blockedNetHTTPSelector(pass, expression)
				if blocked &&
					!consumeAllowDirective(pass, file, expression, consumedDirectives) {
					pass.Reportf(
						expression.Pos(),
						"direct use of net/http.%s bypasses pkg/netguard",
						name,
					)
				}
			}

			return true
		})
	}

	return nil, nil
}

func isExcludedPackage(packagePath string) bool {
	return strings.HasSuffix(packagePath, "/pkg/netguard") ||
		strings.HasSuffix(packagePath, "/cmd")
}

func consumeAllowDirective(
	pass *analysis.Pass,
	file *ast.File,
	node ast.Node,
	consumed map[*ast.Comment]bool,
) bool {
	targetLine := pass.Fset.Position(node.Pos()).Line

	for _, group := range file.Comments {
		directiveLine := pass.Fset.Position(group.End()).Line
		if directiveLine+1 != targetLine {
			continue
		}

		for _, comment := range group.List {
			if consumed[comment] {
				continue
			}

			text := strings.TrimSpace(strings.TrimPrefix(comment.Text, "//"))
			if !strings.HasPrefix(text, allowDirective) {
				continue
			}

			remainder := text[len(allowDirective):]
			if remainder == "" {
				continue
			}

			if remainder[0] != ' ' && remainder[0] != '\t' {
				continue
			}

			if strings.TrimSpace(remainder) == "" {
				continue
			}

			consumed[comment] = true
			return true
		}
	}
	return false
}

func isNetHTTPClient(typ types.Type) bool {
	if typ == nil {
		return false
	}

	typ = types.Unalias(typ)

	named, ok := typ.(*types.Named)
	if !ok {
		return false
	}

	object := named.Obj()
	return object.Pkg() != nil &&
		object.Pkg().Path() == "net/http" &&
		object.Name() == "Client"
}

func isNewNetHTTPClient(pass *analysis.Pass, call *ast.CallExpr) bool {
	if len(call.Args) != 1 {
		return false
	}

	identifier, ok := call.Fun.(*ast.Ident)
	if !ok {
		return false
	}

	builtin, ok := pass.TypesInfo.Uses[identifier].(*types.Builtin)
	if !ok || builtin.Name() != "new" {
		return false
	}

	return isNetHTTPClient(pass.TypesInfo.TypeOf(call.Args[0]))
}

func blockedNetHTTPSelector(
	pass *analysis.Pass,
	selector *ast.SelectorExpr,
) (string, bool) {
	packageIdentifier, ok := selector.X.(*ast.Ident)
	if !ok {
		return "", false
	}

	packageName, ok := pass.TypesInfo.Uses[packageIdentifier].(*types.PkgName)
	if !ok {
		return "", false
	}

	if packageName.Imported().Path() != "net/http" {
		return "", false
	}

	switch selector.Sel.Name {
	case "DefaultClient", "Get", "Head", "Post", "PostForm":
		return selector.Sel.Name, true
	default:
		return "", false
	}
}
