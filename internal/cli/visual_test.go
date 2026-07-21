package cli

import (
	"strings"
	"testing"
)

func TestVisualSpans_CharLineColumn(t *testing.T) {
	rows := []string{"alpha beta", "gamma", "delta epsilon"}
	vis := func(r int) string { return rows[r] }

	// charwise, reversed endpoints normalize; ends inclusive
	spans := visualSpans(visualChar, visualPos{2, 2}, visualPos{0, 6}, vis)
	if len(spans) != 3 {
		t.Fatalf("char spans = %d, want 3", len(spans))
	}
	if got := visualYank(spans, vis); got != "beta\ngamma\ndel" {
		t.Fatalf("char yank = %q", got)
	}

	// linewise
	spans = visualSpans(visualLine, visualPos{1, 3}, visualPos{0, 7}, vis)
	if got := visualYank(spans, vis); got != "alpha beta\ngamma" {
		t.Fatalf("line yank = %q", got)
	}

	// columnwise rectangle over cols [2..4]
	spans = visualSpans(visualColumn, visualPos{0, 2}, visualPos{2, 4}, vis)
	if got := visualYank(spans, vis); got != "pha\nmma\nlta" {
		t.Fatalf("column yank = %q", got)
	}
}

func TestVisualSpans_WideRunesColumn(t *testing.T) {
	rows := []string{"ab宽cd", "vwxyz"}
	vis := func(r int) string { return rows[r] }
	// display cols: a=0 b=1 宽=2-3 c=4 d=5. Rectangle cols [2..4] includes 宽+c.
	spans := visualSpans(visualColumn, visualPos{0, 2}, visualPos{1, 4}, vis)
	// endpoint col 4 is inclusive: x(2) y(3) z(4)
	if got := visualYank(spans, vis); got != "宽c\nxyz" {
		t.Fatalf("wide-rune column yank = %q", got)
	}
}

func TestVisual_KeyFlowAndYank(t *testing.T) {
	tr := searchFixture(t, 6)
	tr.key('g')
	tr.key('g')
	tr.key('v') // charwise from viewport top (no prior cursor)
	if tr.vmode != visualChar || !tr.hasCursor {
		t.Fatalf("v must enter charwise visual with a cursor")
	}
	tr.key('j')
	tr.key('l')
	tr.key('l')
	text, ok := tr.visualYankText()
	if !ok || text == "" {
		t.Fatalf("yank returned nothing (ok=%v)", ok)
	}
	if !strings.Contains(text, "\n") {
		t.Fatalf("two-row charwise yank should span rows: %q", text)
	}
	if tr.vmode != visualOff {
		t.Fatalf("yank must exit visual mode")
	}

	// V linewise: whole rows, UI chrome included (header row selectable).
	// gg inside visual moves the cursor endpoint to row 0 — the "‹ figaro"
	// header — proving chrome is in the selection space.
	tr.key('V')
	tr.key('g')
	tr.key('g')
	text, _ = tr.visualYankText()
	if !strings.Contains(text, "‹ figaro") {
		t.Fatalf("linewise yank should include rendered UI chrome, got %q", text)
	}

	// Ctrl-V column; Esc drops the selection without yanking
	tr.key(0x16)
	if tr.vmode != visualColumn {
		t.Fatalf("Ctrl-V must enter column mode")
	}
	tr.key(0x1b)
	if tr.vmode != visualCursor {
		t.Fatalf("Esc from a selection drops to cursor mode (vim), got %d", tr.vmode)
	}
	tr.key('q')
	if tr.vmode != visualOff {
		t.Fatalf("q must leave cursor mode")
	}
	if !tr.active {
		t.Fatalf("visual interactions must never exit the locked pager")
	}
}

