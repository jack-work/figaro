package cli

import (
	"testing"
	"time"

	"github.com/jack-work/figaro/internal/livelog/aria"
	ldrender "github.com/jack-work/figaro/internal/livelog/render"
)

// The shelf: what a session keeps of the arias it has already shown.
//
// The property that makes it safe is the one retention rests on: history below
// the live edge is append-only and a fork point is sealed, so a parked store is
// a true PREFIX of the aria it names. It can be short. It cannot be wrong.

func parkPager(t testing.TB, turns int, tag, id string) (*transcript, *aria.Client) {
	t.Helper()
	c := aria.NewClient()
	c.Apply(aria.Page{Parts: partsFor(1, turns, tag)}, aria.Notify)
	view := &ariaView{settings: &renderSettings{}}
	tr := newTranscript(ldrender.NewFakeTerminal(60, 24), 60, 24, view, c, id, time.Time{})
	tr.enter()
	tr.buildIndex()
	return tr, c
}

// TestPark_AdoptGivesBackTheStoreAndThePosition: the hop back is free, and it
// lands where the reader left, not at the tail. `here` is false: the window
// the reader is leaving has no overlap to honour.
func TestPark_AdoptGivesBackTheStoreAndThePosition(t *testing.T) {
	tr, c := parkPager(t, 12, "A", "aria1111")
	tr.follow = false
	tr.from = aria.Anchor{Turn: 4}
	tr.offset = 9
	tr.lines()
	rows := len(tr.rowCache)
	if rows == 0 {
		t.Fatal("fixture: no rows were drawn to park")
	}

	p := tr.park("aria1111")
	if p == nil {
		t.Fatal("parking gave back nothing")
	}
	// THE ASSERTION HERE IS THE OUTCOME, NOT THE MECHANISM. Isolation is real,
	// but it is not made by copying at this instant: the switch that follows
	// mints its own client and its own maps from these, so what is parked is
	// the object that was live and nothing writes to it again. The property is
	// pinned on the real path by TestParkedParentSurvivesRelativeRetarget and
	// by TestPark_TheShelfIsNotTheLiveSubject; a pointer comparison here would
	// pin only how it is done.
	if p.client != c {
		t.Fatal("parking did not shelve the client that was live")
	}
	if p.messages() != 12 {
		t.Fatalf("parked %d messages, want 12", p.messages())
	}

	// Somewhere else, then back.
	b := aria.NewClient()
	b.Apply(aria.Page{Parts: partsFor(1, 3, "B")}, aria.Notify)
	tr.retarget(b, "aria2222", newSessionStatus("aria2222", time.Now()), 0)
	tr.adopt(p, newSessionStatus("aria1111", time.Now()), false, 0)

	if tr.client != p.client {
		t.Fatal("the adopted pager is not on the parked store")
	}
	if tr.from.Turn != 4 || tr.offset != 9 || tr.follow {
		t.Fatalf("the reader landed at (turn %d, offset %d, follow %v), want (4, 9, false)",
			tr.from.Turn, tr.offset, tr.follow)
	}
	if len(tr.rowCache) != rows {
		t.Fatalf("the adopted pager holds %d cached rows, want the %d it parked", len(tr.rowCache), rows)
	}
	if !tr.kept {
		t.Fatal("a subject adopted in history must not be dragged to the tail")
	}
}

// TestPark_TheShelfIsBounded: the cost of the shelf is a count of arias, and
// the oldest visit is what goes.
func TestPark_TheShelfIsBounded(t *testing.T) {
	in := &interactiveInput{}
	tr, _ := parkPager(t, 4, "A", "a")
	in.lt = &livelogTurn{tr: tr}

	for _, id := range []string{"a", "b", "c", "d", "e"} {
		c := aria.NewClient()
		c.Apply(aria.Page{Parts: partsFor(1, 4, id)}, aria.Notify)
		tr.client = c
		tr.rowCache = map[sliceKey]cachedMessage{}
		tr.stickyCache = map[sliceKey]stickyQuestion{}
		tr.expanded = map[nodeRef]bool{}
		in.parkSubject(id)
	}
	if len(in.parked) != parkedSubjects {
		t.Fatalf("the shelf holds %d arias, want %d", len(in.parked), parkedSubjects)
	}
	for _, gone := range []string{"a", "b"} {
		if in.parked[gone] != nil {
			t.Fatalf("%q is still on the shelf; the oldest visit goes first", gone)
		}
	}
	for _, held := range []string{"c", "d", "e"} {
		if in.parked[held] == nil {
			t.Fatalf("%q fell off the shelf early", held)
		}
	}

	// Taking one off removes it, so a second hop to the same aria does not
	// hand two owners the same store.
	if p := in.takeParked("d"); p == nil {
		t.Fatal("d was on the shelf and could not be taken")
	}
	if p := in.takeParked("d"); p != nil {
		t.Fatal("d came off the shelf twice")
	}
}

// An empty subject is not worth a shelf slot, and parking one would evict a
// real one.
func TestPark_NothingIsNotParked(t *testing.T) {
	in := &interactiveInput{}
	tr, _ := parkPager(t, 4, "A", "a")
	tr.client = aria.NewClient()
	in.lt = &livelogTurn{tr: tr}
	in.parkSubject("empty")
	if len(in.parked) != 0 {
		t.Fatalf("an empty aria took a shelf slot")
	}
	in.parkSubject("")
	if len(in.parked) != 0 {
		t.Fatalf("an unnamed aria took a shelf slot")
	}
}

// THE SCREEN OUTRANKS THE MEMORY. A reader parked on the prefix two arias
// share keeps that position when the target comes off the shelf: the rows are
// the same rows, and the position remembered from the last visit would move a
// screen that did not need to move.
func TestPark_AdoptKeepsTheScreenOverTheRememberedPosition(t *testing.T) {
	tr, _ := parkPager(t, 12, "A", "aria1111")
	tr.follow = true
	p := tr.park("aria1111")

	b := aria.NewClient()
	b.Apply(aria.Page{Parts: partsFor(1, 12, "A")}, aria.Notify)
	tr.retarget(b, "aria2222", newSessionStatus("aria2222", time.Now()), 0)
	tr.follow = false
	tr.from = aria.Anchor{Turn: 3}
	tr.offset = 5

	tr.adopt(p, newSessionStatus("aria1111", time.Now()), true, 12)
	if tr.client != p.client {
		t.Fatal("the adopted pager is not on the parked store")
	}
	if tr.from.Turn != 3 || tr.offset != 5 || tr.follow {
		t.Fatalf("the screen moved to (turn %d, offset %d, follow %v); it was standing on shared rows",
			tr.from.Turn, tr.offset, tr.follow)
	}
}

// THE SHELF IS DROPPED WHEN THE TREE CHANGES SHAPE. The turns below a fork
// point cannot change, but which arias exist and what they inherit can, and
// the shelf is where an answer from before the change would still be sitting.
func TestPark_AnEpochChangeEmptiesTheShelf(t *testing.T) {
	in := &interactiveInput{lineageEpoch: 7}
	tr, _ := parkPager(t, 4, "A", "a")
	in.lt = &livelogTurn{tr: tr}
	in.parkSubject("a")
	if len(in.parked) != 1 {
		t.Fatal("fixture: nothing was parked")
	}
	in.dropParked()
	if len(in.parked) != 0 || len(in.parkOrder) != 0 {
		t.Fatalf("the shelf still holds %d arias and %d slots", len(in.parked), len(in.parkOrder))
	}
}
