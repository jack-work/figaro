package xwal

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

// A successor's turn-1 fork lands under the first holder; moving it under the
// successor copies nothing and changes nothing it reads.
func TestPlacingMovesTheAuthorityParentAndLeavesTheForkPoint(t *testing.T) {
	f, trunk := seedTrunk(t, filepath.Join(t.TempDir(), "f"))
	appendAll := func(id string, ps ...string) {
		for _, p := range ps {
			if _, _, err := f.Append(id, 0, []byte(`"`+p+`"`), nil); err != nil {
				t.Fatal(err)
			}
		}
	}
	appendAll(trunk, "a", "b")
	h0, err := f.ForkAt(trunk, 1)
	if err != nil {
		t.Fatal(err)
	}
	appendAll(h0, "h0a", "h0b")
	h1, err := f.ForkAt(h0, 2)
	if err != nil {
		t.Fatal(err)
	}
	appendAll(h1, "h1")
	lieutenant, err := f.ForkAt(h1, 1)
	if err != nil {
		t.Fatal(err)
	}
	key := func(id string) string { k, _ := f.idx.Head(id); return k }
	forkPoint := f.idx.ParentOf(key(lieutenant))
	if forkPoint == key(h1) {
		t.Fatal("setup: the turn-1 fork was already cut in the successor")
	}
	before := mainPayloads(t, f, lieutenant)

	walks := f.idx.rebuilds.Load()
	if err := f.Place(key(lieutenant), key(h1)); err != nil {
		t.Fatalf("place: %v", err)
	}
	if f.idx.rebuilds.Load() != walks {
		t.Fatal("a placement walked every marker in the store")
	}
	check := func(when string) {
		t.Helper()
		if got := f.idx.PlacementOf(key(lieutenant)); got != key(h1) {
			t.Fatalf("%s: placement = %q, want %q", when, got, key(h1))
		}
		if got := f.idx.ParentOf(key(lieutenant)); got != forkPoint {
			t.Fatalf("%s: fork point = %q, want %q: a placement rewrote it", when, got, forkPoint)
		}
	}
	check("after the placement")
	if err := f.Refresh(); err != nil {
		t.Fatal(err)
	}
	check("after a rescan")
	if after := mainPayloads(t, f, lieutenant); !slices.Equal(before, after) {
		t.Fatalf("history changed: %v -> %v", before, after)
	}
	for _, ti := range f.List() {
		if ti.ID == lieutenant && (ti.Placed != h1 || ti.Parent == h1) {
			t.Fatalf("listing: placed %q, forked from %q; want placed %q and a fork point that is not it", ti.Placed, ti.Parent, h1)
		}
	}

	deep, err := f.ForkAt(h0, 3)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Place(key(deep), key(h1)); !errors.Is(err, ErrMoveCopies) {
		t.Fatalf("a placement under a node that lacks the fork point: err = %v, want ErrMoveCopies", err)
	}
	if err := f.Place(key(h1), key(lieutenant)); !errors.Is(err, ErrMoveCycle) {
		t.Fatalf("placing a node under one placed beneath it: err = %v, want ErrMoveCycle", err)
	}

	x, err := f.ForkAt(trunk, 2)
	if err != nil {
		t.Fatal(err)
	}
	y, err := f.ForkAt(trunk, 2)
	if err != nil {
		t.Fatal(err)
	}
	if got := f.idx.ParentOf(key(x)); got != key(trunk) {
		t.Fatalf("setup: x was cut from %q, want the trunk %q", got, key(trunk))
	}
	if err := f.Place(key(x), key(y)); err != nil {
		t.Fatalf("placing x under a sibling cut at the same point: %v", err)
	}
	if err := f.Place(key(x), key(trunk)); err != nil {
		t.Fatalf("placing x back under its fork point: %v", err)
	}
	if n, _ := f.idx.Node(key(x)); n.Placed != "" || n.PlacedTop {
		t.Fatalf("a node placed where it was forked keeps a record: %+v", n)
	}
}

