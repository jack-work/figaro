package cli

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/jack-work/figaro/api/livedoc"
	"github.com/jack-work/figaro/sdk/aria"
)

// Two arias sharing a prefix carry the same turn ids over different questions
// above the fork point, so a cache keyed by (turn, node) must clear on retarget.

// switchedPager holds aria A, retargeted at aria B; both have a turn 2 with
// different questions.
func switchedPager(t testing.TB, h int) *transcript {
	t.Helper()
	tr := stickyPager(t, 1, 3, 12, h)
	// The cache is filled by drawing the header, so pin turn 2 in A first.
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
