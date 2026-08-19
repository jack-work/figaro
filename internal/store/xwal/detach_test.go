package xwal

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jack-work/figaro/internal/store/segment"
)

// mainLTs reads every main index visible from a trunk's head.
func mainLTs(t *testing.T, f *Trunks, trunk TrunkID) []uint64 {
	t.Helper()
	x, err := f.Head(trunk)
	if err != nil {
		t.Fatalf("head %s: %v", trunk, err)
	}
	defer x.Close()
	var out []uint64
	ch := x.chans[f.main]
	if err := ch.log.Range(1, func(idx uint64, _ []byte) error {
		out = append(out, idx)
		return nil
	}); err != nil {
		t.Fatalf("range %s: %v", trunk, err)
	}
	return out
}

// A detached node reads its whole history with its ancestor DELETED. This
// is the property a delete depends on: absorb the prefix, then unlink.
func TestDetachSurvivesAncestorRemoval(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "f")
	f, parent := seedMapTrunk(t, dir)
	for i := range 5 {
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
	before := mainLTs(t, f, child)
	if len(before) < 6 {
		t.Fatalf("child sees %d records before detach, want the inherited prefix", len(before))
	}

	if err := f.Detach(f.head(string(child))); err != nil {
		t.Fatalf("detach: %v", err)
	}
	if got := mainLTs(t, f, child); len(got) != len(before) {
		t.Fatalf("after detach child sees %v, want %v", got, before)
	}
	// The ancestor is now dispensable. Remove its directory outright, the
	// way a delete would, and the child must be unharmed.
	if err := os.RemoveAll(f.irDir(f.head(string(parent)))); err != nil {
		t.Fatal(err)
	}
	g, err := openTrunks(dir, mapTrunksCfg())
	if err != nil {
		t.Fatalf("reopen after removing the ancestor: %v", err)
	}
	cleanupTrunks(t, g)
	if got := mainLTs(t, g, child); len(got) != len(before) {
		t.Fatalf("with the ancestor gone the child sees %v, want %v", got, before)
	}
}

// Detaching must not change what the node reads, including reducible state
// folded from a watermark it used to inherit.
func TestDetachPreservesReducibleState(t *testing.T) {
	f, parent := seedMapTrunk(t, filepath.Join(t.TempDir(), "f"))
	_, lt, err := f.Append(parent, 0, []byte(`"p"`), nil)
	if err != nil {
		t.Fatal(err)
	}
	patch, _ := MapSetPatch([]string{"model"}, []byte(`"opus"`))
	if _, err := f.AppendChannel(string(parent), "chalkboard", lt, patch, nil); err != nil {
		t.Fatal(err)
	}
	child, err := f.ForkTail(parent)
	if err != nil {
		t.Fatal(err)
	}
	state := func(label string) string {
		x, err := f.Head(child)
		if err != nil {
			t.Fatalf("%s head: %v", label, err)
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
			t.Fatalf("%s state: %v", label, err)
		}
		return string(st)
	}
	before := state("before")
	if !strings.Contains(before, `"opus"`) {
		t.Fatalf("child state before detach = %s, want the inherited model", before)
	}
	if err := f.Detach(f.head(string(child))); err != nil {
		t.Fatalf("detach: %v", err)
	}
	if after := state("after"); after != before {
		t.Fatalf("detach changed state: %s -> %s", before, after)
	}
}

// Detach is idempotent: running it twice is not an error and changes
// nothing, which is what makes crash recovery a re-run.
func TestDetachIsIdempotent(t *testing.T) {
	f, parent := seedMapTrunk(t, filepath.Join(t.TempDir(), "f"))
	for range 3 {
		if _, _, err := f.Append(parent, 0, []byte(`"p"`), nil); err != nil {
			t.Fatal(err)
		}
	}
	child, err := f.ForkTail(parent)
	if err != nil {
		t.Fatal(err)
	}
	key := f.head(string(child))
	if err := f.Detach(key); err != nil {
		t.Fatal(err)
	}
	first := mainLTs(t, f, child)
	if err := f.Detach(key); err != nil {
		t.Fatalf("second detach: %v", err)
	}
	if got := mainLTs(t, f, child); len(got) != len(first) {
		t.Fatalf("second detach changed the log: %v -> %v", first, got)
	}
}

