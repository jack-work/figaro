package cli

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/jack-work/figaro/api/livedoc"
	ldrender "github.com/jack-work/figaro/internal/livelog/render"

	"github.com/jack-work/figaro/sdk/aria"
)

// The header shows exactly what the body no longer does, at every offset.

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

func headEntryOf(tr *transcript, turn int) (*lineEntry, bool) {
	for k := range tr.index.entries {
		e := &tr.index.entries[k]
		if e.turn == turn && !e.isGap() && e.key.from() == 0 {
			return e, true
		}
	}
	return nil, false
}

// The header stands on the body rows rather than pushing them down.
func bodyRows(tr *transcript) []string {
	body, _ := tr.layout(len(tr.footLines()))
	return tr.window(tr.offset, tr.offset+body, nil)
}

func screenRows(tr *transcript) []string {
	head, body := headRowsOf(tr), bodyRows(tr)
	if len(head) > len(body) {
		head = head[:len(body)]
	}
	return append(append([]string{}, head...), body[len(head):]...)
}

func headRowsOf(tr *transcript) []string {
	return tr.stickyLines(tr.activeHighlight(), tr.selectionSpan())
}

func headTextOf(tr *transcript) []string {
	rows := plain(headRowsOf(tr))
	if n := len(rows); n > 0 && strings.Contains(rows[n-1], "─") {
		return rows[:n-1]
	}
	return rows
}

func plain(rows []string) []string {
	out := make([]string, 0, len(rows))
	for _, r := range rows {
		out = append(out, strings.TrimRight(stripANSI(r), " "))
	}
	return out
}

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

func TestSticky_PinsTheQuestionOnceItLeavesTheBody(t *testing.T) {
	tr := stickyPager(t, 1, 3, 12, 24)
	head, ok := headEntryOf(tr, 2)
	if !ok {
		t.Fatal("fixture: turn 2 is not held")
	}
	block := len(tr.stickyBlockOf(2).rows)
	tr.offset = entryRowsStart(head) + block + 2
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
		pinned := headTextOf(tr)
		// Pinned rows are the block's own head rows, in order; the first wears
		// the heading glyph and the turn address.
		for i, row := range pinned {
			want := strings.TrimRight(stripANSI(q.rows[i].text), " ")
			if i == 0 {
				if !strings.HasPrefix(row, "∨") {
					t.Fatalf("at offset %d the heading row is %q", tr.offset, row)
				}
				continue
			}
			clipped := strings.TrimRight(strings.TrimSuffix(row, ".."), " ")
			if row != want && !strings.HasPrefix(want, clipped) {
				t.Fatalf("at offset %d the header shows %q, which is not row %d of the block (%q)",
					tr.offset, row, i, want)
			}
		}
		for _, row := range pinned[min(1, len(pinned)):] {
			if strings.TrimSpace(row) == "" {
				continue
			}
			for _, b := range body[min(len(headRowsOf(tr)), len(body)):] {
				if b == row {
					t.Fatalf("at offset %d the row %q stands in the header and the body at once", tr.offset, row)
				}
			}
		}
	}
}

