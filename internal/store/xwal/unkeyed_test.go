package xwal

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func unkeyedCfg() Config {
	return Config{
		Main: "ir",
		Channels: []ChannelSpec{
			{Name: "ir", Kind: ChannelLog},
			{Name: "chalkboard", Kind: ChannelReducible, Reducer: MapReducerName, Unkeyed: true},
		},
		SegmentSize: 4096,
	}
}

func seedUnkeyed(t *testing.T, dir string) (*Trunks, TrunkID) {
	t.Helper()
	f, err := createTrunks(dir, unkeyedCfg())
	if err != nil {
		t.Fatal(err)
	}
	cleanupTrunks(t, f)
	if err := f.CreateStump("s"); err != nil {
		t.Fatal(err)
	}
	id, err := f.SpawnUnderStump("s")
	if err != nil {
		t.Fatal(err)
	}
	return f, id
}

// An unkeyed channel is appended with NO reference to the timeline, and a
// fork still inherits exactly what the board held at the fork point --
// because the main record there recorded where the channel stood.
func TestUnkeyedChannelForksAtTheCursor(t *testing.T) {
	f, trunk := seedUnkeyed(t, filepath.Join(t.TempDir(), "f"))
	var at uint64
	for i := range 4 {
		// Board patch FIRST, with mainLT 0: it does not consult main at all.
		patch, _ := MapSetPatch([]string{fmt.Sprintf("k%d", i)}, fmt.Appendf(nil, `%d`, i))
		if _, err := f.AppendChannel(string(trunk), "chalkboard", 0, patch, nil); err != nil {
			t.Fatal(err)
		}
		_, lt, err := f.Append(trunk, 0, fmt.Appendf(nil, `"turn%d"`, i), nil)
		if err != nil {
			t.Fatal(err)
		}
		if i == 1 {
			at = lt // fork here: the board held k0,k1 and nothing later
		}
	}
	alt, err := f.ForkAt(trunk, at)
	if err != nil {
		t.Fatalf("fork at %d: %v", at, err)
	}
	x, err := f.Head(alt)
	if err != nil {
		t.Fatal(err)
	}
	defer x.Close()
	var last uint64
	for _, c := range x.Channels() {
		if c.Name == "chalkboard" {
			last = c.Last
		}
	}
	st, err := x.StateAt("chalkboard", last)
	if err != nil {
		t.Fatalf("state: %v", err)
	}
	got := string(st)
	if want := `{"k0":0,"k1":1}`; got != want {
		t.Fatalf("forked board = %s, want %s (the board AS OF the fork point)", got, want)
	}
}

// The cursor stamp is what makes that work, and a main record written
// before stamping existed has none. Such a record must still yield a
// boundary, derived from the channel's old main-LT key.
func TestUnkeyedCursorFallsBackForOldRecords(t *testing.T) {
	f, trunk := seedUnkeyed(t, filepath.Join(t.TempDir(), "f"))
	for i := range 3 {
		patch, _ := MapSetPatch([]string{fmt.Sprintf("k%d", i)}, fmt.Appendf(nil, `%d`, i))
		if _, err := f.AppendChannel(string(trunk), "chalkboard", 0, patch, nil); err != nil {
			t.Fatal(err)
		}
		if _, _, err := f.Append(trunk, 0, []byte(`"t"`), nil); err != nil {
			t.Fatal(err)
		}
	}
	x, err := f.Head(trunk)
	if err != nil {
		t.Fatal(err)
	}
	// A pre-stamp record: no cursor at all.
	cur, err := x.CursorAt(mainTail(x), "chalkboard")
	x.Close()
	if err != nil {
		t.Fatalf("cursorAt: %v", err)
	}
	if cur == 0 {
		t.Fatal("cursor is 0; the stamp is not being written")
	}
}

