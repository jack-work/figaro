package cli

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/jack-work/figaro/api/livedoc"
	ldrender "github.com/jack-work/figaro/internal/livelog/render"

	"github.com/jack-work/figaro/internal/livelog/aria"
)

// The header is the question's own rows, pinned as they leave the top of the
// body. These pin the seam: what the header shows must be exactly what the
// body no longer does, at every position of a scroll through a turn boundary.

func stickyPager(t testing.TB, firstTurn, turns, nodes, h int) *transcript {
	t.Helper()
	client := aria.NewClient()
	var parts []aria.TurnPart
	for i := range turns {
		id := uint64(firstTurn + i)
		var ns []livedoc.Node
		for n := range nodes {
			ns = append(ns, livedoc.Node{Type: livedoc.NodeProse, Markdown: fmt.Sprintf("NODE%d-%d", id, n)})
		}
		parts = append(parts, aria.TurnPart{Turn: aria.Turn{
			ID: id, Inquiry: fmt.Sprintf("QUESTION%d", id), Sealed: true, Nodes: ns,
		}})
	}
	client.Apply(aria.Page{Parts: parts}, aria.Notify)
	view := &ariaView{settings: &renderSettings{sticky: true}}
	tr := newTranscript(ldrender.NewFakeTerminal(60, h), 60, h, view, client, "aria1234", time.Time{})
	tr.enter()
	tr.follow = false
	tr.buildIndex()
	return tr
}

// bodyRows is what the frame paints below the header.
func bodyRows(tr *transcript) []string {
	body, _ := tr.layout(len(tr.footLines()))
	return tr.window(tr.offset, tr.offset+body, nil)
}

func headRowsOf(tr *transcript) []string {
	return tr.stickyLines(tr.activeHighlight(), tr.selectionSpan())
}

func plain(rows []string) []string {
	out := make([]string, 0, len(rows))
	for _, r := range rows {
		out = append(out, strings.TrimRight(stripANSI(r), " "))
	}
	return out
}

// TestSticky_NothingPinnedAtTheHeadOfATurn: with the question on screen there
// is nothing to stand in for it.
func TestSticky_NothingPinnedAtTheHeadOfATurn(t *testing.T) {
	tr := stickyPager(t, 1, 3, 4, 24)
	tr.offset = 0
	tr.buildIndex()
	for _, row := range plain(headRowsOf(tr)) {
		if strings.Contains(row, "QUESTION") {
			t.Fatalf("the question is in the body; nothing may be pinned:\n%q", row)
		}
	}
}

// TestSticky_PinsTheQuestionOnceItLeavesTheBody is the property the header
// exists for: inside a turn's answer, its question is still on screen.
func TestSticky_PinsTheQuestionOnceItLeavesTheBody(t *testing.T) {
	tr := stickyPager(t, 1, 3, 12, 24)
	head, ok := tr.headEntry(2)
	if !ok {
		t.Fatal("fixture: turn 2 is not held")
	}
	block := len(tr.stickyBlock(2))
	tr.offset = entryRowsStart(head) + block + 2 // inside turn 2's answer
	tr.buildIndex()

	if turn, above := tr.stickyTurn(); turn != 2 || above != block {
		t.Fatalf("stickyTurn = (%d, %d), want turn 2 with all %d rows above", turn, above, block)
	}
	if !strings.Contains(strings.Join(plain(headRowsOf(tr)), "\n"), "QUESTION2") {
		t.Fatalf("turn 2's question is not pinned:\n%s", strings.Join(plain(headRowsOf(tr)), "\n"))
	}
	for _, row := range plain(bodyRows(tr)) {
		if strings.Contains(row, "QUESTION2") {
			t.Fatal("the question is in the body and pinned at once")
		}
	}
}

// TestSticky_HandsRowsBackAcrossATurnBoundary walks one line at a time through
// a boundary and asserts the seam: every row of the question is either in the
// body or in the header, never both, and the header holds the rows nearest the
// body in order.
func TestSticky_HandsRowsBackAcrossATurnBoundary(t *testing.T) {
	tr := stickyPager(t, 1, 3, 8, 24)
	head, ok := tr.headEntry(2)
	if !ok {
		t.Fatal("fixture: turn 2 is not held")
	}
	base := entryRowsStart(head)
	block := tr.stickyBlock(2)

	for above := 0; above <= len(block); above++ {
		tr.offset = base + above
		tr.buildIndex()
		gotTurn, gotAbove := tr.stickyTurn()
		if gotTurn != 2 || gotAbove != above {
			t.Fatalf("at offset %d: stickyTurn = (%d, %d), want (2, %d)", tr.offset, gotTurn, gotAbove, above)
		}
		pinned := plain(headRowsOf(tr))
		// The rule closes the header; above it sit the rows just departed.
		want := block[max(0, above-stickyText):above]
		got := pinned[stickyText-len(want) : stickyText]
		for i := range want {
			if got[i] != strings.TrimRight(stripANSI(want[i].text), " ") {
				t.Fatalf("at offset %d, header row %d = %q, want the body row it replaced %q",
					tr.offset, i, got[i], want[i].text)
			}
		}
		body := plain(bodyRows(tr))
		if len(body) > 0 && above < len(block) {
			if body[0] != strings.TrimRight(stripANSI(block[above].text), " ") {
				t.Fatalf("at offset %d the body starts at %q, want %q: the header and the body must meet",
					tr.offset, body[0], block[above].text)
			}
		}
	}
}

