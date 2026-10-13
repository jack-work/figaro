package cli

import (
	"fmt"
	"testing"
	"time"

	"github.com/jack-work/figaro/api/livedoc"
	ldrender "github.com/jack-work/figaro/internal/livelog/render"
	"github.com/jack-work/figaro/sdk/aria"
)

// A fork point is sealed, so the turns below it are the same turns in both
// arias: same ids, same bytes.

// forkPager is about to be pointed at a relative sharing everything below base.
func forkPager(t testing.TB, turns, base, h int) *transcript {
	t.Helper()
	client := aria.NewClient()
	client.Apply(aria.Page{Parts: partsFor(1, turns, "A")}, aria.Notify)
	view := &ariaView{settings: &renderSettings{}}
	tr := newTranscript(ldrender.NewFakeTerminal(60, h), 60, h, view, client, "aria1111", time.Time{})
	tr.enter()
	tr.buildIndex()
	return tr
}

func partsFor(first, n int, tag string) []aria.TurnPart {
	var parts []aria.TurnPart
	for i := range n {
		id := uint64(first + i)
		parts = append(parts, aria.TurnPart{Turn: aria.Turn{
			ID: id, Inquiry: fmt.Sprintf("%s-Q%d", tag, id), Sealed: true,
			Nodes: []livedoc.Node{{Type: livedoc.NodeProse, Markdown: fmt.Sprintf("%s-BODY%d", tag, id)}},
		}})
	}
	return parts
}

// Keep turn < base, drop at or above, into a client of its own.
func TestRetain_KeepsThePrefixAndDropsTheRest(t *testing.T) {
	c := aria.NewClient()
	c.Apply(aria.Page{Parts: partsFor(1, 40, "A")}, aria.Notify)
	if before := c.Count(); before != 40 {
		t.Fatalf("fixture holds %d messages, want 40", before)
	}

	next := c.CloneBelow(21)
	if kept := next.Count(); kept != 20 {
		t.Fatalf("kept %d messages, want the 20 below turn 21", kept)
	}
	if c.Count() != 40 {
		t.Fatalf("the client it was cloned from lost %d messages", 40-c.Count())
	}
	next.ForEachIn(aria.Anchor{}, aria.Anchor{Turn: ^uint64(0)}, func(m aria.Message) bool {
		if m.Turn >= 21 {
			t.Fatalf("turn %d came through the clone", m.Turn)
		}
		return true
	})
	if _, ok := next.InquiryOf(21); ok {
		t.Fatal("turn 21's question survived; it belongs to the aria we left")
	}
	if _, ok := next.InquiryOf(20); !ok {
		t.Fatal("turn 20's question was dropped; it is shared and must be kept")
	}
}

func TestRetain_ZeroKeepsNothing(t *testing.T) {
	c := aria.NewClient()
	c.Apply(aria.Page{Parts: partsFor(1, 10, "A")}, aria.Notify)
	if kept := c.CloneBelow(0).Count(); kept != 0 {
		t.Fatalf("kept %d messages of an unrelated aria, want none", kept)
	}
}

// No turn of the aria we left may remain under the new subject's suffix.
func TestRetain_TheNewSubjectsTurnsLandOverTheOldOnes(t *testing.T) {
	old := aria.NewClient()
	old.Apply(aria.Page{Parts: partsFor(1, 40, "A")}, aria.Notify)
	c := old.CloneBelow(21)
	c.Apply(aria.Page{Parts: partsFor(21, 3, "B")}, aria.Notify)

	got := map[int]string{}
	c.ForEachIn(aria.Anchor{}, aria.Anchor{Turn: ^uint64(0)}, func(m aria.Message) bool {
		got[m.Turn] = m.Nodes[0].Markdown
		return true
	})
	if len(got) != 23 {
		t.Fatalf("the store holds %d turns, want 20 shared plus 3 new", len(got))
	}
	for turn, body := range got {
		want := "A-BODY"
		if turn >= 21 {
			want = "B-BODY"
		}
		if body != fmt.Sprintf("%s%d", want, turn) {
			t.Fatalf("turn %d reads %q, want %s%d", turn, body, want, turn)
		}
	}
}

func TestRetarget_ParkedOnThePrefixKeepsTheScroll(t *testing.T) {
	tr := forkPager(t, 40, 21, 24)
	tr.follow = false
	tr.from = aria.Anchor{Turn: 5}
	tr.offset = 12
	before := tr.offset

	b := aria.NewClient()
	b.Apply(aria.Page{Parts: partsFor(1, 20, "A")}, aria.Notify)
	tr.retarget(b, "aria2222", newSessionStatus("aria2222", time.Now()), 21)

	if !tr.kept {
		t.Fatal("the switch did not keep the scroll: the window was inside the shared prefix")
	}
	if tr.follow {
		t.Fatal("the switch turned following back on under a reader who had scrolled")
	}
	if tr.from.Turn != 5 || tr.offset != before {
		t.Fatalf("the window moved to (turn %d, offset %d), want (5, %d)", tr.from.Turn, tr.offset, before)
	}
}

