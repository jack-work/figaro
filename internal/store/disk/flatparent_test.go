package disk

import (
	"os"
	"path/filepath"
	"testing"
)

// A child log must be able to read its parent's prefix when the parent is NOT
// its directory ancestor. Open walks ".." only when opts.Parent is nil, so the
// nesting is a default, not a requirement.
//
// This is the whole premise of the flat layout: lineage becomes a fact in an
// index rather than a shape on disk, and a fork stops having to move anything
// the parent owns.
func TestChildReadsFlatParentThroughOptsParent(t *testing.T) {
	root := t.TempDir()
	parentDir := filepath.Join(root, "a4f21b9c") // siblings, not nested
	childDir := filepath.Join(root, "e87dd1da")
	for _, d := range []string{parentDir, childDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}

	p, err := Open(parentDir, Options{})
	if err != nil {
		t.Fatal(err)
	}
	for i := uint64(1); i <= 3; i++ {
		if err := p.Write(i, []byte(`{"n":`+string(rune('0'+i))+`}`)); err != nil {
			t.Fatal(err)
		}
	}
	if err := p.Sync(); err != nil {
		t.Fatal(err)
	}

	// The child forks after LT 3 and names its parent explicitly.
	if err := os.WriteFile(filepath.Join(childDir, ".fork"), []byte("base=4\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	c, err := Open(childDir, Options{Parent: p})
	if err != nil {
		t.Fatalf("open flat child: %v", err)
	}
	if got := c.FirstIndex(); got != 1 {
		t.Fatalf("FirstIndex = %d, want 1 (the parent's prefix is not visible)", got)
	}
	if _, err := c.Read(2); err != nil {
		t.Fatalf("child cannot read parent LT 2: %v", err)
	}
	if err := c.Write(4, []byte(`{"n":4}`)); err != nil {
		t.Fatal(err)
	}
	if got := c.LastIndex(); got != 4 {
		t.Fatalf("LastIndex = %d, want 4", got)
	}
	// And the parent is untouched by the child's existence.
	if got := p.LastIndex(); got != 3 {
		t.Fatalf("parent LastIndex = %d, want 3: the fork mutated the parent", got)
	}
}
