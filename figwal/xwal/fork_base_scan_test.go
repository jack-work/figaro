package xwal

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

// One function reads a .fork base. e1 changes the source of a fork base in
// that one place, so a read anywhere else would keep answering from .fork.
// Writers and presence tests may name the file; they are listed here by
// function so a new one is a reviewed change to this test.
func TestOnlyTheAccessorReadsAForkBase(t *testing.T) {
	mayNameTheFile := map[string]bool{
		"forkBaseIn":         true,
		"strictForkBase":     true,
		"writeForkMarker":    true,
		"readForkBase":       true,
		"forkBaseAt":         true,
		"materializeNode":    true,
		"Export":             true,
		"channelDirShape":    true,
		"SweepUnmarkedNodes": true,
	}
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	var bad []string
	for _, name := range files {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		src, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		f, err := parser.ParseFile(fset, name, src, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, decl := range f.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok {
				continue
			}
			ast.Inspect(fn, func(n ast.Node) bool {
				switch n := n.(type) {
				case *ast.SelectorExpr:
					if id, ok := n.X.(*ast.Ident); ok && id.Name == "disk" && n.Sel.Name == "ForkBaseOf" && fn.Name.Name != "forkBaseIn" {
						bad = append(bad, fset.Position(n.Pos()).String()+" "+fn.Name.Name+" calls disk.ForkBaseOf")
					}
				case *ast.BasicLit:
					if n.Kind == token.STRING && n.Value == `".fork"` && !mayNameTheFile[fn.Name.Name] {
						bad = append(bad, fset.Position(n.Pos()).String()+" "+fn.Name.Name+" names .fork")
					}
				}
				return true
			})
		}
	}
	sort.Strings(bad)
	if len(bad) > 0 {
		t.Fatalf("a .fork base is read outside forkBaseIn:\n%s", strings.Join(bad, "\n"))
	}
}
