package angelus

import "testing"

// El Inspector XIV, C4: a minter crossing nodes is qualified with the node it
// came from, so it never equals a principal on the new home. n1: it sheds
// this node's name only when coming home, that is when this node holds a
// departure record for it; a stranger naming this node stays foreign.
func TestAMovedMinterIsQualifiedWithItsOriginNode(t *testing.T) {
	for _, c := range []struct {
		minted    string
		returning bool
		want      string
	}{
		{"sandbox:box0", false, "A/sandbox:box0"},
		{"user:1000", false, "A/user:1000"},
		{"", false, "node:A"},
		{"not a principal", false, "node:A"},
		{"C/user:x", false, "C/user:x"},
		{"B/sandbox:box0", true, "sandbox:box0"},
		{"B/user:1000", false, "B/user:1000"},
		{"user:1000", true, "A/user:1000"},
	} {
		if got := qualifyMinter(c.minted, "A", "B", c.returning); got != c.want {
			t.Errorf("qualifyMinter(%q, returning %v) on B from A = %q, want %q", c.minted, c.returning, got, c.want)
		}
	}
}
