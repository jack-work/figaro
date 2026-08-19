package xwal

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A recursive remove must take the whole subtree off disk. The flat layout
// puts every node in its own sibling directory, so deleting the founding
// node alone leaves its descendants behind with a .from nobody can follow:
// the index then reads them as roots, and a listing draws a tree that
// never existed.
func TestRemoveRecursiveTakesTheWholeSubtree(t *testing.T) {
	dir := t.TempDir()
	f, root := seedTrunk(t, dir)

	mid, err := f.ForkTail(root)
	if err != nil {
		t.Fatalf("fork mid: %v", err)
	}
	leaf, err := f.ForkTail(mid)
	if err != nil {
		t.Fatalf("fork leaf: %v", err)
	}

	if err := f.Remove(mid, true); err != nil {
		t.Fatalf("remove %s: %v", mid, err)
	}

	live := map[string]bool{}
	for _, ti := range f.ListLight() {
		live[ti.ID] = true
	}
	for _, id := range []TrunkID{mid, leaf} {
		if live[id] {
			t.Errorf("trunk %s survived a recursive remove", id)
		}
	}
	if !live[root] {
		t.Fatalf("root trunk %s was taken too", root)
	}
	for _, n := range danglingNodes(t, dir, "ir") {
		t.Errorf("node %s survived with a .from nobody can follow", n)
	}
}

// danglingNodes is every node directory in a channel whose .from names a
// directory that is not there.
func danglingNodes(t *testing.T, dir, channel string) []string {
	t.Helper()
	ents, err := os.ReadDir(filepath.Join(dir, channel))
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, e := range ents {
		if !e.IsDir() {
			continue
		}
		b, err := os.ReadFile(filepath.Join(dir, channel, e.Name(), ".node"))
		if err != nil {
			continue
		}
		for _, line := range strings.Split(string(b), "\n") {
			from, ok := strings.CutPrefix(line, "from=")
			if !ok || from == "" {
				continue
			}
			if _, err := os.Stat(filepath.Join(dir, channel, from)); os.IsNotExist(err) {
				out = append(out, e.Name()+" -> "+from)
			}
		}
	}
	return out
}
