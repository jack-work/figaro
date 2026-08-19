package xwal

import (
	"path/filepath"
	"testing"
)

// liveLeaves returns the node keys carrying a trunk id. A flat fork never
// freezes a parent, so every such node is live.
func liveLeaves(t *Trunks, trunk string) []string {
	var out []string
	for k, n := range t.idx.All() {
		if n.Trunk == trunk {
			out = append(out, k)
		}
	}
	return out
}

// TestForest_RepeatedTailForkOneHead drives the user's flow — send a turn,
// tail-fork, send, tail-fork … — and asserts the forked trunk keeps EXACTLY
// ONE live head/leaf throughout. The single-leaf invariant must hold for every
// trunk after every op.
func TestForest_RepeatedTailForkOneHead(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "f")
	f, conv := seedTrunkBirth(t, dir, "L@h")
	if _, _, err := f.Append(conv, 0, []byte(`"u1"`), nil); err != nil {
		t.Fatal(err)
	}
	if _, _, err := f.Append(conv, 0, []byte(`"a1"`), nil); err != nil {
		t.Fatal(err)
	}
	trunks := []string{conv}
	for i := 0; i < 6; i++ {
		alt, err := f.ForkTail(conv)
		if err != nil {
			t.Fatalf("tail-fork %d: %v", i, err)
		}
		trunks = append(trunks, alt)
		if _, _, err := f.Append(conv, 0, []byte(`"turn"`), nil); err != nil {
			t.Fatalf("send %d: %v", i, err)
		}
		// Invariant: every trunk has exactly one live leaf carrying its id.
		for _, tr := range trunks {
			if ll := liveLeaves(f, tr); len(ll) != 1 {
				t.Fatalf("after op %d trunk %s has %d live leaves (want 1): %v", i, tr, len(ll), ll)
			}
		}

	}
	// And the head is the single leaf.
	if got := liveLeaves(f, conv); len(got) != 1 || f.head(conv) != got[0] {
		t.Fatalf("conv head=%s leaves=%v", f.head(conv), got)
	}
}

func TestOpenRejectsMultipleHeads(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "f")
	f, first := seedTrunk(t, dir)
	second, err := f.SpawnUnderStump("s")
	if err != nil {
		t.Fatal(err)
	}
	// Forge a second node claiming the first node's trunk id. This is the
	// shape a lost mint counter produced (bumpSeqs recovering only one of
	// the two), and open must refuse it rather than serve two live heads.
	secondBranch := f.head(second)
	n, ok := readNodeMarker(f.irDir(secondBranch))
	if !ok {
		t.Fatalf("no node marker for %s", secondBranch)
	}
	n.trunk = string(first)
	if err := writeNodeMarker(f.irDir(secondBranch), n); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	if reopened, err := openTrunks(dir, trunksCfg()); err == nil {
		reopened.Close()
		t.Fatal("OpenTrunks accepted multiple live heads for one trunk")
	}
}