// A reader walking main already holds the cursor stamp, so attributing an
// unkeyed channel's records to turns costs no extra read at all.
func TestMainRecordCarriesTheCursor(t *testing.T) {
	f, trunk := seedUnkeyed(t, filepath.Join(t.TempDir(), "f"))
	for i := range 3 {
		patch, _ := MapSetPatch([]string{fmt.Sprintf("k%d", i)}, fmt.Appendf(nil, `%d`, i))
		if _, err := f.AppendChannel(string(trunk), "chalkboard", 0, patch, nil); err != nil {
			t.Fatal(err)
		}
		if _, _, err := f.Append(trunk, 0, []byte(`"t"`), nil); err != nil {
			t.Fatal(err)
		}
	}
	x, err := f.Head(trunk)
	if err != nil {
		t.Fatal(err)
	}
	defer x.Close()
	tail := mainTail(x)
	seen := 0
	var prev uint64
	for lt := uint64(1); lt <= tail; lt++ {
		rec, err := x.ReadAt("ir", lt)
		if err != nil {
			t.Fatal(err)
		}
		cur, ok := rec.Cursors["chalkboard"]
		if !ok {
			continue // pre-stamp genesis records
		}
		if cur < prev {
			t.Fatalf("cursor went backwards at LT %d: %d after %d", lt, cur, prev)
		}
		prev = cur
		seen++
	}
	if seen == 0 {
		t.Fatal("no main record carried a cursor; the stamp is not reaching Record")
	}
	if prev == 0 {
		t.Fatal("cursor never advanced despite three board patches")
	}
}

// A cursor is DATA ON DISK. Stale, repaired, or written by an older build,
// it can name a position the parent does not have -- and a fork base above
// the parent's own tail leaves the child numbering over a hole in its own
// prefix, which kills the first read through the gap.
//
// Forged the way reality forges it: main stamps a board of three patches,
// then the board's records are lost (a repair, a truncated tail) while the
// stamp survives. The fork must not believe the stamp over the data.
func TestUnkeyedForkCeilingsAtTheParentTail(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "f")
	f, trunk := seedUnkeyed(t, dir)
	var at uint64
	for i := range 3 {
		patch, _ := MapSetPatch([]string{fmt.Sprintf("k%d", i)}, fmt.Appendf(nil, `%d`, i))
		if _, err := f.AppendChannel(string(trunk), "chalkboard", 0, patch, nil); err != nil {
			t.Fatal(err)
		}
		_, lt, err := f.Append(trunk, 0, []byte(`"t"`), nil)
		if err != nil {
			t.Fatal(err)
		}
		if i == 1 {
			at = lt // INTERIOR: forking at the tail takes a different branch
		}
	}
	node := f.head(string(trunk))
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	// Lose the board's records; keep main and its stamps.
	boardDir := filepath.Join(dir, "chalkboard", node)
	segs, _ := filepath.Glob(filepath.Join(boardDir, "*.jsonl"))
	if len(segs) == 0 {
		t.Fatal("no board segments to lose; the test proves nothing")
	}
	for _, p := range segs {
		if err := os.Remove(p); err != nil {
			t.Fatal(err)
		}
	}

	g, err := openTrunks(dir, unkeyedCfg())
	if err != nil {
		t.Fatalf("reopen after losing the board: %v", err)
	}
	cleanupTrunks(t, g)
	// What the parent can actually serve.
	px, err := g.Head(trunk)
	if err != nil {
		t.Fatal(err)
	}
	var parentTail uint64
	for _, c := range px.Channels() {
		if c.Name == "chalkboard" {
			parentTail = c.Last
		}
	}
	px.Close()

	alt, err := g.ForkAt(trunk, at)
	if err != nil {
		t.Fatalf("fork over a stale cursor: %v", err)
	}
	base := g.readChannelBase(t, g.head(string(alt)), "chalkboard")
	if base > parentTail+1 {
		t.Fatalf("board base %d, parent serves only through %d: the child numbers over a hole",
			base, parentTail)
	}
}

func (t *Trunks) readChannelBase(tb testing.TB, node, channel string) uint64 {
	tb.Helper()
	b, err := readForkBaseFile(filepath.Join(t.root, channel, node, ".fork"))
	if err != nil {
		tb.Fatalf("read %s base for %s: %v", channel, node, err)
	}
	return b
}
