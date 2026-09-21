package models

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"sort"
	"strings"
	"testing"
)

// AllNodeTypes must list every NodeType constant this package declares.
//
// The list's whole purpose is to be the one place a gate can iterate, so
// a list that falls behind the constants defeats every gate built on it.
// Rather than trust a reviewer to notice, this parses the package source
// and compares.
//
// Reading the source rather than using reflection is deliberate: Go has
// no way to enumerate the constants of a named type at runtime, so the
// declarations themselves are the only available source of truth.
func TestAllNodeTypesIsComplete(t *testing.T) {
	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, ".", func(os.FileInfo) bool { return true }, 0)
	if err != nil {
		t.Fatalf("parsing this package: %v", err)
	}

	declared := map[string]bool{}
	for name, pkg := range pkgs {
		if strings.HasSuffix(name, "_test") {
			continue
		}
		for path, file := range pkg.Files {
			if strings.HasSuffix(path, "_test.go") {
				continue
			}
			ast.Inspect(file, func(n ast.Node) bool {
				spec, ok := n.(*ast.ValueSpec)
				if !ok {
					return true
				}
				// Only constants whose declared type is NodeType. A
				// NodeType-typed var, or a string constant that merely
				// looks like one, is not a node type.
				ident, ok := spec.Type.(*ast.Ident)
				if !ok || ident.Name != "NodeType" {
					return true
				}
				for _, name := range spec.Names {
					declared[name.Name] = true
				}
				return true
			})
		}
	}
	if len(declared) == 0 {
		t.Fatal("found no NodeType constants at all; this test would pass for an empty list")
	}

	// The values in AllNodeTypes, mapped back to the constant names by
	// value, so the comparison is on names a reader can act on.
	byValue := map[NodeType]string{}
	for name := range declared {
		byValue[nodeTypeValue(t, name)] = name
	}

	listed := map[string]bool{}
	for _, nt := range AllNodeTypes {
		name, ok := byValue[nt]
		if !ok {
			t.Errorf("AllNodeTypes contains %q, which matches no NodeType constant", nt)
			continue
		}
		if listed[name] {
			t.Errorf("AllNodeTypes lists %s twice", name)
		}
		listed[name] = true
	}

	var missing []string
	for name := range declared {
		if !listed[name] {
			missing = append(missing, name)
		}
	}
	sort.Strings(missing)
	if len(missing) > 0 {
		t.Errorf("%d NodeType constant(s) are not in AllNodeTypes: %v\n\n"+
			"Add them. Every gate that iterates node types -- column lineage "+
			"coverage among them -- silently skips whatever is missing here.",
			len(missing), missing)
	}
}

// nodeTypeValue resolves a constant name to its value.
//
// A switch rather than reflection, because the values are what the test
// is checking and reading them back through the same list under test
// would be circular. This one does have to be maintained by hand, and
// the test above fails loudly when it is not: a new constant lands in
// `declared`, finds no value here, and reports as missing.
func nodeTypeValue(t *testing.T, name string) NodeType {
	t.Helper()
	all := map[string]NodeType{
		"NodeTypeSourceFile": NodeTypeSourceFile, "NodeTypeSourceAPI": NodeTypeSourceAPI,
		"NodeTypeSourceDB": NodeTypeSourceDB, "NodeTypeTransform": NodeTypeTransform,
		"NodeTypeProject": NodeTypeProject, "NodeTypeAggregate": NodeTypeAggregate,
		"NodeTypeQualityCheck": NodeTypeQualityCheck, "NodeTypeSQLGenerate": NodeTypeSQLGenerate,
		"NodeTypeCode": NodeTypeCode, "NodeTypeJoin": NodeTypeJoin,
		"NodeTypeSinkFile": NodeTypeSinkFile, "NodeTypeSinkDB": NodeTypeSinkDB,
		"NodeTypeSinkAPI": NodeTypeSinkAPI, "NodeTypeMigrate": NodeTypeMigrate,
		"NodeTypeCondition": NodeTypeCondition, "NodeTypeWait": NodeTypeWait,
		"NodeTypeDBT": NodeTypeDBT, "NodeTypeNotify": NodeTypeNotify,
		"NodeTypeUnion": NodeTypeUnion, "NodeTypeDatasetMap": NodeTypeDatasetMap,
		"NodeTypeDatasetFilter": NodeTypeDatasetFilter, "NodeTypeTask": NodeTypeTask,
	}
	v, ok := all[name]
	if !ok {
		t.Errorf("the constant %s is new: add it to nodeTypeValue and to AllNodeTypes", name)
		return NodeType("")
	}
	return v
}

func TestIsKnownNodeType(t *testing.T) {
	for _, nt := range AllNodeTypes {
		if !IsKnownNodeType(nt) {
			t.Errorf("IsKnownNodeType(%q) = false for a listed type", nt)
		}
	}
	for _, nt := range []NodeType{"", "not_a_node", "Transform", "transform "} {
		if IsKnownNodeType(nt) {
			t.Errorf("IsKnownNodeType(%q) = true", nt)
		}
	}
}