// TestSticky_ReleasesAtTheNextQuestion: the header lets go the moment the top
// row belongs to the next turn. The separator is that turn's own overline, so
// the release happens there, one pair of rows before its question.
func TestSticky_ReleasesAtTheNextQuestion(t *testing.T) {
	tr := stickyPager(t, 1, 3, 8, 24)
	next, ok := tr.headEntry(3)
	if !ok {
		t.Fatal("fixture: turn 3 is not held")
	}
	base := entryRowsStart(next)

	tr.offset = next.start - 1 // the last row of turn 2
	tr.buildIndex()
	if turn, above := tr.stickyTurn(); turn != 2 || above == 0 {
		t.Fatalf("inside turn 2: stickyTurn = (%d, %d), want turn 2 pinned", turn, above)
	}
	for _, off := range []int{next.start, base} {
		tr.offset = off
		tr.buildIndex()
		if turn, above := tr.stickyTurn(); turn != 3 || above != 0 {
			t.Fatalf("at offset %d: stickyTurn = (%d, %d), want (3, 0): turn 3 speaks for itself", off, turn, above)
		}
	}
}

// TestSticky_PinsAQuestionWhoseHeadIsNotHeld is the case the pager could not
// answer before the fold was unified: paging into the middle of a tall turn
// holds no slice carrying the question, and the header must still name it.
func TestSticky_PinsAQuestionWhoseHeadIsNotHeld(t *testing.T) {
	client := aria.NewClient()
	var nodes []livedoc.Node
	for n := range 30 {
		nodes = append(nodes, livedoc.Node{Type: livedoc.NodeProse, Markdown: fmt.Sprintf("NODE%d", n)})
	}
	// A backward page that opens mid-turn: the wire states the question on
	// every part of the turn, and only the head slice draws it.
	client.Apply(aria.Page{Parts: []aria.TurnPart{{
		Turn:        aria.Turn{ID: 4, Inquiry: "DEEPQUESTION", Sealed: true, Nodes: nodes[20:]},
		From:        20,
		ClippedHead: true,
	}}}, aria.Notify)

	view := &ariaView{settings: &renderSettings{sticky: true}}
	tr := newTranscript(ldrender.NewFakeTerminal(60, 24), 60, 24, view, client, "aria1234", time.Time{})
	tr.enter()
	tr.follow = false
	tr.buildIndex()

	if _, ok := tr.headEntry(4); ok {
		t.Fatal("fixture: the head slice must not be held")
	}
	if turn, above := tr.stickyTurn(); turn != 4 || above == 0 {
		t.Fatalf("stickyTurn = (%d, %d), want turn 4 wholly above the body", turn, above)
	}
	if !strings.Contains(strings.Join(plain(headRowsOf(tr)), "\n"), "DEEPQUESTION") {
		t.Fatalf("the question of a turn held only by its tail is not pinned:\n%s",
			strings.Join(plain(headRowsOf(tr)), "\n"))
	}
	for _, row := range plain(bodyRows(tr)) {
		if strings.Contains(row, "DEEPQUESTION") {
			t.Fatal("a clipped-head slice drew the question in the body")
		}
	}
}

// TestSticky_HeaderIsTheSameComponentAsTheBody: the pinned rows are composed
// by the path that draws the block inline, so the two cannot drift.
func TestSticky_HeaderIsTheSameComponentAsTheBody(t *testing.T) {
	tr := stickyPager(t, 1, 2, 4, 24)
	head, ok := tr.headEntry(2)
	if !ok {
		t.Fatal("fixture: turn 2 is not held")
	}
	block := tr.stickyBlock(2)
	if len(block) == 0 {
		t.Fatal("turn 2 composed no question")
	}
	for i, r := range block {
		if got := head.rows[i]; got.text != r.text || got.ref != r.ref {
			t.Fatalf("row %d differs between the header and the body:\n header %q %+v\n body   %q %+v",
				i, r.text, r.ref, got.text, got.ref)
		}
	}
}

