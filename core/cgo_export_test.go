package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"strings"
	"testing"
)

// ensure all export functions have recoverBoundary to prevent crash
func TestExportedCGOFunctionsRecoverBoundary(t *testing.T) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "cgo_bridge.go", nil, parser.ParseComments)
	if err != nil {
		t.Fatalf("parse cgo_bridge.go: %v", err)
	}

	checked := 0
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Doc == nil {
			continue
		}
		exported := false
		for _, c := range fn.Doc.List {
			if strings.HasPrefix(c.Text, "//export ") {
				exported = true
				break
			}
		}
		if !exported {
			continue
		}
		checked++

		found := false
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			if id, ok := call.Fun.(*ast.Ident); ok && id.Name == "recoverBoundary" {
				found = true
			}
			return true
		})
		if !found {
			t.Errorf("exported function %s lacks recoverBoundary", fn.Name.Name)
		}
	}
	if checked < 30 {
		t.Fatalf("found only %d exported functions; parser or file layout changed", checked)
	}
}
