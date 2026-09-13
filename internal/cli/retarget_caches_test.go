package cli

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/jack-work/figaro/api/livedoc"
	"github.com/jack-work/figaro/internal/livelog/aria"
)

// A SWITCH CHANGES WHAT EVERY (turn, node) KEY MEANS. Two arias that share a
// prefix carry the same turn ids over different questions above their fork
// point, so any cache keyed by that coordinate and not cleared on retarget
// paints one conversation's chrome over another's body.
//
// Seen in a pane on b04f2120: hopping parent -> branch -> parent -> cousin
// left the header reading "branch turn 1" while the footer named the cousin
// and the wire had delivered "cousin turn 1".

// switchedPager is a pager holding aria A, retargeted at aria B, where both
// have a turn 2 and they ask different questions in it.
func switchedPager(t testing.TB, h int) *transcript {
	t.Helper()
	tr := stickyPager(t, 1, 3, 12, h)
	// PIN TURN 2 IN ARIA A FIRST. The cache is filled by drawing the header, so
	// a switch tested from a pager that never pinned anything cannot fail.
	head, ok := headEntryOf(tr, 2)
	if !ok {
		t.Fatal("fixture: turn 2 is not held")
	}
	tr.offset = entryRowsStart(head) + len(tr.stickyBlockOf(2).rows) + 2
	tr.buildIndex()
	if got := strings.Join(plain(headRowsOf(tr)), "\n"); !strings.Contains(got, "QUESTION2") {
		t.Fatalf("fixture: turn 2's question is not pinned before the switch:\n%s", got)
	}

	b := aria.NewClient()
	var parts []aria.TurnPart
	for i := range 3 {
		id := uint64(1 + i)
		var ns []livedoc.Node
		for n := range 12 {
			ns = append(ns, livedoc.Node{Type: livedoc.NodeProse, Markdown: fmt.Sprintf("OTHER%d-%d", id, n)})
		}
		parts = append(parts, aria.TurnPart{Turn: aria.Turn{
			ID: id, Inquiry: fmt.Sprintf("COUSIN%d", id), Sealed: true, Nodes: ns,
		}})
	}
	b.Apply(aria.Page{Parts: parts}, aria.Notify)
	tr.retarget(b, "aria5678", newSessionStatus("aria5678", time.Now()), 0)
	tr.follow = false
	tr.buildIndex()
	return tr
}

// TestRetarget_StickyHeaderFollowsTheSubject is the canary for the defect
// above: pin a question in aria A, switch to aria B, and read the header.
func TestRetarget_StickyHeaderFollowsTheSubject(t *testing.T) {
	tr := switchedPager(t, 24)
	head, ok := headEntryOf(tr, 2)
	if !ok {
		t.Fatal("turn 2 has no head entry after the switch")
	}
	tr.offset = entryRowsStart(head) + len(tr.stickyBlockOf(2).rows) + 2
	tr.buildIndex()
	got := strings.Join(plain(headRowsOf(tr)), "\n")
	if strings.Contains(got, "QUESTION") {
		t.Fatalf("the header still names the aria we LEFT:\n%s", got)
	}
	if !strings.Contains(got, "COUSIN2") {
		t.Fatalf("the header does not name the aria we arrived at:\n%s", got)
	}
}
