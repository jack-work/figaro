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

	"github.com/jack-work/figaro/figwal/disk"
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

// The race this defends: the copy runs outside the topology lock, so a
// retention cut can drop the parent's segments after the copy chose where
// the prefix starts and before it reads them. The node still pins the
// segment that holds its last borrowed record, so only the span below it
// goes, and the read must fail on that span rather than copy around it.
// A nil initial makes the copy start at 1, the start it chose before the
// cut. The next detach starts from the floor and lands.
func TestACutUnderTheCopyFailsItAndTheNextDetachLands(t *testing.T) {
	fx := newRetainFixture(t)
	fx.patch(32)
	child := fx.birth()
	fx.patch(4)
	cut, err := fx.f.Retain(fx.formNode(), "chalkboard", false)
	if err != nil {
		t.Fatal(err)
	}
	if len(cut.Dropped) == 0 {
		t.Fatal("the cut dropped nothing; the fixture needs unpinned sealed segments")
	}
	node := fx.f.head(string(child))
	x, err := fx.f.Head(string(child))
	if err != nil {
		t.Fatal(err)
	}
	l, err := x.channel("chalkboard").Log()
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(fx.f.root, "chalkboard", fsName(node))
	scratch, _, err := buildAbsorbed(context.Background(), dir, l, l.ForkBase(), nil, x.codec)
	x.Close()
	if !errors.Is(err, disk.ErrCompacted) {
		os.Remove(scratch)
		t.Fatalf("a copy started before the cut returned %v, want disk.ErrCompacted", err)
	}
	if left := scratchLeft(t, fx.dir); len(left) != 0 {
		t.Fatalf("the failed copy left its scratch: %v", left)
	}
	if err := fx.f.Detach(node); err != nil {
		t.Fatal(err)
	}
	fx.childrenRead("detached after the cut")
}