// The separator is the next turn's own overline, so the release happens there.
func TestSticky_ReleasesAtTheNextQuestion(t *testing.T) {
	tr := stickyPager(t, 1, 3, 8, 24)
	next, ok := headEntryOf(tr, 3)
	if !ok {
		t.Fatal("fixture: turn 3 is not held")
	}
	base := entryRowsStart(next)

	tr.offset = next.start - 1
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

// Paging into the middle of a tall turn holds no slice carrying the question.
func TestSticky_PinsAQuestionWhoseHeadIsNotHeld(t *testing.T) {
	client := aria.NewClient()
	var nodes []livedoc.Node
	for n := range 30 {
		nodes = append(nodes, livedoc.Node{Type: livedoc.NodeProse, Markdown: fmt.Sprintf("NODE%d", n)})
	}
	// The wire states the question on every part; only the head slice draws it.
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

func TestSticky_DoesNotShortenTheConversation(t *testing.T) {
	tr := stickyPager(t, 1, 4, 6, 24)
	tr.view.(*ariaView).settings.sticky = false
	bare, _ := tr.layout(len(tr.footLines()))
	tr.view.(*ariaView).settings.sticky = true

	pinned, bareSeen := 0, 0
	for off := 0; off < tr.index.total; off++ {
		tr.offset = off
		tr.buildIndex()
		body, _ := tr.layout(len(tr.footLines()))
		if body != bare {
			t.Fatalf("at offset %d the header took %d rows of the conversation", off, bare-body)
		}
		if len(headRowsOf(tr)) == 0 {
			bareSeen++
		} else {
			pinned++
		}
	}
	if pinned == 0 || bareSeen == 0 {
		t.Fatalf("the walk never saw both states: %d pinned, %d bare", pinned, bareSeen)
	}
}

func TestSticky_OffCostsNothing(t *testing.T) {
	tr := stickyPager(t, 1, 3, 6, 24)
	on, _ := tr.layout(len(tr.footLines()))
	tr.view.(*ariaView).settings.sticky = false
	off, _ := tr.layout(len(tr.footLines()))
	if off != on {
		t.Fatalf("the mode changed the geometry: %d rows off, %d on", off, on)
	}
	if rows := headRowsOf(tr); rows != nil {
		t.Fatalf("the mode is off and %d header rows were painted", len(rows))
	}
}

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
			continue
		}
		k := tr.index.entryAt(off)
		if k < 0 || tr.index.entries[k].isGap() {
			continue
		}
		if got := tr.index.entries[k].turn; got != turn {
			t.Fatalf("at offset %d the header names turn %d, the top row belongs to %d", off, turn, got)
		}
		// The body may hold a copy under the header; the reader must see one.
		want := fmt.Sprintf("QUESTION%d", turn)
		screen := plain(screenRows(tr))
		if len(screen) < stickyText {
			continue
		}
		seen := 0
		for _, row := range screen {
			if strings.Contains(row, want) {
				seen++
			}
		}
		if seen != 1 {
			t.Fatalf("at offset %d (turn %d, %d rows above) the question stands %d times on screen:\n%s",
				off, turn, above, seen, strings.Join(screen, "\n"))
		}
	}
}

