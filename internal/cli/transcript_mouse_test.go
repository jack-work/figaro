package cli

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/jack-work/figaro/api/livedoc"
	ldrender "github.com/jack-work/figaro/internal/livelog/render"
	"github.com/jack-work/figaro/internal/term"
	"github.com/jack-work/figaro/sdk/aria"
)

// A click resolves against the frame that was painted, so every case asserts
// through frameRefs rather than recomputing rows from t.offset.

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
	client.Apply(aria.Page{Parts: history}, aria.Notify)
	tr := newTranscript(ft, 80, h, &ariaView{settings: &renderSettings{}}, client, "aria1234", time.Now())
	tr.enter()
	tr.render()
	return tr, ft, history
}

func TestClickSelectsNodeUnderPointer(t *testing.T) {
	defer term.SetColorMode(term.ColorAlways)()
	tr, ft, _ := mouseFixture(t, 30)
	want := nodeRef{turn: 1, index: 1}
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
	// A bare point without the hash fails at paste time.
	if tr.selection.focus.hash == 0 {
		t.Fatal("clicked endpoint carries no node hash: copy would reject it")
	}
	tr.render()
	// The fake terminal holds cells, not renditions.
	if !washed(strings.Join(tr.lines(), "\n")) {
		t.Fatalf("no selection cue painted after a click:\n%s", strings.Join(ft.Screen(), "\n"))
	}
}

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

	// Expansion moved the rows under the pointer.
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

// A node with no collapsed form is not flipped invisibly.
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

// frameRefs is exactly the body.
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

// The pointer and the keyboard must produce the same selection, hash included.
func TestClickAgreesWithCtrlNSelection(t *testing.T) {
	trA, _, _ := mouseFixture(t, 30)
	trA.key(0x0e)
	keyboard := trA.selection

	trB, _, _ := mouseFixture(t, 30)
	if !trB.clickAt(clickRowOf(t, trB, keyboard.focus.nodeRef), false) {
		t.Fatal("click on the keyboard-selected node did nothing")
	}
	if trB.selection.focus != keyboard.focus || trB.selection.anchor != keyboard.anchor {
		t.Fatalf("click selection %+v != ^N selection %+v", trB.selection, keyboard)
	}
}

// The clicked row is on screen by construction, so scrolling is never justified.
func TestClickDoesNotMoveTheViewport(t *testing.T) {
	tr, _, _ := mouseFixture(t, 12)
	tr.follow = false
	tr.offset = 2
	tr.render()

	before := tr.offset
	// The last node row, not the bottom row: whether that is chrome is a
	// geometry accident.
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

// After a scroll, a click on row 0 must name the node now painted there.
func TestClickResolvesAgainstThePaintedFrame(t *testing.T) {
	tr, _, _ := mouseFixture(t, 12)
	tr.follow = false
	tr.offset = 0
	tr.render()
	firstRefs := append([]nodeRef(nil), tr.frameRefs...)

	tr.scrollBy(6)
	tr.render()
	// Unchanged refs after a scroll are the bug, not a reason to skip.
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

// frameRefs[i] describes the i'th painted body row, checked by text.
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

// The gap sentinel has no rows at all.
func TestRowRefsSkipSeparatorsAndGaps(t *testing.T) {
	e := &lineEntry{
		turn: 3, sep: true, start: 10,
		rows: []transcriptRow{
			{text: "header"},
			{text: "body", ref: nodeRef{turn: 3, index: 0}},
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

// Through the input loop with real bytes: decoded at all, and acting once
// despite arriving as press and release.
func clickBytes(col, row int, shift bool) []byte {
	base := 0
	if shift {
		base = 4
	}
	press := fmt.Sprintf("\x1b[<%d;%d;%dM", base, col, row)
	release := fmt.Sprintf("\x1b[<%d;%d;%dm", base, col, row)
	return []byte(press + release)
}

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

// A gesture firing on both reports toggles twice and appears never to fire.
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
