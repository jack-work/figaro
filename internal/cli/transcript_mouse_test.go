package cli

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/jack-work/figaro/internal/livedoc"
	"github.com/jack-work/figaro/internal/livelog/aria"
	ldrender "github.com/jack-work/figaro/internal/livelog/render"
)

// THE POINTER'S TESTS.
//
// Every one of these asserts through frameRefs — the map renderFrame records —
// because that is the property under test: a click resolves against the frame
// that was PAINTED. A test that computed the expected row from t.offset itself
// would agree with a broken implementation that did the same thing, which is
// trap #11 of the tmux-testing skill in miniature: two arms, one binary.

// clickRowOf finds the first painted body row belonging to ref. It fails the
// test rather than returning a sentinel: "no row for that node" means the
// fixture, not the assertion, is wrong.
func clickRowOf(t *testing.T, tr *transcript, ref nodeRef) int {
	t.Helper()
	for row, r := range tr.frameRefs {
		if r == ref {
			return row
		}
	}
	t.Fatalf("node %+v is not on the painted frame; refs = %+v", ref, tr.frameRefs)
	return -1
}

// chromeRow finds a painted body row that belongs to no node — a separator, a
// voice header, or a blank between two blocks.
func chromeRow(t *testing.T, tr *transcript) int {
	t.Helper()
	for row, r := range tr.frameRefs {
		if !r.valid() {
			return row
		}
	}
	t.Fatalf("frame has no chrome row; refs = %+v", tr.frameRefs)
	return -1
}

func mouseFixture(t *testing.T, h int) (*transcript, *ldrender.FakeTerminal, []aria.TurnPart) {
	t.Helper()
	ft := ldrender.NewFakeTerminal(80, h)
	client := aria.NewClient()
	lines := make([]string, 12)
	for i := range lines {
		lines[i] = fmt.Sprintf("line-%02d", i)
	}
	history := []aria.TurnPart{{Turn: aria.Turn{
		ID: 1, Sealed: true, Nodes: []livedoc.Node{
			{Type: livedoc.NodeProse, Markdown: "first node"},
			{Type: livedoc.NodeProse, Markdown: "second node"},
			{Type: livedoc.NodeTool, Name: "bash", Status: livedoc.StatusOK, Output: strings.Join(lines, "\n")},
		},
	}}}
	client.Apply(aria.Page{Parts: history})
	tr := newTranscript(ft, 80, h, &ariaView{settings: &renderSettings{}}, client, "aria1234", time.Now())
	tr.enter()
	tr.render()
	return tr, ft, history
}

// TestClickSelectsNodeUnderPointer is the first rule of the gesture.
func TestClickSelectsNodeUnderPointer(t *testing.T) {
	tr, ft, _ := mouseFixture(t, 30)
	want := nodeRef{turn: 1, index: 1} // the SECOND node: an off-by-one lands on the first
	row := clickRowOf(t, tr, want)

	if !tr.clickAt(row, false) {
		t.Fatal("click on a node row did nothing")
	}
	if !tr.selection.active || tr.selection.focus.nodeRef != want {
		t.Fatalf("selection = %+v, want focus %+v", tr.selection, want)
	}
	if tr.selection.anchor.nodeRef != want {
		t.Fatalf("a plain click must collapse the range onto one node: anchor = %+v", tr.selection.anchor)
	}
	// The hash is what makes a clicked selection yankable. A bare point would
	// pass every assertion above and then fail at paste time with "selection
	// start changed".
	if tr.selection.focus.hash == 0 {
		t.Fatal("clicked endpoint carries no node hash: copy would reject it")
	}
	tr.render()
	if screen := strings.Join(ft.Screen(), "\n"); !strings.Contains(screen, "▎") {
		t.Fatalf("no selection cue painted after a click:\n%s", screen)
	}
}

