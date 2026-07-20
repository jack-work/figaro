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
	if tr.vmode != visualOff {
		t.Fatalf("Esc must exit visual mode")
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
	row0 := tr.pointToRow(tr.vCursor)
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
	if tr.pointToRow(tr.vAnchor) != row0 || tr.pointToRow(tr.vCursor) == row0 {
		t.Fatalf("n must move the cursor endpoint only (anchor %d cursor %d)", tr.pointToRow(tr.vAnchor), tr.pointToRow(tr.vCursor))
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
