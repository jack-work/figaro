package aria

import "testing"

// An aria with no messages and a pager that has not read yet look the same
// in Count; Heard is what tells them apart.
func TestHeardIsSetByTheFirstPageEvenAnEmptyOne(t *testing.T) {
	c := NewClient()
	if c.Heard() {
		t.Fatal("a fresh client has heard nothing")
	}
	c.Apply(Page{}, Quiet)
	if !c.Heard() {
		t.Fatal("an empty page was applied; the client has heard the aria is empty")
	}
	if c.CloneBelow(0).Heard() {
		t.Fatal("a clone points at another aria and has heard nothing of it")
	}
}