// TestClickOnFocusTogglesExpansion is the second rule: click again to expand,
// once more to collapse.
func TestClickOnFocusTogglesExpansion(t *testing.T) {
	tr, _, _ := mouseFixture(t, 30)
	tool := nodeRef{turn: 1, index: 2}
	row := clickRowOf(t, tr, tool)

	if !tr.clickAt(row, false) {
		t.Fatal("first click did not select")
	}
	if got := stripANSI(strings.Join(tr.lines(), "\n")); strings.Contains(got, "line-00") {
		t.Fatalf("selecting must not expand:\n%s", got)
	}
	tr.render()

	// Second click on the same node: expand.
	row = clickRowOf(t, tr, tool)
	if !tr.clickAt(row, false) {
		t.Fatal("second click on the focused node did nothing")
	}
	if got := stripANSI(strings.Join(tr.lines(), "\n")); !strings.Contains(got, "line-00") {
		t.Fatalf("second click did not expand the tool:\n%s", got)
	}
	if !tr.expanded[tool] {
		t.Fatal("expansion state not recorded")
	}
	tr.render()

	// Third click: collapse again. The row is re-derived because expansion moved
	// the rows under the pointer.
	row = clickRowOf(t, tr, tool)
	if !tr.clickAt(row, false) {
		t.Fatal("third click did not collapse")
	}
	if tr.expanded[tool] {
		t.Fatal("third click left the node expanded")
	}
	if got := stripANSI(strings.Join(tr.lines(), "\n")); strings.Contains(got, "line-00") {
		t.Fatalf("third click did not re-collapse:\n%s", got)
	}
}

// TestClickOnFocusedProseIsInert pins the seam: a node with no collapsed form is
// not flipped invisibly. When prose grows one (feat/table-wrap widens
// nodeExpandable) this test is the one that must be updated deliberately.
func TestClickOnFocusedProseIsInert(t *testing.T) {
	tr, _, _ := mouseFixture(t, 30)
	prose := nodeRef{turn: 1, index: 0}
	row := clickRowOf(t, tr, prose)
	if !tr.clickAt(row, false) {
		t.Fatal("first click did not select the prose node")
	}
	tr.render()
	row = clickRowOf(t, tr, prose)
	if tr.clickAt(row, false) {
		t.Fatal("a second click on a node with no collapsed form must report no-op")
	}
	if tr.expanded[prose] {
		t.Fatal("a non-expandable node must not be marked expanded")
	}
	if !tr.selection.active || tr.selection.focus.nodeRef != prose {
		t.Fatalf("an inert second click must not disturb the selection: %+v", tr.selection)
	}
}

// TestClickOnChromeIsNoOp: the rows between nodes are half the screen, and a
// stray click must not cost a selection.
func TestClickOnChromeIsNoOp(t *testing.T) {
	tr, _, _ := mouseFixture(t, 30)
	sel := nodeRef{turn: 1, index: 1}
	tr.clickAt(clickRowOf(t, tr, sel), false)
	tr.render()

	row := chromeRow(t, tr)
	if tr.clickable(row) {
		t.Fatalf("row %d reports clickable but carries no ref", row)
	}
	if tr.clickAt(row, false) {
		t.Fatalf("click on chrome row %d acted", row)
	}
	if !tr.selection.active || tr.selection.focus.nodeRef != sel {
		t.Fatalf("chrome click disturbed the selection: %+v", tr.selection)
	}
}

// TestClickOutsideBodyIsNoOp covers the footer rows and a row below the frame:
// frameRefs is exactly the BODY, so anything past it must miss.
func TestClickOutsideBodyIsNoOp(t *testing.T) {
	tr, _, _ := mouseFixture(t, 30)
	for _, row := range []int{-1, len(tr.frameRefs), len(tr.frameRefs) + 1, 1 << 20} {
		if tr.clickable(row) || tr.clickAt(row, false) {
			t.Errorf("row %d outside the body acted (body = %d rows)", row, len(tr.frameRefs))
		}
	}
	if tr.selection.active {
		t.Fatal("an out-of-body click started a selection")
	}
}

