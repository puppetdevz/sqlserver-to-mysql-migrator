package importer

import (
	"go/ast"
	"go/build"
	"go/parser"
	"go/token"
	"path/filepath"
	"testing"
)

// This regression examines source, not the database. Live DB tests must require
// an explicit integration build tag, so ambient DSNs cannot activate default tests.
func TestDefaultTestsExcludeDatabaseIntegration(t *testing.T) {
	for _, dir := range []string{".", "../source", "../target"} {
		pkg, err := build.Default.ImportDir(dir, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, name := range append(pkg.TestGoFiles, pkg.XTestGoFiles...) {
			file, err := parser.ParseFile(token.NewFileSet(), filepath.Join(dir, name), nil, 0)
			if err != nil {
				t.Fatal(err)
			}
			ast.Inspect(file, func(node ast.Node) bool {
				call, ok := node.(*ast.CallExpr)
				if !ok {
					return true
				}
				selector, ok := call.Fun.(*ast.SelectorExpr)
				if !ok {
					return true
				}
				pkg, ok := selector.X.(*ast.Ident)
				if ok && ((pkg.Name == "sql" && (selector.Sel.Name == "Open" || selector.Sel.Name == "OpenDB")) || (pkg.Name == "database" && selector.Sel.Name == "NewConnection")) {
					t.Errorf("default test %s/%s includes a live database constructor; require integration tag", dir, name)
				}
				return true
			})
		}
	}
}
