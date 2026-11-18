package xwal

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func seedDetachable(t *testing.T) (f *Trunks, dir string, parent, child TrunkID) {
	t.Helper()
	dir = filepath.Join(t.TempDir(), "f")
	f, parent = seedMapTrunk(t, dir)
	for i := range 40 {
		_, lt, err := f.Append(parent, 0, fmt.Appendf(nil, `"p%d"`, i), nil)
		if err != nil {
			t.Fatal(err)
		}
		patch, _ := MapSetPatch([]string{"n"}, fmt.Appendf(nil, `%d`, i))
		if _, err := f.AppendChannel(string(parent), "chalkboard", lt, patch, nil); err != nil {
			t.Fatal(err)
		}
	}
	child, err := f.ForkTail(parent)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := f.Append(child, 0, []byte(`"c0"`), nil); err != nil {
		t.Fatal(err)
	}
	return f, dir, parent, child
}

func scratchLeft(t *testing.T, dir string) []string {
	t.Helper()
	var left []string
	filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err == nil && strings.HasPrefix(d.Name(), ".absorb-") {
			left = append(left, path)
		}
		return nil
	})
	return left
}

// Defends the copy-on-write detach: the copy is built with the topology
// lock free, so a head opens and the node itself takes appends meanwhile.
func TestDetachCopiesWithoutHoldingTheTopologyLock(t *testing.T) {
	f, dir, parent, child := seedDetachable(t)
	node := f.head(string(child))
	f.testDetachPlanned = func(string) {
		worked := make(chan error, 1)
		go func() {
			x, err := f.Head(parent)
			if err != nil {
				worked <- fmt.Errorf("head of another trunk: %w", err)
				return
			}
			x.Close()
			_, _, err = f.Append(child, 0, []byte(`"during"`), nil)
			worked <- err
		}()
		select {
		case err := <-worked:
			if err != nil {
				t.Errorf("during the copy: %v", err)
			}
		case <-time.After(5 * time.Second):
			t.Error("a head or an append waited on the detach's copy: the topology lock is held while copying")
		}
	}
	want := len(mainLTs(t, f, child)) + 1
	if err := f.Detach(node); err != nil {
		t.Fatal(err)
	}
	f.testDetachPlanned = nil
	if got := mainLTs(t, f, child); len(got) != want {
		t.Fatalf("after detach the child reads %d records, want %d (the prefix, its own and the one written mid-copy)", len(got), want)
	}
	if n := f.node(node); n == nil || n.From != "" {
		t.Fatalf("node after detach: %+v, want it detached", n)
	}
	if left := scratchLeft(t, dir); len(left) > 0 {
		t.Fatalf("scratch left behind: %v", left)
	}
}

func TestDetachCancelledPublishesNothing(t *testing.T) {
	f, dir, _, child := seedDetachable(t)
	node := f.head(string(child))
	before := mainLTs(t, f, child)
	from := f.node(node).From
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := f.DetachContext(ctx, node); !errors.Is(err, context.Canceled) {
		t.Fatalf("a cancelled detach returned %v, want context.Canceled", err)
	}
	if n := f.node(node); n == nil || n.From != from {
		t.Fatalf("a cancelled detach moved the node: %+v, want from %q", n, from)
	}
	if m, _ := readNodeMarker(f.irDir(node)); m.from != from {
		t.Fatalf("a cancelled detach rewrote .node: from %q, want %q", m.from, from)
	}
	for _, ch := range []string{"ir", "chalkboard"} {
		if base, err := readForkBaseFile(filepath.Join(dir, ch, fsName(node))); err != nil || base <= 1 {
			t.Fatalf("%s .fork after a cancelled detach: base %d err %v, want the inherited base", ch, base, err)
		}
	}
	if left := scratchLeft(t, dir); len(left) > 0 {
		t.Fatalf("a cancelled detach left scratch: %v", left)
	}
	if got := mainLTs(t, f, child); fmt.Sprint(got) != fmt.Sprint(before) {
		t.Fatalf("a cancelled detach changed what the node reads: %v, want %v", got, before)
	}
	if err := f.Detach(node); err != nil {
		t.Fatalf("detach after a cancelled one: %v", err)
	}
}

func TestDetachOfANodeRemovedMidCopyResurrectsNothing(t *testing.T) {
	f, dir, _, child := seedDetachable(t)
	node := f.head(string(child))
	f.testDetachPlanned = func(string) {
		if err := f.Remove(string(child), false); err != nil {
			t.Errorf("remove during the copy: %v", err)
		}
	}
	if err := f.Detach(node); err == nil {
		t.Fatal("detach of a node removed under it succeeded")
	}
	f.testDetachPlanned = nil
	for _, ch := range []string{"ir", "chalkboard"} {
		if _, err := os.Stat(filepath.Join(dir, ch, fsName(node))); !os.IsNotExist(err) {
			t.Fatalf("%s dir of the removed node after the detach: %v, want it gone", ch, err)
		}
	}
	if f.node(node) != nil {
		t.Fatal("the removed node is back in the index")
	}
}