// TestShiftClickExtendsRange: pointing at the far end of a range must build the
// same selection as walking to it with Shift+^N.
func TestShiftClickExtendsRange(t *testing.T) {
	tr, _, history := mouseFixture(t, 30)
	first, third := nodeRef{turn: 1, index: 0}, nodeRef{turn: 1, index: 2}

	tr.clickAt(clickRowOf(t, tr, first), false)
	tr.render()
	if !tr.clickAt(clickRowOf(t, tr, third), true) {
		t.Fatal("shift-click did not extend")
	}
	if tr.selection.anchor.nodeRef != first || tr.selection.focus.nodeRef != third {
		t.Fatalf("range = %+v..%+v, want %+v..%+v",
			tr.selection.anchor.nodeRef, tr.selection.focus.nodeRef, first, third)
	}
	// The range must be COPYABLE, which is the whole point of carrying hashes.
	text, err := selectedTextForTest(tr, history)
	if err != nil {
		t.Fatalf("copy of a shift-clicked range failed: %v", err)
	}
	for _, want := range []string{"first node", "second node", "line-11"} {
		if !strings.Contains(text, want) {
			t.Fatalf("copied text missing %q:\n%s", want, text)
		}
	}
}

// TestClickAgreesWithCtrlNSelection is the equivalence proof: the pointer and
// the keyboard must produce the SAME selection for the same node, hash included.
// Two ways to name a node that disagree is the defect class the coordinate row
// and the ref list were unified to remove.
func TestClickAgreesWithCtrlNSelection(t *testing.T) {
	trA, _, _ := mouseFixture(t, 30)
	trA.key(0x0e) // ^N: cold entry selects a node
	keyboard := trA.selection

	trB, _, _ := mouseFixture(t, 30)
	if !trB.clickAt(clickRowOf(t, trB, keyboard.focus.nodeRef), false) {
		t.Fatal("click on the keyboard-selected node did nothing")
	}
	if trB.selection.focus != keyboard.focus || trB.selection.anchor != keyboard.anchor {
		t.Fatalf("click selection %+v != ^N selection %+v", trB.selection, keyboard)
	}
}

// TestClickDoesNotMoveTheViewport is property 2 of the gesture. The clicked row
// is on screen by construction, so scrolling is never justified — and a tall
// node whose tail runs off the bottom is exactly where ensureSelectionVisible
// would drag the page down, which is the jump selectNode's cold path was fixed
// to stop making.
func TestClickDoesNotMoveTheViewport(t *testing.T) {
	tr, _, _ := mouseFixture(t, 12) // short pane: the tool node overflows it
	tr.follow = false
	tr.offset = 2
	tr.render()

	before := tr.offset
	// The LAST node row on the frame, not literally the bottom row: whether the
	// bottom row is chrome is a geometry accident, and skipping on it is how a
	// test quietly stops testing. The property under test is "a click on the
	// furthest-down node does not scroll".
	row := -1
	for i := len(tr.frameRefs) - 1; i >= 0; i-- {
		if tr.frameRefs[i].valid() {
			row = i
			break
		}
	}
	if row < 0 {
		t.Fatalf("no node rows on a %d-row frame: fixture no longer exercises its path", len(tr.frameRefs))
	}
	if !tr.clickAt(row, false) {
		t.Fatalf("click on the last node row (%d) did nothing", row)
	}
	if tr.offset != before {
		t.Fatalf("click moved the viewport: offset %d -> %d", before, tr.offset)
	}
}

