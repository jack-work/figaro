package xwal

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// The topology snapshot above trusts that one Trunks holds a root: a second
// Trunks' spawn, Detach or Reparent moves no clock the first one reads.
// OpenStore is the only way to a Trunks outside tests, and it takes an
// exclusive flock on the root. These tests defend both halves.

const secondOpenerEnv = "XWAL_SECOND_OPENER_ROOT"

func TestASecondProcessCannotOpenAHeldStore(t *testing.T) {
	if root := os.Getenv(secondOpenerEnv); root != "" {
		s, err := OpenStore(root, testStoreOptions())
		if err == nil {
			s.Close()
			os.Exit(0)
		}
		if strings.Contains(err.Error(), "already has a writer") {
			os.Exit(3)
		}
		os.Exit(4)
	}
	dir := t.TempDir()
	s, err := OpenStore(dir, testStoreOptions())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	cmd := exec.Command(os.Args[0], "-test.run=^TestASecondProcessCannotOpenAHeldStore$")
	cmd.Env = append(os.Environ(), secondOpenerEnv+"="+dir)
	out, err := cmd.CombinedOutput()
	if code := cmd.ProcessState.ExitCode(); code != 3 {
		t.Fatalf("a second process opening a held store exited %d (%v), want 3 (refused as a second writer):\n%s", code, err, out)
	}
}

func TestOnlyOpenStoreMakesATrunks(t *testing.T) {
	allowed := map[string]bool{"OpenStore": true, "PeerTrunksForTest": true}
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
		f, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, decl := range f.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok {
				continue
			}
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				id, ok := call.Fun.(*ast.Ident)
				if ok && (id.Name == "openTrunks" || id.Name == "createTrunks") && !allowed[fn.Name.Name] {
					bad = append(bad, fset.Position(call.Pos()).String()+" "+fn.Name.Name+" calls "+id.Name)
				}
				return true
			})
		}
	}
	sort.Strings(bad)
	if len(bad) > 0 {
		t.Fatalf("a Trunks is made outside OpenStore:\n%s", strings.Join(bad, "\n"))
	}
}
