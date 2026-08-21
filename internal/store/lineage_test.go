package store

import (
	"testing"

	"github.com/jack-work/figaro/api/message"
)

// Lineage is the plumbing prefix sharing needs: figaro's own topology
// projected into forest refs, so no figwal export and no adapter arithmetic.
func TestLineageWalksToTheRootWithOwnedBases(t *testing.T) {
	b, err := NewXwalBackend(t.TempDir(), 0)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()

	l, _ := b.CreateOutfit("d", patchSet(map[string]string{"system.model": "m"}))
	conv, _ := b.CreateConversation(l)

	ir, _ := b.OpenFigIR(conv)
	for i := 0; i < 6; i++ {
		if _, err := ir.Append(Entry[message.Message]{Payload: message.Message{Role: message.RoleInput}}); err != nil {
			t.Fatal(err)
		}
	}
	_, alt, err := b.ForkAt(conv, 4)
	if err != nil {
		t.Fatal(err)
	}

	s := b.Store()

	root := s.Lineage(conv)
	if len(root) == 0 {
		t.Fatal("no lineage for the trunk")
	}
	if last := root[len(root)-1]; last.Node != conv {
		t.Errorf("lineage must END at the node asked for: got %q, want %q", last.Node, conv)
	}
	if root[0].Base != 0 {
		t.Errorf("the root owns from the beginning, got base %d", root[0].Base)
	}

	branch := s.Lineage(alt)
	if len(branch) < 2 {
		t.Fatalf("a fork's lineage must include its ancestor, got %d refs", len(branch))
	}
	if last := branch[len(branch)-1]; last.Node != alt {
		t.Errorf("branch lineage ends at %q, want %q", last.Node, alt)
	}
	if last := branch[len(branch)-1]; last.Base == 0 {
		t.Error("a fork's own base must be its divergence point, not 0")
	}

	// Root first: an ancestor never follows its descendant, or split() cuts
	// the wrong span.
	for i := 1; i < len(branch); i++ {
		if branch[i].Base != 0 && branch[i].Base < branch[i-1].Base {
			t.Errorf("lineage not ordered root-first at %d: base %d after %d",
				i, branch[i].Base, branch[i-1].Base)
		}
	}

	// The shared prefix is the SAME node in both lineages, which is what lets
	// two branches hit one residency.
	if branch[0].Node != root[0].Node {
		t.Errorf("branch and trunk disagree on the root node: %q vs %q",
			branch[0].Node, root[0].Node)
	}
}

// The base figaro reports must equal the base the cache will split on. This is
// the live-store agreement (marker/first-record/branched_lt) asserted on a
// fixture, so it fails here rather than as a sibling's record in a read.
func TestLineageBaseMatchesTheTrunkBranchPoint(t *testing.T) {
	b, err := NewXwalBackend(t.TempDir(), 0)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()

	l, _ := b.CreateOutfit("d", patchSet(map[string]string{"system.model": "m"}))
	conv, _ := b.CreateConversation(l)
	ir, _ := b.OpenFigIR(conv)
	for i := 0; i < 6; i++ {
		ir.Append(Entry[message.Message]{Payload: message.Message{Role: message.RoleInput}})
	}
	_, alt, err := b.ForkAt(conv, 4)
	if err != nil {
		t.Fatal(err)
	}

	refs := b.Store().Lineage(alt)
	own := refs[len(refs)-1]

	var want uint64
	for _, n := range b.Store().Nodes() {
		if n.ID == alt {
			want = n.BranchedLT
		}
	}
	if want == 0 {
		t.Skip("topology did not report a branch point for the fork")
	}
	if own.Base != want {
		t.Errorf("lineage base %d != topology BranchedLT %d; the cache would split at the wrong record",
			own.Base, want)
	}
}

func TestLineageOfAnUnknownIDIsEmpty(t *testing.T) {
	b, err := NewXwalBackend(t.TempDir(), 0)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	if got := b.Store().Lineage("nosuchtrunk"); got != nil {
		t.Errorf("unknown id returned %v; a miss must not invent a lineage", got)
	}
}