// TestClickResolvesAgainstThePaintedFrame is the anti-staleness test. After a
// scroll, screen row 0 holds a DIFFERENT node than it did before; a click on it
// must name the node now painted there. An implementation that cached refs at
// pager entry, or that recomputed them from a stale offset, passes every other
// test in this file and fails this one.
func TestClickResolvesAgainstThePaintedFrame(t *testing.T) {
	tr, _, _ := mouseFixture(t, 12)
	tr.follow = false
	tr.offset = 0
	tr.render()
	firstRefs := append([]nodeRef(nil), tr.frameRefs...)

	tr.scrollBy(6)
	tr.render()
	// NOT A SKIP. If the visible refs did not change after a six-line scroll, the
	// click map is stale — which is precisely the bug this test exists to catch.
	// Skipping here is how the canary run came back green with frameRefs cached
	// once at pager entry ("does the fixture still exercise its own path?").
	if sameRefs(firstRefs, tr.frameRefs) {
		t.Fatalf("frameRefs unchanged after scrolling %d lines: the row->node map is stale\n before %+v\n after  %+v",
			6, firstRefs, tr.frameRefs)
	}
	clicked := 0
	for row, ref := range tr.frameRefs {
		if !ref.valid() {
			continue
		}
		if !tr.clickAt(row, false) {
			t.Fatalf("click on row %d (ref %+v) did nothing", row, ref)
		}
		if tr.selection.focus.nodeRef != ref {
			t.Fatalf("row %d after scrolling selected %+v, want %+v",
				row, tr.selection.focus.nodeRef, ref)
		}
		clicked++
	}
	if clicked == 0 {
		t.Fatal("no node rows on the frame after scrolling: fixture no longer exercises its path")
	}
}

