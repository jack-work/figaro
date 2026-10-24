package cli

import (
	"strings"
	"testing"
)

// Gluck: the double bar separates the header and nothing else; a message in
// the body is bordered by single rules.
func TestRule_OnlyTheHeadersEdgeIsDouble(t *testing.T) {
	tr := stickyPager(t, 1, 4, 6, 24)
	_, maxOff := tr.layoutNow()
	pinned := 0
	for off := 0; off <= maxOff; off++ {
		tr.offset = off
		tr.buildIndex()
		head := plain(headRowsOf(tr))
		body := plain(bodyRows(tr))
		for i, row := range body[min(len(head), len(body)):] {
			if strings.Contains(row, ruleDouble) {
				t.Fatalf("offset %d: body row %d is a double rule: %q", off, len(head)+i, row)
			}
		}
		if n := len(head); n > stickyText {
			pinned++
			if edge := head[n-1]; strings.Trim(edge, ruleDouble+"╧╤╪") != "" {
				t.Fatalf("offset %d: the header's edge is not double: %q", off, edge)
			}
		}
	}
	if pinned == 0 {
		t.Fatal("fixture: no offset pinned a header with its rule")
	}
}

func TestRule_JointTakesTheWeightOfTheCellItCuts(t *testing.T) {
	gutter := gutterCut{row: "  │ x", hidden: "  │ y"}
	mixed := dimRuleOf(8, func(i int) bool { return i < 4 })
	if got := stripANSI(joinRule(mixed, gutter, gutterCut{})); got != "══╧═────" {
		t.Errorf("a joint in the double half: %q", got)
	}
	cut := gutterCut{row: "      │ x", hidden: "      │ y"}
	if got := stripANSI(joinRule(mixed, cut, gutterCut{})); got != "════──┴─" {
		t.Errorf("a joint in the single half: %q", got)
	}
}