func TestVisual_SearchPlacesCursorAndNExtends(t *testing.T) {
	tr := searchFixture(t, 9)
	tr.key('g')
	tr.key('g')
	tr.findQuery("msg0[46]")
	if !tr.hasCursor {
		t.Fatalf("search landing must place the cursor")
	}
	row0, _ := tr.pointToRow(tr.vCursor)
	if lt := tr.lineLT[row0]; lt != 4 {
		t.Fatalf("cursor row should be on Msg04 (LT 4), got LT %d", lt)
	}
	if col := tr.vCursor.col; col == 0 {
		t.Fatalf("cursor col should sit at the match offset, got 0")
	}
	tr.key('v') // anchor at the match
	tr.key('n') // extend to the next match
	if tr.vmode != visualChar {
		t.Fatalf("n in visual mode must stay in visual mode")
	}
	ar, _ := tr.pointToRow(tr.vAnchor)
	cr, _ := tr.pointToRow(tr.vCursor)
	if ar != row0 || cr == row0 {
		t.Fatalf("n must move the cursor endpoint only (anchor %d cursor %d)", ar, cr)
	}
	if lt := tr.vCursor.lt; lt != 6 {
		t.Fatalf("n should extend the cursor to Msg06 (LT 6), got LT %d", lt)
	}
	text, ok := tr.visualYankText()
	// vim-inclusive: the selection ends AT the cursor char, so Msg05's full
	// row is inside and Msg06 contributes its match start.
	if !ok || !strings.Contains(text, "Msg04") || !strings.Contains(text, "Msg05") {
		t.Fatalf("selection should span match to match: %q", text)
	}
}

func TestVisual_OverlayComposesWithStyledRows(t *testing.T) {
	styled := "\x1b[2mhello \x1b[0mworld"
	out := overlayVisual(styled, 3, 8) // visible "lo wo"
	if !strings.Contains(out, visualBgOn) || !strings.Contains(out, visualBgOff) {
		t.Fatalf("overlay missing: %q", out)
	}
	if !strings.Contains(out, "\x1b[0m"+visualBgOn) {
		t.Fatalf("bg must re-assert across the row's own reset: %q", out)
	}
}

// Review-round regressions: caret anchoring, same-row wrap, vanished-endpoint
// yank safety, and viewport-relative n outside visual mode.
func TestVisual_ReviewRegressions(t *testing.T) {
	// F5: a ^-anchored pattern must not re-anchor at the cursor offset.
	p, _ := compileSearch("^Msg")
	if idx, ok := p.matchIndexAfter("xxMsgyy", 2); ok {
		t.Fatalf("^ pattern re-anchored mid-row at %d", idx)
	}
	if idx, ok := p.matchIndexAfter("Msgyy", 0); !ok || idx != 0 {
		t.Fatalf("^ pattern should match at the real line start")
	}

	// F7: wrap must reach other matches on the cursor's own row.
	tr := searchFixture(t, 3)
	tr.rowCache = map[int]cachedMessage{}
	// craft a single row with three matches by searching the prompt-like row
	tr.findQuery("body")
	_, c0, _ := tr.resolveCursor()
	tr.key('n') // advances (across rows or wrapping) without getting stuck
	_, c1, _ := tr.resolveCursor()
	r0, _ := tr.pointToRow(tr.vCursor)
	if r0 < 0 || (c0 == c1 && r0 == 0) {
		t.Fatalf("n made no progress (col %d -> %d)", c0, c1)
	}

	// F2: a yank whose endpoint message vanished must refuse, not mis-copy.
	tr2 := searchFixture(t, 5)
	tr2.key('v')
	tr2.vAnchor = visualPoint{lt: 999, within: 0, col: 3} // not in window
	if text, ok := tr2.visualYankText(); ok || text != "" {
		t.Fatalf("yank with a vanished endpoint must be refused, got %q", text)
	}
	if tr2.vmode != visualOff {
		t.Fatalf("refused yank still exits visual mode")
	}

	// F4: outside visual mode, an off-screen cursor must not be the origin.
	tr3 := searchFixture(t, 40)
	tr3.key('g')
	tr3.key('g')
	tr3.findQuery("msg02") // cursor near the top
	tr3.key('G')           // scroll far away (normal mode, cursor stays)
	all := tr3.lines()
	row, _, useCol := tr3.searchOrigin(all)
	if useCol || row != tr3.offset {
		t.Fatalf("off-screen cursor must yield viewport origin (row %d offset %d useCol %v)", row, tr3.offset, useCol)
	}
}