func sameRefs(a, b []nodeRef) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// TestFrameRefsAlignWithPaintedRows is the invariant the whole gesture rests on:
// frameRefs[i] describes the same row as the i'th painted body row. It is
// checked against the row TEXT, so a drift of one (the separator-height bug this
// arithmetic is most likely to grow) shows up as a node whose ref points at a
// row that does not contain its content.
func TestFrameRefsAlignWithPaintedRows(t *testing.T) {
	tr, _, _ := mouseFixture(t, 30)
	tr.follow = false
	tr.offset = 0
	tr.render()

	body, _ := tr.layout(len(tr.footLines()))
	if len(tr.frameRefs) > body {
		t.Fatalf("frameRefs has %d rows, body is %d", len(tr.frameRefs), body)
	}
	rows := tr.window(tr.offset, tr.offset+body, nil)
	if len(rows) != len(tr.frameRefs) {
		t.Fatalf("painted %d rows but recorded %d refs", len(rows), len(tr.frameRefs))
	}
	// Spot-check the mapping semantically: the row a prose node's ref points at
	// must contain that node's text.
	want := map[nodeRef]string{
		{turn: 1, index: 0}: "first node",
		{turn: 1, index: 1}: "second node",
	}
	for ref, text := range want {
		found := false
		for i, r := range tr.frameRefs {
			if r == ref && strings.Contains(stripANSI(rows[i]), text) {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("no painted row carries both ref %+v and %q", ref, text)
		}
	}
}

// TestRowRefsSkipSeparatorsAndGaps pins refAt's arithmetic directly, including
// the gap sentinel — which has no rows at all and whose entry is exactly where a
// naive `rel < len(e.rows)` check reads out of a nil slice.
func TestRowRefsSkipSeparatorsAndGaps(t *testing.T) {
	e := &lineEntry{
		turn: 3, sep: true, start: 10,
		rows: []transcriptRow{
			{text: "header"}, // no ref: chrome
			{text: "body", ref: nodeRef{turn: 3, index: 0}}, // a node row
		},
	}
	if got := e.refAt(0); got.valid() {
		t.Errorf("separator blank resolved to %+v", got)
	}
	if got := e.refAt(1); got.valid() {
		t.Errorf("separator rule resolved to %+v", got)
	}
	if got := e.refAt(sepRows); got.valid() {
		t.Errorf("voice header row resolved to %+v", got)
	}
	if got := e.refAt(sepRows + 1); got != (nodeRef{turn: 3, index: 0}) {
		t.Errorf("node row resolved to %+v", got)
	}
	if got := e.refAt(sepRows + 2); got.valid() {
		t.Errorf("past the last row resolved to %+v", got)
	}

	gap := &lineEntry{start: 0, gap: &aria.Gap{}}
	if got := gap.refAt(0); got.valid() {
		t.Errorf("gap sentinel resolved to %+v", got)
	}
}

// ---------------------------------------------------------------------------
// The byte level. Everything above drives clickAt directly; these two drive the
// INPUT LOOP with the bytes a terminal actually sends, because that is the only
// place two properties are visible: that a report is decoded at all, and that a
// click acts ONCE despite arriving as two reports.
// ---------------------------------------------------------------------------

// clickBytes is the pair of SGR reports a real left click produces: press, then
// release, at 1-based (col, row).
func clickBytes(col, row int, shift bool) []byte {
	base := 0
	if shift {
		base = 4
	}
	press := fmt.Sprintf("\x1b[<%d;%d;%dM", base, col, row)
	release := fmt.Sprintf("\x1b[<%d;%d;%dm", base, col, row)
	return []byte(press + release)
}

// firstToolRow finds a painted row belonging to a tool node with output — the
// one node kind that currently has a collapsed form to toggle.
func firstToolRow(t *testing.T, tr *transcript) (int, nodeRef) {
	t.Helper()
	for row, ref := range tr.frameRefs {
		if !ref.valid() {
			continue
		}
		if n, ok := tr.nodeAt(ref); ok && nodeExpandable(n) {
			return row, ref
		}
	}
	t.Fatal("no expandable node on the painted frame")
	return -1, nodeRef{}
}

// TestClickReportsActOncePerClick is the press/release rule. A gesture that
// fired on both reports would toggle twice per click and so appear never to fire
// at all — the failure is INVISIBLE to any test that calls clickAt directly,
// which is exactly why this one goes through the bytes.
func TestClickReportsActOncePerClick(t *testing.T) {
	out := &countingWriter{}
	in, lt := coalesceInput(t, out)
	tr := lt.tr
	tr.render()

	row, ref := firstToolRow(t, tr)
	if rest, stop := in.consume(clickBytes(5, row+1, false)); stop || len(rest) != 0 {
		t.Fatalf("click bytes not fully consumed: rest=%q stop=%v", rest, stop)
	}
	if !tr.selection.active || tr.selection.focus.nodeRef != ref {
		t.Fatalf("click bytes did not select %+v: %+v", ref, tr.selection)
	}
	if tr.expanded[ref] {
		t.Fatal("the FIRST click expanded: selection and expansion collapsed into one gesture")
	}

	// Second click on the same node: exactly one toggle, not two.
	tr.render()
	row, ref2 := firstToolRow(t, tr)
	if ref2 != ref {
		t.Fatalf("fixture moved: expected still %+v, got %+v", ref, ref2)
	}
	if rest, stop := in.consume(clickBytes(5, row+1, false)); stop || len(rest) != 0 {
		t.Fatalf("second click bytes not consumed: rest=%q stop=%v", rest, stop)
	}
	if !tr.expanded[ref] {
		t.Fatal("second click did not expand: the release probably undid the press")
	}
}

// TestWheelReportsStillScroll guards the gesture that already existed: adding a
// button case to the mouse switch must not swallow the wheel.
func TestWheelReportsStillScroll(t *testing.T) {
	out := &countingWriter{}
	in, lt := coalesceInput(t, out)
	tr := lt.tr
	tr.render()
	before := tr.offset
	if rest, stop := in.consume(wheelReports(4, true)); stop || len(rest) != 0 {
		t.Fatalf("wheel bytes not consumed: rest=%q stop=%v", rest, stop)
	}
	if tr.offset >= before {
		t.Fatalf("wheel up did not scroll: offset %d -> %d", before, tr.offset)
	}
}