// The crash-safety argument, exercised rather than asserted: a Detach that
// dies between two channels' marker flips must leave EVERY channel
// readable. .from is written last precisely so this holds -- an unflipped
// channel still delegates to the parent, a flipped one reads its own
// absorbed copy, and the two are byte-identical.
func TestDetachHalfFlipIsReadable(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "f")
	f, parent := seedMapTrunk(t, dir)
	for i := range 4 {
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
	key := f.head(string(child))
	want := mainLTs(t, f, child)
	if err := f.Detach(key); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}

	// Rewind to a half-flip: main published, chalkboard not, .from still
	// naming the parent. This is exactly the state a kill between the two
	// .fork writes leaves behind.
	cbFork := filepath.Join(dir, "chalkboard", key, ".fork")
	if err := os.WriteFile(cbFork, []byte("base=5\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "ir", key, ".from"), []byte("n0\nconversation\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	g, err := openTrunks(dir, mapTrunksCfg())
	if err != nil {
		t.Fatalf("reopen half-flipped: %v", err)
	}
	cleanupTrunks(t, g)
	if got := mainLTs(t, g, child); len(got) != len(want) {
		t.Fatalf("half-flipped child reads %v, want %v", got, want)
	}
	x, err := g.Head(child)
	if err != nil {
		t.Fatalf("half-flipped head: %v", err)
	}
	defer x.Close()
	for _, c := range x.Channels() {
		if c.Name != "chalkboard" || c.Last == 0 {
			continue
		}
		if _, err := x.StateAt("chalkboard", c.Last); err != nil {
			t.Fatalf("half-flipped chalkboard unreadable: %v", err)
		}
	}
}

// A prefix longer than one segment's worth. The absorbed prefix must fold
// to the same reducible state regardless of size: chunking it and giving
// each chunk the INITIAL watermark would reproduce the wrong state from the
// second chunk on, and only a prefix this long can show it.
func TestDetachAbsorbsAPrefixLargerThanASegment(t *testing.T) {
	cfg := mapTrunksCfg()
	cfg.SegmentSize = 4096 // several rows per segment, so the prefix spans many
	dir := filepath.Join(t.TempDir(), "f")
	f, err := createTrunks(dir, cfg)
	if err != nil {
		t.Fatal(err)
	}
	cleanupTrunks(t, f)
	if err := f.CreateStump("s"); err != nil {
		t.Fatal(err)
	}
	parent, err := f.SpawnUnderStump("s")
	if err != nil {
		t.Fatal(err)
	}
	const turns = 120
	for i := range turns {
		_, lt, err := f.Append(parent, 0, fmt.Appendf(nil, `"turn-%03d-padding-padding-padding"`, i), nil)
		if err != nil {
			t.Fatal(err)
		}
		// A DISTINCT key per turn, so state ACCUMULATES. Overwriting one
		// key would make a lost watermark invisible: last-write-wins
		// reproduces the right answer from the wrong history.
		patch, _ := MapSetPatch([]string{fmt.Sprintf("k%d", i)}, fmt.Appendf(nil, `%d`, i))
		if _, err := f.AppendChannel(string(parent), "chalkboard", lt, patch, nil); err != nil {
			t.Fatal(err)
		}
	}
	child, err := f.ForkTail(parent)
	if err != nil {
		t.Fatal(err)
	}
	// EVERY index, not just the tail: a chunked absorb gives each chunk
	// after the first a watermark claiming the INITIAL state, and only a
	// read landing INSIDE such a chunk can see it. Reading the tail alone
	// is served by the node's own segment and proves nothing.
	states := func(g *Trunks) []string {
		x, err := g.Head(child)
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
		out := make([]string, 0, last)
		for i := uint64(1); i <= last; i++ {
			st, err := x.StateAt("chalkboard", i)
			if err != nil {
				t.Fatalf("state at %d: %v", i, err)
			}
			out = append(out, string(st))
		}
		return out
	}
	before := states(f)
	if len(before) < turns {
		t.Fatalf("only %d chalkboard indices, want >= %d", len(before), turns)
	}
	if tail := before[len(before)-1]; !strings.Contains(tail, `"k0":0`) ||
		!strings.Contains(tail, fmt.Sprintf(`"k%d":%d`, turns-1, turns-1)) {
		t.Fatalf("pre-detach tail state does not accumulate: %s", tail)
	}
	lts := mainLTs(t, f, child)

	if err := f.Detach(f.head(string(child))); err != nil {
		t.Fatalf("detach: %v", err)
	}
	// Compare against DISK, not the live handle: a cached head still holds
	// the pre-detach fork base and would keep delegating to the parent,
	// hiding whatever the absorb actually wrote. Remove the ancestor too,
	// so nothing can be served from it by accident.
	if err := os.RemoveAll(f.irDir(f.head(string(parent)))); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	g, err := openTrunks(dir, cfg)
	if err != nil {
		t.Fatalf("reopen without the ancestor: %v", err)
	}
	cleanupTrunks(t, g)
	after := states(g)
	for i := range before {
		if i >= len(after) {
			t.Fatalf("after detach only %d indices survive, want %d", len(after), len(before))
		}
		if after[i] != before[i] {
			t.Fatalf("detach changed folded state at index %d: %s -> %s", i+1, before[i], after[i])
		}
	}
	if got := mainLTs(t, g, child); len(got) != len(lts) {
		t.Fatalf("with the ancestor gone: %d records, want %d", len(got), len(lts))
	}
}

// A crash mid-absorb leaves a partial prefix segment at its final name.
// Detach must still complete on the retry: crash recovery here IS re-running
// it, so anything that makes a second run fail makes the store unrecoverable.
func TestDetachRecoversFromAPartialAbsorb(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "f")
	f, parent := seedMapTrunk(t, dir)
	for range 4 {
		if _, _, err := f.Append(parent, 0, []byte(`"p"`), nil); err != nil {
			t.Fatal(err)
		}
	}
	child, err := f.ForkTail(parent)
	if err != nil {
		t.Fatal(err)
	}
	key := f.head(string(child))
	want := mainLTs(t, f, child)

	// Simulate the crash twice over: a half-written segment at the FINAL
	// name, and our own scratch file left behind. segment.Create is O_EXCL,
	// so either one alone would fail every retry.
	chDir := filepath.Join(dir, "ir", key)
	partial := filepath.Join(chDir, segFileName(1, segment.JSONLCodec{}))
	if err := os.WriteFile(partial, []byte("{\"truncated\":\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	scratch := filepath.Join(chDir, fmt.Sprintf(".absorb-%d", os.Getpid()))
	if err := os.WriteFile(scratch, []byte("stale\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := f.Detach(key); err != nil {
		t.Fatalf("detach after a partial absorb: %v", err)
	}
	if got := mainLTs(t, f, child); len(got) != len(want) {
		t.Fatalf("after recovery child reads %v, want %v", got, want)
	}
}
