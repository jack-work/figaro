package cli

import "testing"

func TestAnElidedSideOfAProposedMoveIsMarked(t *testing.T) {
	if got := sideOf("", true); got != "…" {
		t.Fatalf("an elided side prints %q", got)
	}
	if got := sideOf("", false); got != "-" {
		t.Fatalf("the top prints %q", got)
	}
	if got := sideOf("abc", false); got != "abc" {
		t.Fatalf("a readable side prints %q", got)
	}
}