// TestSticky_ReservationDoesNotVaryWithScroll: a header that changed height as
// the reader moved would change the body height, and with it the offset it is
// derived from.
func TestSticky_ReservationDoesNotVaryWithScroll(t *testing.T) {
	tr := stickyPager(t, 1, 4, 6, 24)
	want, _ := tr.layout(len(tr.footLines()))
	for off := 0; off < tr.index.total; off++ {
		tr.offset = off
		tr.buildIndex()
		if got, _ := tr.layout(len(tr.footLines())); got != want {
			t.Fatalf("at offset %d the body is %d rows, want %d everywhere", off, got, want)
		}
		if got := len(headRowsOf(tr)); got != tr.headRows() {
			t.Fatalf("at offset %d the header painted %d rows, reserved %d", off, got, tr.headRows())
		}
	}
}

// TestSticky_OffCostsNothing: with the mode off no rows are reserved and none
// are painted.
func TestSticky_OffCostsNothing(t *testing.T) {
	tr := stickyPager(t, 1, 3, 6, 24)
	on, _ := tr.layout(len(tr.footLines()))
	tr.view.(*ariaView).settings.sticky = false
	off, _ := tr.layout(len(tr.footLines()))
	if off != on+stickyText+1 {
		t.Fatalf("the mode off gives the body %d rows, on gives %d: the difference must be the reservation", off, on)
	}
	if rows := headRowsOf(tr); rows != nil {
		t.Fatalf("the mode is off and %d header rows were painted", len(rows))
	}
}

// TestSticky_ClickLandsOnTheNodeUnderThePointer: the header displaces the body,
// so a screen row addresses a node only after the reservation is accounted for.
func TestSticky_ClickLandsOnTheNodeUnderThePointer(t *testing.T) {
	tr := stickyPager(t, 1, 3, 6, 24)
	tr.offset = 0
	tr.render()

	for r := range tr.headRows() {
		if tr.clickable(r) {
			t.Fatalf("screen row %d is header chrome and must address no node", r)
		}
	}
	body, _ := tr.layout(len(tr.footLines()))
	refs := tr.rowRefs(tr.offset, tr.offset+body, nil)
	for i, want := range refs {
		if got := tr.frameRefs[tr.headRows()+i]; got != want {
			t.Fatalf("body row %d addresses %+v, want %+v", i, got, want)
		}
	}
}

// stickyHistory is a wire of tall turns, each with its question: the shape a
// scroll-up walks back through.
func stickyHistory(turns, nodes int) []aria.TurnPart {
	out := make([]aria.TurnPart, 0, turns)
	for i := range turns {
		id := uint64(i + 1)
		var ns []livedoc.Node
		for n := range nodes {
			ns = append(ns, livedoc.Node{Type: livedoc.NodeProse, Markdown: fmt.Sprintf("NODE%d-%d", id, n)})
		}
		out = append(out, aria.TurnPart{Turn: aria.Turn{
			ID: id, Inquiry: fmt.Sprintf("QUESTION%d", id), Sealed: true, Nodes: ns,
		}})
	}
	return out
}

// TestSticky_FollowsAWalkIntoHistory: paging older turns in and scrolling up
// through them, the header names the turn the reader is inside at every step,
// and never a turn whose question is on screen.
func TestSticky_FollowsAWalkIntoHistory(t *testing.T) {
	history := stickyHistory(40, 6)
	client := aria.NewClient()
	applyTail(client, readBefore(history, recentCursor, transcriptPageSize))
	view := &ariaView{settings: &renderSettings{sticky: true}}
	tr := newTranscript(ldrender.NewFakeTerminal(60, 20), 60, 20, view, client, "aria1234", time.Time{})
	tr.enter()
	tr.follow = false

	for range 6 {
		tr.offset = 0
		tr.buildIndex()
		if !pageOnce(tr, history) {
			break
		}
	}
	tr.buildIndex()
	if tr.index.total == 0 {
		t.Fatal("fixture: the walk loaded nothing")
	}

	for off := 0; off < tr.index.total; off++ {
		tr.offset = off
		tr.buildIndex()
		turn, above := tr.stickyTurn()
		if turn == 0 {
			continue // a hole carries no turn
		}
		k := tr.index.entryAt(off)
		if k < 0 || tr.index.entries[k].isGap() {
			continue
		}
		if got := tr.index.entries[k].turn; got != turn {
			t.Fatalf("at offset %d the header names turn %d, the top row belongs to %d", off, turn, got)
		}
		// The promise: while the reader is inside a turn, its question is on
		// screen exactly once, in the header or in the body.
		want := fmt.Sprintf("QUESTION%d", turn)
		inHead := strings.Contains(strings.Join(plain(headRowsOf(tr)), "\n"), want)
		inBody := strings.Contains(strings.Join(plain(bodyRows(tr)), "\n"), want)
		if inHead == inBody {
			t.Fatalf("at offset %d (turn %d, %d rows above) the question is in head=%v body=%v",
				off, turn, above, inHead, inBody)
		}
	}
}
