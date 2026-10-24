package cli

import (
	"fmt"

	ldrender "github.com/jack-work/figaro/internal/livelog/render"
	"github.com/jack-work/figaro/internal/term"
)

const (
	headMinRows    = 4
	headOpenGlyph  = "∧"
	headShutGlyph  = "∨"
	headPaneShare  = 4
	headChromeRows = 2
)

type questionHead struct {
	open bool
	full bool
	turn int
	top  int
	room int
}

func (t *transcript) headFocused() bool { return t.head.open && t.focused == focusHead }

func (t *transcript) fullHead() bool { return t.headFocused() && t.head.full }

func (t *transcript) headQuestion() (int, stickyQuestion) {
	turn, _ := t.stickyTurn()
	q := t.stickyBlockOf(turn)
	if turn != t.head.turn {
		t.head.turn, t.head.top = turn, 0
	}
	return turn, q
}

func headTextRows(q stickyQuestion) []transcriptRow {
	if len(q.rows) == 0 || q.textHigh <= 1 {
		return nil
	}
	return q.rows[1:q.textHigh]
}

func (t *transcript) headRoom() int {
	if t.fullHead() {
		return max(t.h-t.barRows()-1-headChromeRows, 1)
	}
	return max(t.h/headPaneShare, headMinRows) - headChromeRows
}

func (t *transcript) headHeight() int {
	if !t.head.open {
		return 0
	}
	_, q := t.headQuestion()
	if q.empty() {
		return 0
	}
	return min(len(headTextRows(q)), t.headRoom()) + headChromeRows
}

func (t *transcript) headPush() int {
	if !t.head.open || t.headFocused() {
		return 0
	}
	return t.headHeight()
}

func (t *transcript) headLines(hl string, sel selectionSpan) []string {
	if !t.head.open {
		return t.stickyLines(hl, sel)
	}
	turn, q := t.headQuestion()
	if q.empty() {
		return nil
	}
	text := headTextRows(q)
	room := min(len(text), t.headRoom())
	t.head.room = room
	t.head.top = clampInt(t.head.top, 0, len(text)-room)
	out := make([]string, 0, room+headChromeRows)
	heading := t.rowLine(q.rows[0], hl, sel, -1)
	glyph := headOpenGlyph
	if !t.headFocused() {
		glyph = term.Dim(headOpenGlyph)
	}
	heading = ldrender.OverlayColumn(heading, 0, glyph)
	heading = ldrender.OverlayRight(heading, term.Dim(coordLabel(turn, inquiryNode, 0, t.coordFormat())+" "), t.w-ldrender.GutterCols)
	out = append(out, ldrender.OverlayGutter(heading, q.gutter, t.w))
	for _, r := range text[t.head.top : t.head.top+room] {
		r.mark = ""
		out = append(out, t.rowLine(r, hl, sel, -1))
	}
	rule := t.headRule()
	if room < len(text) {
		pos := fmt.Sprintf(" %d–%d/%d ", t.head.top+1, t.head.top+room, len(text))
		rule = ldrender.OverlayRight(rule, term.Dim(pos), t.w)
	}
	return append(out, rule)
}

func (t *transcript) headCovers() int {
	if t.head.open && !t.headFocused() {
		return 0
	}
	return len(t.headLines("", selectionSpan{}))
}

func pagerExpandHead(t *transcript) {
	if t.headFocused() {
		t.collapseHead()
		return
	}
	if !t.head.open {
		_, q := t.headQuestion()
		if q.empty() {
			t.note("no question here to expand")
			return
		}
		t.head = questionHead{open: true, turn: t.head.turn}
	}
	t.focused = focusHead
}

func (t *transcript) collapseHead() {
	t.head = questionHead{}
	if t.focused == focusHead {
		t.focused = focusPit
	}
}

func (t *transcript) headScroll(d int) {
	_, q := t.headQuestion()
	t.head.top = clampInt(t.head.top+d, 0, max(len(headTextRows(q))-t.headRoom(), 0))
}

func (t *transcript) headPage(d int) { t.headScroll(d * max(t.headRoom()/2, 1)) }