func TestDetachKeepsThePlacementAndUnplaceDropsIt(t *testing.T) {
	f, trunk := seedTrunk(t, filepath.Join(t.TempDir(), "f"))
	for _, p := range []string{`"a"`, `"b"`} {
		if _, _, err := f.Append(trunk, 0, []byte(p), nil); err != nil {
			t.Fatal(err)
		}
	}
	child, err := f.ForkAt(trunk, 2)
	if err != nil {
		t.Fatal(err)
	}
	key := func(id string) string { k, _ := f.idx.Head(id); return k }
	if got := f.idx.ParentOf(key(child)); got != key(trunk) {
		t.Fatalf("setup: the child was cut from %q, want the trunk %q", got, key(trunk))
	}
	if err := f.Detach(key(child)); err != nil {
		t.Fatal(err)
	}
	if got := f.idx.ParentOf(key(child)); got != "" {
		t.Fatalf("fork point after a detach = %q, want none", got)
	}
	if got := f.idx.PlacementOf(key(child)); got != key(trunk) {
		t.Fatalf("placement after a detach = %q, want %q: owning its history must not move a node", got, key(trunk))
	}
	if err := f.Unplace(key(child)); err != nil {
		t.Fatal(err)
	}
	if err := f.Refresh(); err != nil {
		t.Fatal(err)
	}
	if got := f.idx.PlacementOf(key(child)); got != "" {
		t.Fatalf("placement after unplace = %q, want none", got)
	}
}

func TestAPlacementWhoseNodeIsGoneIsReportedLost(t *testing.T) {
	f, trunk := seedTrunk(t, filepath.Join(t.TempDir(), "f"))
	if _, _, err := f.Append(trunk, 0, []byte(`"a"`), nil); err != nil {
		t.Fatal(err)
	}
	child, err := f.ForkAt(trunk, 1)
	if err != nil {
		t.Fatal(err)
	}
	k, _ := f.idx.Head(child)
	n, _ := f.idx.Node(k)
	if err := writeNodeMarker(f.irDir(k), n.marker().placedUnder("gone")); err != nil {
		t.Fatal(err)
	}
	if err := f.Refresh(); err != nil {
		t.Fatal(err)
	}
	ti, ok := f.InfoLight(child)
	if !ok || ti.PlacedLost != "gone" || ti.Placed != "" {
		t.Fatalf("info = %+v, want the placement reported lost, never read as the fork point", ti)
	}
}

// Real stores hold nodes whose fork point names no node, and 0.65.4 reads
// them as top-level. With no placement recorded they still read so: only a
// recorded placement can be lost.
func TestAForkPointWhoseNodeIsGoneIsNotALostPlacement(t *testing.T) {
	f, trunk := seedTrunk(t, filepath.Join(t.TempDir(), "f"))
	for _, row := range []string{`"a"`, `"b"`} {
		if _, _, err := f.Append(trunk, 0, []byte(row), nil); err != nil {
			t.Fatal(err)
		}
	}
	child, err := f.ForkAt(trunk, 2)
	if err != nil {
		t.Fatal(err)
	}
	k, _ := f.idx.Head(child)
	n, _ := f.idx.Node(k)
	m := n.marker()
	m.from = "gone"
	if err := writeNodeMarker(f.irDir(k), m); err != nil {
		t.Fatal(err)
	}
	if err := f.Refresh(); err != nil {
		t.Fatal(err)
	}
	ti, ok := f.InfoLight(child)
	if !ok || ti.PlacedLost != "" || ti.Placed != "" || ti.Parent != "" {
		t.Fatalf("info = %+v, want a node under nothing with no placement lost", ti)
	}
}

func TestATornMarkerIsNoNodeAtAll(t *testing.T) {
	dir := t.TempDir()
	for _, m := range []nodeMarker{
		{from: "parent", kind: "conversation", trunk: "t1", placed: "elsewhere"},
		{from: "parent", kind: "conversation", trunk: "t1", placedTop: true},
		{from: "parent", kind: "conversation", trunk: "t1"},
		{kind: "loadout"},
	} {
		if err := writeNodeMarker(dir, m); err != nil {
			t.Fatal(err)
		}
		whole, err := os.ReadFile(filepath.Join(dir, nodeMarkerName))
		if err != nil {
			t.Fatal(err)
		}
		if got, ok := parseNodeMarker(whole); !ok || got != m {
			t.Fatalf("a whole marker read back as %+v (ok %v), want %+v", got, ok, m)
		}
		for cut := 0; cut < len(whole); cut++ {
			if got, ok := parseNodeMarker(whole[:cut]); ok {
				t.Fatalf("marker %q cut at %d read as %+v: a torn marker must be no node, never the node without its placement", whole, cut, got)
			}
		}
	}
	if _, ok := parseNodeMarker([]byte("from=a\nkind=conversation\ntrunk=t1\nplaced=late\n")); ok {
		t.Fatal("a marker with a line after trunk= was read: trunk= seals it")
	}
}
