package angelus

import "testing"

// El Inspector XIV, C4: a minter crossing nodes is qualified with the node it
// came from, so it never equals a principal on the new home; one coming home
// sheds its own node.
func TestAMovedMinterIsQualifiedWithItsOriginNode(t *testing.T) {
	for _, c := range []struct{ minted, want string }{
		{"sandbox:box0", "A/sandbox:box0"},
		{"user:1000", "A/user:1000"},
		{"", "node:A"},
		{"not a principal", "node:A"},
		{"C/user:x", "C/user:x"},
		{"B/sandbox:box0", "sandbox:box0"},
	} {
		if got := qualifyMinter(c.minted, "A", "B"); got != c.want {
			t.Errorf("qualifyMinter(%q) on B from A = %q, want %q", c.minted, got, c.want)
		}
	}
}