func richTurn(id uint64, nodes int) aria.TurnPart {
	var ns []livedoc.Node
	for n := range nodes {
		ns = append(ns, livedoc.Node{Type: livedoc.NodeProse, Markdown: fmt.Sprintf("NODE%d-%d", id, n)})
	}
	return aria.TurnPart{Turn: aria.Turn{
		ID: id, Inquiry: fmt.Sprintf("please commit %d", id), Sealed: true,
		InquirySegments: []aria.InquirySegment{{Sender: "Duke", Text: fmt.Sprintf("please commit %d", id)}},
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

func TestSticky_PinsTheQuestionNotTheFormDeltas(t *testing.T) {
	tr := richPager(t, 3, 8, 24)
	head, ok := headEntryOf(tr, 2)
	if !ok {
		t.Fatal("fixture: turn 2 is not held")
	}
	// Collapsed, the delta list draws no rows at all.
	inq := nodeRef{turn: 2, index: inquiryNode}
	tr.metaOpen[inq] = true
	tr.dropTurnsRows(map[int]struct{}{2: {}})
	tr.buildIndex()
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
	if strings.Contains(pinned, "datetime") {
		t.Fatalf("the header pinned a form delta instead of the question:\n%s", pinned)
	}
}

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
	_ = rows
	if !strings.HasSuffix(rows[0], "2 Δ") {
		t.Fatalf("the pinned question does not carry its turn at the right edge:\n%s", strings.Join(rows, "\n"))
	}
	if strings.Contains(rows[0], "2 Duke") {
		t.Fatalf("the address was written into the attribution instead of the edge: %q", rows[0])
	}
}

// A shadowing open turn must not let the header count rows against the
// turn's first entry while the body paints its open copy.
func TestSticky_LiveTurnDoesNotStandTwice(t *testing.T) {
	client := aria.NewClient()
	client.Apply(aria.Page{Parts: []aria.TurnPart{richTurn(1, 6)}}, aria.Notify)
	client.Apply(aria.Page{Parts: []aria.TurnPart{{Turn: aria.Turn{
		ID: 2, Inquiry: "please commit", InquirySegments: []aria.InquirySegment{{Sender: "Duke", Text: "please commit"}},
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
		seen := map[string]int{}
		for _, row := range plain(screenRows(tr)) {
			if strings.TrimSpace(row) == "" {
				continue
			}
			if seen[row]++; seen[row] > 1 && strings.Contains(row, "please commit") {
				t.Fatalf("at offset %d the row %q is on screen twice:\n%s",
					off, row, strings.Join(plain(screenRows(tr)), "\n"))
			}
		}
	}
}

func TestSticky_EllipsisMarksAQuestionTooTallToPin(t *testing.T) {
	client := aria.NewClient()
	long := strings.TrimSpace(strings.Repeat("a very long question that will wrap across several rows ", 6))
	client.Apply(aria.Page{Parts: []aria.TurnPart{{Turn: aria.Turn{
		ID: 1, Inquiry: long, Sealed: true,
		InquirySegments: []aria.InquirySegment{{Sender: "Duke", Text: long}},
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

func TestSticky_PinsTheHeadOfTheQuestionNotAMiddleChunk(t *testing.T) {
	client := aria.NewClient()
	var text []string
	for i := range 12 {
		text = append(text, fmt.Sprintf("QLINE%02d some words of the question that go on and on", i))
	}
	long := strings.Join(text, " ")
	client.Apply(aria.Page{Parts: []aria.TurnPart{{Turn: aria.Turn{
		ID: 6, Inquiry: long, Sealed: true,
		InquirySegments: []aria.InquirySegment{{Sender: "Duke", Text: long}},
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

	shown := 0
	for above := 1; above <= len(q.rows); above++ {
		tr.offset = above
		tr.buildIndex()
		rows := headTextOf(tr)
		if len(rows) < 2 {
			continue
		}
		shown++
		if !strings.HasPrefix(rows[0], "∨ Duke") || !strings.HasPrefix(rows[1], head[:20]) {
			t.Fatalf("at offset %d the header shows a middle chunk: got %q, want metadata then %q", tr.offset, rows, head)
		}
		taller := q.textLo+len(rows)-1 < q.textHigh
		if marked := strings.Contains(rows[len(rows)-1], strings.TrimSpace(stickyEllipsis)); marked != taller {
			t.Fatalf("at offset %d the question is taller=%v but the header marked=%v: %q",
				tr.offset, taller, marked, rows[len(rows)-1])
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

// The conversation scrolls under the header, one row per keystroke.
func TestSticky_EveryKeystrokeMovesOneRow(t *testing.T) {
	tr := richPager(t, 3, 10, 20)
	tr.follow = false
	tr.offset = tr.index.total - 1
	tr.render()

	held := 0
	for range tr.index.total - 1 {
		if tr.offset == 0 {
			break
		}
		before := plain(screenRows(tr))
		beforeHead := len(headRowsOf(tr))
		if beforeHead > 0 {
			held++
		}
		tr.key('k')
		after := plain(screenRows(tr))
		if len(after) == 0 || len(before) == 0 {
			continue
		}
		if strings.Join(before, "\n") == strings.Join(after, "\n") {
			t.Fatalf("at offset %d a keystroke changed nothing (header held %d rows):\n%s",
				tr.offset, beforeHead, strings.Join(after, "\n"))
		}
		if n := min(len(before), len(after)) - 1; n > 2 &&
			strings.Join(before[:n-1], "\n") != strings.Join(after[1:n], "\n") {
			// Only the rows below the header have to line up.
			h := max(beforeHead, len(headRowsOf(tr)))
			if h+2 < n && strings.Join(before[h:n-1], "\n") != strings.Join(after[h+1:n], "\n") {
				t.Fatalf("at offset %d the screen moved by more than a row:\n before %q\n after  %q",
					tr.offset, before[h:n], after[h:n])
			}
		}
	}
	if held == 0 {
		t.Fatal("the walk never held a question")
	}
}

func TestSticky_MergesIntoTheBlockWithoutASeam(t *testing.T) {
	tr := richPager(t, 3, 8, 24)
	head, ok := headEntryOf(tr, 2)
	if !ok {
		t.Fatal("fixture: turn 2 is not held")
	}
	base := entryRowsStart(head)
	q := tr.stickyBlockOf(2)

	var heights []int
	for above := len(q.rows); above >= 0; above-- {
		tr.offset = base + above
		tr.buildIndex()
		rows := headTextOf(tr)
		heights = append(heights, len(rows))

		// No rule while the block itself is still on screen.
		full := plain(headRowsOf(tr))
		ruled := len(full) > 0 && strings.Contains(full[len(full)-1], "─")
		if want := above >= len(q.rows); ruled != want && len(rows) > 0 {
			t.Fatalf("at %d rows above (block is %d rows) the header ruled=%v, want %v",
				above, len(q.rows), ruled, want)
		}
		var want []transcriptRow
		if len(rows) > 0 {
			if !strings.HasPrefix(rows[0], "∨ Duke") {
				t.Fatalf("missing sticky metadata: %q", rows[0])
			}
			want = q.rows[q.textLo : q.textLo+len(rows)-1]
		}
		for i, r := range want {
			got := rows[i+1]
			text := strings.TrimRight(stripANSI(r.text), " ")
			if !strings.HasPrefix(got, text) && !strings.HasPrefix(text, strings.TrimSuffix(got, stickyEllipsis)) {
				t.Fatalf("at %d rows above, header row %d = %q, want the question's %q", above, i, got, text)
			}
		}
		if body := plain(bodyRows(tr)); above < len(q.rows) && len(body) > 0 {
			if got, want := body[0], strings.TrimRight(stripANSI(q.rows[above].text), " "); got != want {
				t.Fatalf("at %d rows above the body starts at %q, want %q", above, got, want)
			}
		}
	}
	for i := 1; i < len(heights); i++ {
		if heights[i] > heights[i-1] {
			t.Fatalf("scrolling up grew the header: %v", heights)
		}
	}
	if heights[len(heights)-1] != 0 {
		t.Fatalf("the header outlived the block: %v", heights)
	}
}

// The header holds the block's own first two rows, heading included, so it
// lets go only when nothing of the block is above.
func TestSticky_HeadingIsPinnedFromTheFirstRowScrolled(t *testing.T) {
	question := "REMINDER (figla): the baseline rebuild is due now, and this " +
		"first line is long enough to wrap more than once at sixty columns.\n\n" +
		"--watch git status\n\nNothing to cancel."
	client := aria.NewClient()
	var nodes []livedoc.Node
	for n := range 12 {
		nodes = append(nodes, livedoc.Node{Type: livedoc.NodeProse, Markdown: fmt.Sprintf("NODE%d", n)})
	}
	client.Apply(aria.Page{Parts: []aria.TurnPart{{Turn: aria.Turn{
		ID: 1, Sealed: true, Inquiry: question, Nodes: nodes,
		InquirySegments: []aria.InquirySegment{{Sender: "Duke", Text: question}},
	}}}}, aria.Notify)
	view := &ariaView{settings: &renderSettings{sticky: true}}
	tr := newTranscript(ldrender.NewFakeTerminal(60, 16), 60, 16, view, client, "aria1234", time.Time{})
	tr.enter()
	tr.follow = false
	tr.buildIndex()

	q := tr.stickyBlockOf(1)
	if q.textHigh-q.textLo < 4 {
		t.Fatalf("fixture: the question must run to several rows, got %d", q.textHigh-q.textLo)
	}
	first := strings.TrimRight(stripANSI(q.rows[1].text), " ")
	for above := 1; above < len(q.rows); above++ {
		tr.offset = above
		tr.buildIndex()
		screen := plain(screenRows(tr))
		if !strings.Contains(screen[0], "Duke") {
			t.Fatalf("%d rows scrolled: the heading is gone:\n%s", above, strings.Join(screen[:3], "\n"))
		}
		if got := strings.TrimRight(strings.TrimSuffix(screen[1], ".."), " "); !strings.HasPrefix(first, got) {
			t.Fatalf("%d rows scrolled: the pinned line is %q, want the head of %q", above, got, first)
		}
	}
}
