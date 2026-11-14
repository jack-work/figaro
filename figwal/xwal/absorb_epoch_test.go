package xwal

import (
	"path/filepath"
	"testing"

	"github.com/jack-work/figaro/figwal/log"
)

// A finished absorb changes a node's base, and a lineage snapshot keyed on
// the root epoch must see it. Both ways an open finishes one move the epoch:
// the handle that writes base=1, and a handle that opened under the old base
// and finds a Detach (or another repair) already wrote it.
func TestEveryFinishedAbsorbMovesTheTopology(t *testing.T) {
	dir, parentNode, node := interruptedImageWithAppends(t)
	cfg := rootDetachOptions().config()
	rx, err := Open(dir, cfg)
	if err != nil {
		t.Fatal(err)
	}
	state := rx.channel("state")
	opts, initial, codec := rx.channelOpts(state), state.absorbInitial(), rx.codec
	if err := rx.Close(); err != nil {
		t.Fatal(err)
	}
	open := func() *log.Log {
		p, err := log.Open(filepath.Join(dir, "state", fsName(parentNode)), opts)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { p.Close() })
		o := opts
		o.Parent = p.Disk()
		l, err := log.Open(filepath.Join(dir, "state", fsName(node)), o)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { l.Close() })
		if segs := l.SegmentBaseIndexes(); len(segs) == 0 || segs[0] >= l.ForkBase() {
			t.Fatalf("a handle opened %v under base %d, want the interrupted layout", segs, l.ForkBase())
		}
		return l
	}
	writer, adopter := open(), open()
	nodeDir := filepath.Join(dir, "state", fsName(node))
	moves := 0
	moved := func() { moves++ }

	if err := finishInterruptedAbsorb(nodeDir, writer, initial, codec, moved); err != nil {
		t.Fatal(err)
	}
	if base, err := readForkBaseStrict(nodeDir); err != nil || base != 1 {
		t.Fatalf("after the repair .fork is base %d (%v), want 1", base, err)
	}
	if moves != 1 {
		t.Fatalf("the repair that wrote base=1 moved the topology %d times, want 1", moves)
	}

	if err := finishInterruptedAbsorb(nodeDir, adopter, initial, codec, moved); err != nil {
		t.Fatal(err)
	}
	if moves != 2 {
		t.Fatalf("the handle that adopted a finished absorb moved the topology %d times in all, want 2", moves)
	}

	if err := finishInterruptedAbsorb(nodeDir, adopter, initial, codec, moved); err != nil {
		t.Fatal(err)
	}
	if moves != 2 {
		t.Fatalf("an open with nothing to finish moved the topology (%d moves, want 2)", moves)
	}
}