func TestRetarget_FollowingTheTailJumpsToTheNewTail(t *testing.T) {
	tr := forkPager(t, 40, 21, 24)
	tr.follow = true
	tr.from = aria.Anchor{Turn: 38}

	b := aria.NewClient()
	b.Apply(aria.Page{Parts: partsFor(1, 20, "A")}, aria.Notify)
	tr.retarget(b, "aria2222", newSessionStatus("aria2222", time.Now()), 21)

	if tr.kept {
		t.Fatal("a reader at the live edge was left parked in history")
	}
	if !tr.follow || tr.from != (aria.Anchor{}) {
		t.Fatalf("the window did not reset to the tail: follow=%v from=%v", tr.follow, tr.from)
	}
}

func TestRetarget_ParkedAboveTheDivergenceJumpsToTheTail(t *testing.T) {
	tr := forkPager(t, 40, 21, 24)
	tr.follow = false
	tr.from = aria.Anchor{Turn: 30}

	b := aria.NewClient()
	b.Apply(aria.Page{Parts: partsFor(1, 20, "A")}, aria.Notify)
	tr.retarget(b, "aria2222", newSessionStatus("aria2222", time.Now()), 21)

	if tr.kept {
		t.Fatal("the window showed turns the new aria does not have and was kept anyway")
	}
}

func TestRetarget_RowCacheKeepsThePrefixAndDropsTheRest(t *testing.T) {
	tr := forkPager(t, 40, 21, 24)
	tr.follow = false
	tr.from = aria.Anchor{Turn: 1}
	tr.buildIndex()
	tr.lines()
	if len(tr.rowCache) == 0 {
		t.Fatal("fixture: nothing was cached to keep")
	}
	kept, dropped := 0, 0
	for k := range tr.rowCache {
		if k.turn() < 21 {
			kept++
		} else {
			dropped++
		}
	}
	if kept == 0 || dropped == 0 {
		t.Fatalf("fixture: %d rows below the divergence, %d at or above it", kept, dropped)
	}

	b := aria.NewClient()
	b.Apply(aria.Page{Parts: partsFor(1, 20, "A")}, aria.Notify)
	tr.retarget(b, "aria2222", newSessionStatus("aria2222", time.Now()), 21)

	for k := range tr.rowCache {
		if k.turn() >= 21 {
			t.Fatalf("a row of turn %d survived the switch", k.turn())
		}
	}
	if len(tr.rowCache) != kept {
		t.Fatalf("the switch kept %d cached rows, want the %d below the divergence", len(tr.rowCache), kept)
	}
}

// Every fold state obeys the retention rule: open only where the two arias share
// the block.
func TestRetarget_FoldStatesFollowTheDivergence(t *testing.T) {
	tr := forkPager(t, 40, 21, 24)
	below := nodeRef{turn: 5, index: 0}
	above := nodeRef{turn: 30, index: 0}
	for _, fold := range []map[nodeRef]bool{tr.expanded, tr.adorned} {
		fold[below] = true
		fold[above] = true
	}

	b := aria.NewClient()
	b.Apply(aria.Page{Parts: partsFor(1, 20, "A")}, aria.Notify)
	tr.retarget(b, "aria2222", newSessionStatus("aria2222", time.Now()), 21)

	for name, fold := range map[string]map[nodeRef]bool{"expanded": tr.expanded, "adorned": tr.adorned} {
		if !fold[below] {
			t.Fatalf("%s lost a fold in the prefix both arias share", name)
		}
		if fold[above] {
			t.Fatalf("%s kept a fold from the aria we left", name)
		}
	}
}

func TestRetarget_AStrangerKeepsNoFolds(t *testing.T) {
	tr := forkPager(t, 40, 21, 24)
	tr.expanded[nodeRef{turn: 5}] = true
	tr.adorned[nodeRef{turn: 5}] = true

	b := aria.NewClient()
	b.Apply(aria.Page{Parts: partsFor(1, 3, "Z")}, aria.Notify)
	tr.retarget(b, "aria3333", newSessionStatus("aria3333", time.Now()), 0)

	if len(tr.expanded) != 0 || len(tr.adorned) != 0 {
		t.Fatalf("a stranger inherited %d expansions and %d adornments", len(tr.expanded), len(tr.adorned))
	}
}

// A head fork keeps everything and must not claim content above what it holds.
func TestRetain_AHeadForkKeepsTheWholeWindow(t *testing.T) {
	c := aria.NewClient()
	c.Apply(aria.Page{Parts: partsFor(1, 12, "A")}, aria.Notify)
	next := c.CloneBelow(13)

	if next.Count() != 12 {
		t.Fatalf("a head fork kept %d of 12 messages", next.Count())
	}
	if next.Store().More().After {
		t.Fatal("the store claims there is more above a window that lost nothing")
	}

	// A fork that drops turns says so.
	cut := c.CloneBelow(6)
	if !cut.Store().More().After {
		t.Fatal("the store dropped turns and does not say anything is above what it kept")
	}
}
