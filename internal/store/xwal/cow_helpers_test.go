package xwal

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func nodeDirs(t *testing.T, f *Trunks, node string) []string {
	t.Helper()
	names, err := channelNames(f.root)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, ch := range names {
		if d := filepath.Join(f.root, ch, fsName(node)); pathExists(d) {
			out = append(out, d)
		}
	}
	return out
}

func assertRemovedButHeld(t *testing.T, f *Trunks, trunk, node string, heads int) {
	t.Helper()
	if _, _, err := f.borrowHotUntracked([]string{node}); !errors.Is(err, ErrUnknownTrunk) {
		t.Fatalf("borrowing a removed node: %v, want ErrUnknownTrunk", err)
	}
	if f.hasHead(trunk) {
		t.Fatal("the index still lists the removed trunk")
	}
	if len(nodeDirs(t, f, node)) == 0 {
		t.Fatal("the directories went while a head still reads them")
	}
	pending := f.PendingRemovals()
	if len(pending) != 1 || pending[0].Heads != heads {
		t.Fatalf("pending removals = %+v, want one held by %d head(s)", pending, heads)
	}
}

func assertDrained(t *testing.T, f *Trunks, node string) {
	t.Helper()
	f.drainRemovals()
	if dirs := nodeDirs(t, f, node); len(dirs) != 0 {
		t.Fatalf("directories left after the last head closed: %v", dirs)
	}
	if p := f.PendingRemovals(); len(p) != 0 {
		t.Fatalf("pending removals after drain: %+v", p)
	}
	ents, err := os.ReadDir(f.root)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range ents {
		if len(e.Name()) > len(removalPrefix) && e.Name()[:len(removalPrefix)] == removalPrefix {
			t.Fatalf("removal plan %s left after drain", e.Name())
		}
	}
}
