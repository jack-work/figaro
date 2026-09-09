package cli

import (
	"encoding/json"
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

// headEntryOf is the entry that starts a turn: the one that draws its question.
func headEntryOf(tr *transcript, turn int) (*lineEntry, bool) {
	for k := range tr.index.entries {
		e := &tr.index.entries[k]
		if e.turn == turn && !e.isGap() && e.key.from() == 0 {
			return e, true
		}
	}
	return nil, false
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
	head, ok := headEntryOf(tr, 2)
	if !ok {
		t.Fatal("fixture: turn 2 is not held")
	}
	block := len(tr.stickyBlockOf(2).rows)
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
// a boundary and pins the seam: what the header shows has left the body, the
// body picks up exactly where the block left off, and nothing stands twice.
func TestSticky_HandsRowsBackAcrossATurnBoundary(t *testing.T) {
	tr := stickyPager(t, 1, 3, 8, 24)
	head, ok := headEntryOf(tr, 2)
	if !ok {
		t.Fatal("fixture: turn 2 is not held")
	}
	base := entryRowsStart(head)
	q := tr.stickyBlockOf(2)

	for above := 0; above <= len(q.rows); above++ {
		tr.offset = base + above
		tr.buildIndex()
		gotTurn, gotAbove := tr.stickyTurn()
		if gotTurn != 2 || gotAbove != above {
			t.Fatalf("at offset %d: stickyTurn = (%d, %d), want (2, %d)", tr.offset, gotTurn, gotAbove, above)
		}
		body := plain(bodyRows(tr))
		if above < len(q.rows) && len(body) > 0 {
			if body[0] != strings.TrimRight(stripANSI(q.rows[above].text), " ") {
				t.Fatalf("at offset %d the body starts at %q, want %q: the header and the body must meet",
					tr.offset, body[0], q.rows[above].text)
			}
		}
		for _, row := range plain(headRowsOf(tr)) {
			if strings.TrimSpace(row) == "" || strings.HasPrefix(strings.TrimSpace(row), "\u2500") {
				continue
			}
			// Every pinned row is one of the question's own, from above the
			// body. The first wears the turn's address at the right edge.
			found := false
			for i := q.textLo; i < min(q.textHigh, above); i++ {
				want := strings.TrimRight(stripANSI(q.rows[i].text), " ")
				if row == want || strings.HasPrefix(row, want) || strings.HasPrefix(want, strings.TrimRight(row, "0123456789 ")) {
					found = true
				}
			}
			if !found {
				t.Fatalf("at offset %d the header shows %q, which is not question text above the body", tr.offset, row)
			}
			for _, b := range body {
				if b == row {
					t.Fatalf("at offset %d the row %q stands in the header and the body at once", tr.offset, row)
				}
			}
		}
	}
}

// TestSticky_ReleasesAtTheNextQuestion: the header lets go the moment the top
// row belongs to the next turn. The separator is that turn's own overline, so
// the release happens there, one pair of rows before its question.
func TestSticky_ReleasesAtTheNextQuestion(t *testing.T) {
	tr := stickyPager(t, 1, 3, 8, 24)
	next, ok := headEntryOf(tr, 3)
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

	if _, ok := headEntryOf(tr, 4); ok {
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
	head, ok := headEntryOf(tr, 2)
	if !ok {
		t.Fatal("fixture: turn 2 is not held")
	}
	q := tr.stickyBlockOf(2)
	if len(q.rows) == 0 {
		t.Fatal("turn 2 composed no question")
	}
	for i, r := range q.rows {
		if got := head.rows[i]; got.text != r.text || got.ref != r.ref {
			t.Fatalf("row %d differs between the header and the body:\n header %q %+v\n body   %q %+v",
				i, r.text, r.ref, got.text, got.ref)
		}
	}
	if q.empty() {
		t.Fatal("the question has no text rows of its own")
	}
}

// TestSticky_TakesNoRoomWhenItPinsNothing: blank rows are not held above a
// conversation that is already showing its own question, and what the header
// paints is exactly what the frame reserved for it.
func TestSticky_TakesNoRoomWhenItPinsNothing(t *testing.T) {
	tr := stickyPager(t, 1, 4, 6, 24)
	tr.view.(*ariaView).settings.sticky = false
	bare, _ := tr.layout(len(tr.footLines()))
	tr.view.(*ariaView).settings.sticky = true

	pinned, bareSeen := 0, 0
	for off := 0; off < tr.index.total; off++ {
		tr.offset = off
		tr.buildIndex()
		body, _ := tr.layout(len(tr.footLines()))
		rows := headRowsOf(tr)
		if len(rows) != tr.headRows() {
			t.Fatalf("at offset %d the header painted %d rows, reserved %d", off, len(rows), tr.headRows())
		}
		switch len(rows) {
		case 0:
			bareSeen++
			if body != bare {
				t.Fatalf("at offset %d nothing is pinned but the body is %d rows, want %d", off, body, bare)
			}
		default:
			pinned++
			if body != bare-(stickyText+1) {
				t.Fatalf("at offset %d a pinned question left the body %d rows, want %d", off, body, bare-(stickyText+1))
			}
		}
	}
	if pinned == 0 || bareSeen == 0 {
		t.Fatalf("the walk never saw both states: %d pinned, %d bare", pinned, bareSeen)
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
	_ = off
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

// richTurn is the shape a real exchange has: an attributed question, the form
// state that arrived with it, and a tall answer.
func richTurn(id uint64, nodes int) aria.TurnPart {
	var ns []livedoc.Node
	for n := range nodes {
		ns = append(ns, livedoc.Node{Type: livedoc.NodeProse, Markdown: fmt.Sprintf("NODE%d-%d", id, n)})
	}
	return aria.TurnPart{Turn: aria.Turn{
		ID: id, Inquiry: fmt.Sprintf("please commit %d", id), Sealed: true,
		InquirySegments: []aria.InquirySegment{{Sender: "Gluck", Text: fmt.Sprintf("please commit %d", id)}},
		FormDeltas: map[string]livedoc.FormDelta{
			"@f.datetime": {
				Value: json.RawMessage(`"Tuesday, September 8, 2026"`),
				Kind:  livedoc.FormBound, Event: livedoc.FormSet, Form: "@f",
			},
		},
		Nodes: ns,
	}}
}

func richPager(t testing.TB, turns, nodes, h int) *transcript {
	t.Helper()
	client := aria.NewClient()
	var parts []aria.TurnPart
	for i := range turns {
		parts = append(parts, richTurn(uint64(i+1), nodes))
	}
	client.Apply(aria.Page{Parts: parts}, aria.Notify)
	view := &ariaView{settings: &renderSettings{sticky: true}}
	tr := newTranscript(ldrender.NewFakeTerminal(64, h), 64, h, view, client, "aria1234", time.Time{})
	tr.enter()
	tr.follow = false
	tr.buildIndex()
	return tr
}

// TestSticky_PinsTheQuestionNotTheFormDeltas: the state that arrived with a
// question is not the question. Scrolled past the block, the header names the
// exchange.
func TestSticky_PinsTheQuestionNotTheFormDeltas(t *testing.T) {
	tr := richPager(t, 3, 8, 24)
	head, ok := headEntryOf(tr, 2)
	if !ok {
		t.Fatal("fixture: turn 2 is not held")
	}
	q := tr.stickyBlockOf(2)
	if q.textHigh >= len(q.rows) {
		t.Fatal("fixture: the block carries no form deltas below its text")
	}
	tr.offset = entryRowsStart(head) + len(q.rows) + 1
	tr.buildIndex()

	pinned := strings.Join(plain(headRowsOf(tr)), "\n")
	if !strings.Contains(pinned, "please commit 2") {
		t.Fatalf("the header does not name the exchange:\n%s", pinned)
	}
	if strings.Contains(pinned, "Figaro saw") {
		t.Fatalf("the header pinned a form delta instead of the question:\n%s", pinned)
	}
}

// TestSticky_NamesTheTurn: a question pinned out of its place in the
// conversation carries its address, against the right edge as ^O draws every
// other one.
func TestSticky_NamesTheTurn(t *testing.T) {
	tr := richPager(t, 3, 8, 24)
	head, _ := headEntryOf(tr, 2)
	q := tr.stickyBlockOf(2)
	tr.offset = entryRowsStart(head) + len(q.rows) + 1
	tr.buildIndex()

	rows := plain(headRowsOf(tr))
	if len(rows) == 0 {
		t.Fatal("nothing pinned")
	}
	if !strings.HasSuffix(rows[0], "2") {
		t.Fatalf("the pinned question does not carry its turn at the right edge:\n%s", strings.Join(rows, "\n"))
	}
	if strings.Contains(rows[0], "2 Gluck") {
		t.Fatalf("the address was written into the attribution instead of the edge: %q", rows[0])
	}
}

// TestSticky_LiveTurnDoesNotStandTwice is the defect a shadowing open turn
// produced: the header counted rows against the turn's first entry while the
// body was painting its open copy, so the question was drawn in both.
func TestSticky_LiveTurnDoesNotStandTwice(t *testing.T) {
	client := aria.NewClient()
	client.Apply(aria.Page{Parts: []aria.TurnPart{richTurn(1, 6)}}, aria.Notify)
	// Turn 2 opens: its question commits, then its answer streams.
	client.Apply(aria.Page{Parts: []aria.TurnPart{{Turn: aria.Turn{
		ID: 2, Inquiry: "please commit", InquirySegments: []aria.InquirySegment{{Sender: "Gluck", Text: "please commit"}},
	}}}}, aria.Notify)
	for i := range 8 {
		client.Apply(aria.Page{Parts: []aria.TurnPart{{Turn: aria.Turn{ID: 2, Live: &aria.Live{
			From: 0, V: i, Nodes: []aria.NodeDelta{{ID: uint64(i), Set: map[string]any{
				"type": "prose", "markdown": fmt.Sprintf("NODE2-%d", i),
			}}},
		}}}}}, aria.Notify)
	}

	view := &ariaView{settings: &renderSettings{sticky: true}}
	tr := newTranscript(ldrender.NewFakeTerminal(64, 16), 64, 16, view, client, "aria1234", time.Time{})
	tr.enter()
	tr.follow = false
	tr.buildIndex()

	for off := range tr.index.total {
		tr.offset = off
		tr.buildIndex()
		body := plain(bodyRows(tr))
		head := plain(headRowsOf(tr))
		if len(head) == 0 {
			continue
		}
		for _, row := range head[:len(head)-1] { // the last row is the rule
			if strings.TrimSpace(row) == "" {
				continue
			}
			for _, b := range body {
				if b == row && strings.TrimSpace(b) != "" {
					t.Fatalf("at offset %d the row %q stands in the header and the body at once:\nheader %q\nbody %q",
						off, row, plain(headRowsOf(tr)), body)
				}
			}
		}
	}
}

// TestSticky_JumpTravelsBetweenQuestions: with a question pinned the unit of
// travel is the exchange.
func TestSticky_JumpTravelsBetweenQuestions(t *testing.T) {
	tr := richPager(t, 4, 6, 24)
	starts := tr.stickyStarts()
	if len(starts) < 3 {
		t.Fatalf("fixture: %d questions in the window, want at least 3", len(starts))
	}
	tr.offset = starts[0]
	tr.buildIndex()

	tr.stickyJump(1)
	if tr.offset != starts[1] {
		t.Fatalf("forward one question landed at %d, want %d", tr.offset, starts[1])
	}
	// It lands on the question, which then speaks for itself rather than being
	// pinned above the body.
	if _, above := tr.stickyTurn(); above != 0 {
		t.Fatalf("landing on a question pinned %d of its rows", above)
	}
	tr.stickyJump(-1)
	if tr.offset != starts[0] {
		t.Fatalf("back one question landed at %d, want %d", tr.offset, starts[0])
	}
	// The last question is as far as it goes: the walk stops rather than
	// wrapping or falling off the end.
	for range len(starts) + 2 {
		tr.stickyJump(1)
	}
	if _, maxOff := tr.layout(len(tr.footLines())); tr.offset > maxOff {
		t.Fatalf("the walk ran past the end: offset %d, max %d", tr.offset, maxOff)
	}
}

// TestSticky_EllipsisMarksAQuestionTooTallToPin.
func TestSticky_EllipsisMarksAQuestionTooTallToPin(t *testing.T) {
	client := aria.NewClient()
	long := strings.TrimSpace(strings.Repeat("a very long question that will wrap across several rows ", 6))
	client.Apply(aria.Page{Parts: []aria.TurnPart{{Turn: aria.Turn{
		ID: 1, Inquiry: long, Sealed: true,
		InquirySegments: []aria.InquirySegment{{Sender: "Gluck", Text: long}},
		Nodes: []livedoc.Node{
			{Type: livedoc.NodeProse, Markdown: "NODE1-0"},
			{Type: livedoc.NodeProse, Markdown: "NODE1-1"},
			{Type: livedoc.NodeProse, Markdown: "NODE1-2"},
		},
	}}}}, aria.Notify)
	view := &ariaView{settings: &renderSettings{sticky: true}}
	tr := newTranscript(ldrender.NewFakeTerminal(64, 16), 64, 16, view, client, "aria1234", time.Time{})
	tr.enter()
	tr.follow = false
	tr.buildIndex()
	q := tr.stickyBlockOf(1)
	if q.textHigh-q.textLo <= stickyText {
		t.Fatalf("fixture: the question is %d rows, want more than %d", q.textHigh-q.textLo, stickyText)
	}
	tr.offset = len(q.rows) + 1
	tr.buildIndex()

	pinned := plain(headRowsOf(tr))
	if !strings.Contains(strings.Join(pinned, "\n"), strings.TrimSpace(stickyEllipsis)) {
		t.Fatalf("a question too tall to pin was not marked:\n%s", strings.Join(pinned, "\n"))
	}
}

// TestSticky_PinsTheHeadOfTheQuestionNotAMiddleChunk is the defect a reader
// sees as two broken paragraphs: the header showed the rows nearest the body,
// so a tall question was cut at an arbitrary row and continued under the rule.
func TestSticky_PinsTheHeadOfTheQuestionNotAMiddleChunk(t *testing.T) {
	client := aria.NewClient()
	var text []string
	for i := range 12 {
		text = append(text, fmt.Sprintf("QLINE%02d some words of the question that go on and on", i))
	}
	long := strings.Join(text, " ")
	client.Apply(aria.Page{Parts: []aria.TurnPart{{Turn: aria.Turn{
		ID: 6, Inquiry: long, Sealed: true,
		InquirySegments: []aria.InquirySegment{{Sender: "Gluck", Text: long}},
		Nodes: []livedoc.Node{
			{Type: livedoc.NodeProse, Markdown: "ANSWER-0"},
			{Type: livedoc.NodeProse, Markdown: "ANSWER-1"},
		},
	}}}}, aria.Notify)
	view := &ariaView{settings: &renderSettings{sticky: true}}
	tr := newTranscript(ldrender.NewFakeTerminal(56, 20), 56, 20, view, client, "aria1234", time.Time{})
	tr.enter()
	tr.follow = false
	tr.buildIndex()

	q := tr.stickyBlockOf(6)
	if q.textHigh-q.textLo < 6 {
		t.Fatalf("fixture: the question is %d rows, want a tall one", q.textHigh-q.textLo)
	}
	head := strings.TrimRight(stripANSI(q.rows[q.textLo].text), " ")
	second := strings.TrimRight(stripANSI(q.rows[q.textLo+1].text), " ")

	// Walk the whole question through the top of the body.
	shown := 0
	for above := 1; above <= len(q.rows); above++ {
		tr.offset = above
		tr.buildIndex()
		pinned := plain(headRowsOf(tr))
		if len(pinned) == 0 {
			continue
		}
		rows := pinned[:len(pinned)-1]
		if strings.TrimSpace(strings.Join(rows, "")) == "" {
			continue
		}
		shown++
		if !strings.HasPrefix(rows[0], head) || !strings.HasPrefix(rows[1], second[:20]) {
			t.Fatalf("at offset %d the header shows a middle chunk:\n got %q\nwant the head %q / %q",
				tr.offset, rows, head, second)
		}
		if !strings.Contains(rows[1], strings.TrimSpace(stickyEllipsis)) {
			t.Fatalf("at offset %d a question the header could not fit was not marked: %q", tr.offset, rows[1])
		}
		for _, b := range plain(bodyRows(tr)) {
			for _, r := range rows {
				if strings.TrimSpace(b) != "" && b == r {
					t.Fatalf("at offset %d the row %q stands in the header and the body at once", tr.offset, r)
				}
			}
		}
	}
	if shown == 0 {
		t.Fatal("the header never appeared while a tall question scrolled past")
	}
}
