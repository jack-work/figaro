package authz

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// Engineer VII's condition on the grant: the subject that carries a lease
// is built in the functions named here and nowhere else in the daemon. A
// new caller fails this until it is listed, which is the moment to ask
// whether it serves exactly one lease.
func TestOnlyTheLeaseHandlersBuildAProvisionerSubject(t *testing.T) {
	allowed := map[string][]string{
		"internal/angelus/slot_mint.go": {"mintLeased"},
	}
	root := filepath.Join("..", "..")
	found := map[string][]string{}
	fset := token.NewFileSet()
	for _, dir := range []string{"api", "cmd", "internal"} {
		err := filepath.WalkDir(filepath.Join(root, dir), func(path string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return err
			}
			file, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
			if err != nil {
				return err
			}
			rel, _ := filepath.Rel(root, path)
			for _, decl := range file.Decls {
				fn, ok := decl.(*ast.FuncDecl)
				if !ok || fn.Body == nil {
					continue
				}
				ast.Inspect(fn.Body, func(n ast.Node) bool {
					name := ""
					switch x := n.(type) {
					case *ast.SelectorExpr:
						name = x.Sel.Name
					case *ast.Ident:
						name = x.Name
					}
					if name == "ProvisionerServing" && !slices.Contains(found[rel], fn.Name.Name) {
						found[rel] = append(found[rel], fn.Name.Name)
					}
					return true
				})
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	if len(found) == 0 {
		t.Fatal("no function names ProvisionerServing: the scan reads nothing")
	}
	for file, funcs := range found {
		for _, fn := range funcs {
			if !slices.Contains(allowed[file], fn) {
				t.Errorf("%s: %s builds a provisioner subject and is not a lease handler listed here", file, fn)
			}
		}
	}
	for file, funcs := range allowed {
		for _, fn := range funcs {
			if !slices.Contains(found[file], fn) {
				t.Errorf("%s: %s is listed and no longer builds a provisioner subject", file, fn)
			}
		}
	}
}
