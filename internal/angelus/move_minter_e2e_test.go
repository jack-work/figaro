package angelus_test

import "testing"

// C4 across two nodes: on the new home the moved aria's minter names the
// node it came from, so no principal there owns it by alias.
func TestAMovedAriasMinterNamesItsOriginNode(t *testing.T) {
	a, b := newFedPair(t)
	parent := a.mint()
	before, _ := a.a.Backend.Meta(parent)
	_, err := a.as("probe").Move(callCtx(t), parent, b.id)
	if err != nil {
		t.Fatal(err)
	}
	m, err := b.a.Backend.Meta(parent)
	if err != nil || m == nil {
		t.Fatalf("no meta for %s on the new home: %v", parent, err)
	}
	was, want := "", "node:"+a.id
	if before != nil && before.MintedBy != "" {
		was, want = before.MintedBy, a.id+"/"+before.MintedBy
	}
	if m.MintedBy != want {
		t.Fatalf("minted_by on the new home = %q (was %q on the old), want %q", m.MintedBy, was, want)
	}
}
