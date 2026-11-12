package authz

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
)

const atPackageLevel = "(package level)"

// Engineer VII's condition on the grant, widened by El Inspector XV (C2):
// a provisioner subject is built in the functions named here and nowhere
// else in the daemon. The scan follows the four ways to build one: the
// constructor, the principal (a subject literal around it needs no
// constructor), the principal's ID, and the scope, by name or spelled out
// in a string. A new
// user of any of them fails this until it is listed, which is the moment
// to ask whether it serves exactly one lease.
func TestOnlyTheLeaseHandlersBuildAProvisionerSubject(t *testing.T) {
	allowed := map[string]map[string][]string{
		"ProvisionerServing": {
			"internal/angelus/slot_mint.go":    {"mintLeased"},
			"internal/angelus/slot_release.go": {"releaseLease"},
		},
		"ProvisionerPrincipal": {
			"internal/authz/pool.go":        {"ProvisionerServing"},
			"internal/angelus/slot_mint.go": {"poolNodeOf", "mintPoolNodes"},
		},
		"provisionerID": {
			"internal/authz/pool.go": {atPackageLevel, "ProvisionerPrincipal", "IsProvisioner"},
		},
		"PoolScope": {
			"internal/authz/pool.go": {atPackageLevel, "ProvisionerServing", "leaseServed"},
		},
	}
	root := filepath.Join("..", "..")
	found := map[string]map[string][]string{}
	note := func(name, file, where string) {
		if found[name] == nil {
			found[name] = map[string][]string{}
		}
		if !slices.Contains(found[name][file], where) {
			found[name][file] = append(found[name][file], where)
		}
	}
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
				where, body := atPackageLevel, ast.Node(decl)
				if fn, ok := decl.(*ast.FuncDecl); ok {
					if fn.Body == nil {
						continue
					}
					where, body = fn.Name.Name, fn.Body
				}
				ast.Inspect(body, func(n ast.Node) bool {
					switch x := n.(type) {
					case *ast.Ident:
						if _, watched := allowed[x.Name]; watched {
							note(x.Name, rel, where)
						}
					case *ast.BasicLit:
						if text, err := strconv.Unquote(x.Value); err == nil && x.Kind == token.STRING && strings.Contains(text, PoolScope) {
							note("PoolScope", rel, where)
						}
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
	for name, files := range allowed {
		if len(found[name]) == 0 {
			t.Fatalf("nothing names %s: the scan reads nothing", name)
		}
		for file, funcs := range files {
			for _, fn := range funcs {
				if !slices.Contains(found[name][file], fn) {
					t.Errorf("%s: %s is listed for %s and no longer uses it", file, fn, name)
				}
			}
		}
	}
	for name, files := range found {
		for file, funcs := range files {
			for _, fn := range funcs {
				if !slices.Contains(allowed[name][file], fn) {
					t.Errorf("%s: %s uses %s and is not listed here: only the lease handlers build a provisioner subject", file, fn, name)
				}
			}
		}
	}
}
