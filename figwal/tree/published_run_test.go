package tree

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A published run is read without the cache lock, so no code may assign
// its units, residency or pin after construction. Construction uses
// composite literals; any assignment through a selector is a write to a
// run someone may already hold.
func TestNoAssignmentToPublishedRunFields(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	guarded := map[string]bool{"units": true, "resident": true, "pinned": true}
	fset := token.NewFileSet()
	scanned := 0
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
		scanned++
		ast.Inspect(f, func(n ast.Node) bool {
			as, ok := n.(*ast.AssignStmt)
			if !ok {
				return true
			}
			for _, lhs := range as.Lhs {
				if sel, ok := lhs.(*ast.SelectorExpr); ok && guarded[sel.Sel.Name] {
					t.Errorf("%s: assignment to .%s outside a run's construction", fset.Position(sel.Pos()), sel.Sel.Name)
				}
			}
			return true
		})
	}
	if scanned == 0 {
		t.Fatal("scanned no source files")
	}
}

// DropNode hands back exactly the bytes the dropped runs were charged.
func TestDropNodeReturnsTheBudgetToItsLevelBeforeTheRead(t *testing.T) {
	b := NewBudget(0)
	c := New(func(co Coord) ([]unit, error) {
		var out []unit
		for k := co.From + 1; k <= co.To; k++ {
			out = append(out, unit{k: k, s: strings.Repeat("x", 100)})
		}
		return out, nil
	}, b, func(u unit) int { return len(u.s) }, func(u unit) uint64 { return u.k })
	other := c.Node("q")
	if _, err := other.Range(0, 10); err != nil {
		t.Fatal(err)
	}
	before, _, _ := b.Stats()
	h := c.Node("p")
	if _, err := h.Range(0, 200); err != nil {
		t.Fatal(err)
	}
	if mid, _, _ := b.Stats(); mid <= before {
		t.Fatalf("the read charged nothing: %d -> %d", before, mid)
	}
	c.DropNode("p")
	if after, _, _ := b.Stats(); after != before {
		t.Fatalf("budget %d after DropNode, want %d", after, before)
	}
	c.DropNode("p")
	if again, _, _ := b.Stats(); again != before {
		t.Fatalf("a second DropNode moved the budget to %d, want %d", again, before)
	}
}