// Backward search ('?'): n follows the search direction, N reverses (vim).
func TestVisual_BackwardSearchDirection(t *testing.T) {
	tr := searchFixture(t, 9)
	tr.key('G')
	tr.key('?')
	for _, c := range []byte("msg0[357]") {
		tr.key(c)
	}
	tr.key(0x0d)
	if !tr.searchBack {
		t.Fatalf("? commit must set backward direction")
	}
	first := tr.vCursor.lt
	tr.key('n') // n continues BACKWARD
	second := tr.vCursor.lt
	if second >= first {
		t.Fatalf("n after ? must move backward (lt %d -> %d)", first, second)
	}
	tr.key('N') // N reverses: forward
	if tr.vCursor.lt != first {
		t.Fatalf("N after ? must move forward (lt %d, want %d)", tr.vCursor.lt, first)
	}
}

// Word motions in cursor mode; 'i' clears node selection; q/Esc ladder.
func TestVisual_MotionsAndModalLadder(t *testing.T) {
	tr := searchFixture(t, 4)
	tr.selectNode(1, false) // node selection active
	if !tr.selection.active {
		t.Fatalf("fixture: node selection should be active")
	}
	tr.key('i')
	if tr.selection.active {
		t.Fatalf("'i' must clear node selection")
	}
	if tr.vmode != visualCursor {
		t.Fatalf("'i' enters cursor mode")
	}
	// put the cursor on a wordy row and walk it
	tr.key('G')
	tr.key('^')
	c0 := tr.vCursor.col
	tr.key('w')
	c1 := tr.vCursor.col
	tr.key('e')
	c2 := tr.vCursor.col
	tr.key('b')
	c3 := tr.vCursor.col
	if !(c1 > c0 && c2 >= c1 && c3 <= c2) {
		t.Fatalf("w/e/b sequence incoherent: %d %d %d %d", c0, c1, c2, c3)
	}
	tr.key('$')
	if tr.vCursor.col < c2 {
		t.Fatalf("$ should reach at least the last word end")
	}
	// ladder: v -> Esc -> cursor -> Esc -> off -> Esc -> :noh
	tr.key('v')
	tr.key(0x1b)
	if tr.vmode != visualCursor {
		t.Fatalf("Esc from selection drops to cursor mode")
	}
	tr.key(0x1b)
	if tr.vmode != visualOff {
		t.Fatalf("Esc from cursor mode exits")
	}
	tr.findQuery("msg01")
	tr.key('q') // q from cursor mode: straight out
	if tr.vmode != visualOff || !tr.active {
		t.Fatalf("q exits modes without touching the pager lock")
	}
}

// Ghost regression: a row overlay that strands an open background must not
// bleed into other rows' erases (BCE) — every painted row write is SGR-reset
// bracketed.
func TestPaint_ResetBracketsEveryRow(t *testing.T) {
	var sink strings.Builder
	tr := &transcript{out: &sink, w: 20, h: 3, active: true}
	tr.paint([]string{"plain", "\x1b[48;5;240mdangling-bg-open", "after"})
	got := sink.String()
	for _, frag := range []string{"\x1b[1;1H\x1b[0m\x1b[2K", "\x1b[2;1H\x1b[0m\x1b[2K", "\x1b[3;1H\x1b[0m\x1b[2K"} {
		if !strings.Contains(got, frag) {
			t.Fatalf("row write not reset-bracketed (%q missing): %q", frag, got)
		}
	}
	if !strings.Contains(got, "dangling-bg-open\x1b[0m") {
		t.Fatalf("row content must be closed with a reset: %q", got)
	}
}
