package xwal

import (
	"fmt"
	"math/rand"
	"path/filepath"
	"strings"
	"testing"
)

// TestForest_FuzzSequential drives a long random sequence of trunk ops
// (send/set/interior-fork/tail-fork) and after every op asserts global
// invariants: every trunk's head opens, its IR reads end-to-end, and its
// reducible map folds — no errors, no panics. Finally it reopens the
// forest from disk and asserts every trunk's IR tail + folded state match.
// Deterministic seed -> reproducible.
func TestForest_FuzzSequential(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "f")
	f, root := seedMapTrunk(t, dir)
	rng := rand.New(rand.NewSource(1))
	trunks := []string{root}

	tailOf := func(tr string) uint64 {
		x, err := f.Head(tr)
		if err != nil {
			t.Fatalf("head %s: %v", tr, err)
		}
		defer x.Close()
		return mainTail(x)
	}

	for i := 0; i < 500; i++ {
		tr := trunks[rng.Intn(len(trunks))]
		switch rng.Intn(5) {
		case 0, 1: // send at tail
			if _, _, err := f.Append(tr, 0, []byte(fmt.Sprintf("%q", fmt.Sprintf("m%d", i))), nil); err != nil {
				t.Fatalf("send %s: %v", tr, err)
			}
		case 2: // set a (possibly nested) key
			path := []string{fmt.Sprintf("k%d", rng.Intn(4))}
			if rng.Intn(2) == 0 {
				path = append(path, fmt.Sprintf("s%d", rng.Intn(3)))
			}
			p, _ := MapSetPatch(path, []byte(fmt.Sprintf("%d", i)))
			if _, err := f.AppendChannel(tr, "chalkboard", 0, p, nil); err != nil {
				t.Fatalf("set %s: %v", tr, err)
			}
		case 3: // interior fork (if there's interior room)
			tail := tailOf(tr)
			if tail <= 2 {
				continue
			}
			n := uint64(1 + rng.Intn(int(tail-1))) // 1..tail-1
			alt, err := f.ForkAt(tr, n)
			if err != nil {
				if strings.Contains(err.Error(), "frozen history") {
					continue // deferred re-split-below; expected, not a fault
				}
				t.Fatalf("interior fork %s:%d: %v", tr, n, err)
			}
			if _, _, err := f.Append(alt, 0, []byte(`"forked"`), nil); err != nil {
				t.Fatalf("append after interior fork %s:%d: %v", tr, n, err)
			}
			if alt != tr {
				trunks = append(trunks, alt)
			}
		case 4: // tail fork (bisect present)
			alt, err := f.ForkTail(tr)
			if err != nil {
				t.Fatalf("fork-tail %s: %v", tr, err)
			}
			trunks = append(trunks, alt)
		}
		// Global invariant after every op.
		for _, tk := range trunks {
			assertTrunkReadable(t, f, tk)
			// Single-leaf invariant: exactly one live (unfrozen) leaf carries
			// each trunk id, and it is the head. (Guards against the multi-head
			// regression where a frozen branch point was re-forked into same-id
			// sibling continuations.)
			n := 0
			for k, nd := range f.idx.All() {
				if nd.Trunk == tk {
					n++
					if f.head(tk) != k {
						t.Fatalf("trunk %s: live leaf %s is not the head (%s)", tk, k, f.head(tk))
					}
				}
			}
			if n != 1 {
				t.Fatalf("trunk %s has %d live leaves (want 1)", tk, n)
			}
		}
	}

	// Reopen-from-disk consistency.
	before := snapshotTrunks(t, f, trunks)
	f2, err := openTrunks(dir, mapTrunksCfg())
	if err != nil {
		t.Fatal(err)
	}
	cleanupTrunks(t, f2)
	after := snapshotTrunks(t, f2, trunks)
	for tr, b := range before {
		if after[tr] != b {
			t.Fatalf("reopen mismatch for %s:\n  before %q\n  after  %q", tr, b, after[tr])
		}
	}
	t.Logf("fuzz ok: %d trunks after 500 ops", len(trunks))
}

func assertTrunkReadable(t *testing.T, f *Trunks, tr string) {
	t.Helper()
	x, err := f.Head(tr)
	if err != nil {
		t.Fatalf("head %s: %v", tr, err)
	}
	defer x.Close()
	last := mainTail(x)
	for lt := uint64(1); lt <= last; lt++ {
		if _, _, err := x.Read("ir", lt); err != nil {
			t.Fatalf("read ir %s@%d: %v", tr, lt, err)
		}
	}
	for _, c := range x.Channels() {
		if c.Kind == ChannelReducible && c.Last >= c.First && c.First > 0 {
			if _, err := x.StateAt(c.Name, c.Last); err != nil {
				t.Fatalf("stateAt %s.%s@%d: %v", tr, c.Name, c.Last, err)
			}
		}
	}
}

// snapshotTrunks captures each trunk's IR tail + folded chalkboard state.
func snapshotTrunks(t *testing.T, f *Trunks, trunks []string) map[string]string {
	t.Helper()
	out := map[string]string{}
	for _, tr := range trunks {
		x, err := f.Head(tr)
		if err != nil {
			t.Fatalf("head %s: %v", tr, err)
		}
		last := mainTail(x)
		state := "{}"
		for _, c := range x.Channels() {
			if c.Kind == ChannelReducible && c.Last >= c.First && c.First > 0 {
				if st, err := x.StateAt(c.Name, c.Last); err == nil {
					state = string(st)
				}
			}
		}
		out[tr] = fmt.Sprintf("tail=%d state=%s", last, state)
		x.Close()
	}
	return out
}
