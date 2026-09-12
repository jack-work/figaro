package cli

// VISUAL MODE: a block cursor over the transcript's rows, and a highlight
// anchored from it.
//
// `v` (or `V`) puts a CURSOR on the rows: one cell, moved by hjkl and the
// arrow cluster, the viewport following it. Pressing `v` again anchors a
// character-wise highlight at the cursor; from then on moving the cursor
// extends the wash between anchor and cursor. `V` anchors line-wise. The
// same key again turns the highlight off and leaves the cursor; Esc leaves
// the mode entirely. This is vim's shape with one difference: vim always
// has a cursor, and the pager did not, so the first press supplies one.
//
// Node selection (^N/^P) addresses whole blocks and is what Enter and the
// copier act on; the visual highlight addresses what the reader can see
// between two points, and is what `y` yanks and what `:<,>` hands to a
// command. The two cannot both be active: entering one drops the other.
//
// THE POINTS ARE NOT LINE NUMBERS. Line space is rebuilt every frame and a
// page landing above the viewport shifts every absolute line, so a point
// held as a line would drift under the reader's hand. It is held as (node,
// row within that node, column), which survives a rebuild for the same
// reason a node selection does, and is turned back into a line only when a
// frame is painted or a motion is asked for. Chrome rows (a voice header, a
// rule, a blank) belong to no node and so cannot hold the cursor: the
// motions step over them, which is also the honest answer for a highlight,
// since chrome has no source to quote.

import (
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/jack-work/figaro/internal/render"
	"github.com/jack-work/figaro/internal/term"
	"github.com/mattn/go-runewidth"
)

type visualKind uint8

const (
	visualNone visualKind = iota // no highlight: the cursor alone
	visualChar                   // v: from a column on one row to a column on another
	visualLine                   // V: whole rows
)

// visualPoint is the cursor, or one end of a highlight.
type visualPoint struct {
	ref nodeRef
	row int // index among the rows that belong to ref, in this width
	col int // display column; only visualChar reads it
}

type visualSelection struct {
	on     bool        // the mode is up and the cursor is drawn
	cursor visualPoint // where the reader is
	kind   visualKind  // visualNone until v/V anchors a highlight
	anchor visualPoint // the other end of the highlight, when kind != visualNone
}

// active reports whether the mode is up (a cursor is showing).
func (s visualSelection) active() bool { return s.on }

// highlighted reports whether a range is marked, which is what y, Y and
// `:<,>` spend.
func (s visualSelection) highlighted() bool { return s.on && s.kind != visualNone }

// focus is the moving end of the highlight: the cursor.
func (s visualSelection) focus() visualPoint { return s.cursor }

// visualSpan is the per-frame form: the ordered endpoints as absolute lines,
// so a row can answer "am I inside, and which columns" by comparison.
type visualSpan struct {
	kind           visualKind
	loLine, hiLine int
	loCol, hiCol   int
}

func (s visualSpan) active() bool { return s.kind != visualNone }

// cols reports the column range [from, to) a row at absolute line i paints,
// and whether it paints at all. Line mode washes the whole row; char mode
// washes from loCol on the first line to hiCol inclusive on the last.
func (s visualSpan) cols(i, width int) (from, to int, ok bool) {
	if !s.active() || i < s.loLine || i > s.hiLine {
		return 0, 0, false
	}
	if s.kind == visualLine {
		return 0, width, true
	}
	from, to = 0, width
	if i == s.loLine {
		from = s.loCol
	}
	if i == s.hiLine {
		to = s.hiCol + 1
	}
	if to > width {
		to = width
	}
	if from >= to {
		// The focus stands on a column past the row's end (a shorter row
		// under a wider one). One cell still marks where the cursor is.
		to = from + 1
	}
	return from, to, true
}

// ---------------------------------------------------------------------------
// Between points and lines.

// visualLineOf resolves a point to its absolute line in the current index, or
// false when the node has left the retained window.
func (t *transcript) visualLineOf(p visualPoint) (int, bool) {
	for k := t.entriesOfTurn(p.ref.turn); k >= 0 && k < len(t.index.entries); k++ {
		e := &t.index.entries[k]
		if e.turn != p.ref.turn {
			break
		}
		if e.isGap() {
			continue
		}
		n := 0
		for i := range e.rows {
			if e.rows[i].ref != p.ref {
				continue
			}
			if n == p.row {
				return entryRowsStart(e) + i, true
			}
			n++
		}
	}
	return 0, false
}

// entriesOfTurn is the index of the first entry of turn, or -1. Entries are
// in turn order, so this is a binary search rather than a walk: a highlight
// over a wide range resolves its ends without visiting the nodes between.
func (t *transcript) entriesOfTurn(turn int) int {
	es := t.index.entries
	lo, hi := 0, len(es)
	for lo < hi {
		mid := int(uint(lo+hi) >> 1)
		if es[mid].turn < turn {
			lo = mid + 1
		} else {
			hi = mid
		}
	}
	if lo < len(es) && es[lo].turn == turn {
		return lo
	}
	return -1
}

// visualPointAt is the inverse: the point standing on absolute line i, or
// false when that line belongs to no node (a separator, a gap, chrome).
func (t *transcript) visualPointAt(i, col int) (visualPoint, bool) {
	k := t.index.entryAt(i)
	if k < 0 {
		return visualPoint{}, false
	}
	e := &t.index.entries[k]
	rel := i - entryRowsStart(e)
	if rel < 0 || e.isGap() || rel >= len(e.rows) {
		return visualPoint{}, false
	}
	ref := e.rows[rel].ref
	if !ref.valid() {
		return visualPoint{}, false
	}
	n := 0
	for j := 0; j < rel; j++ {
		if e.rows[j].ref == ref {
			n++
		}
	}
	return visualPoint{ref: ref, row: n, col: col}, true
}

// visualSpan is the frame's view of the selection. An endpoint whose node has
// left the window resolves to the window's edge on that side, so the wash
// never vanishes while the other end is still on screen.
func (t *transcript) visualSpan() visualSpan {
	if !t.visual.highlighted() {
		return visualSpan{}
	}
	a, aok := t.visualLineOf(t.visual.anchor)
	f, fok := t.visualLineOf(t.visual.cursor)
	if !aok && !fok {
		return visualSpan{}
	}
	ac, fc := t.visual.anchor.col, t.visual.cursor.col
	if !aok {
		a, ac = t.edgeToward(t.visual.anchor, f)
	}
	if !fok {
		f, fc = t.edgeToward(t.visual.cursor, a)
	}
	s := visualSpan{kind: t.visual.kind, loLine: a, hiLine: f, loCol: ac, hiCol: fc}
	if f < a || (f == a && fc < ac) {
		s.loLine, s.hiLine, s.loCol, s.hiCol = f, a, fc, ac
	}
	return s
}

// edgeToward picks the window edge an evicted endpoint stands for: before the
// surviving line when its turn is earlier, after it otherwise.
func (t *transcript) edgeToward(p visualPoint, other int) (int, int) {
	k := t.index.entryAt(other)
	if k >= 0 && p.ref.turn < t.index.entries[k].turn {
		return 0, 0
	}
	return t.index.total - 1, t.w
}

// ---------------------------------------------------------------------------
// Entering, moving, leaving.

// visualCursorLine is the cursor's absolute line, or false when its node has
// left the window.
func (t *transcript) visualCursorLine() (int, bool) { return t.visualLineOf(t.visual.cursor) }

// pressVisual is v or V. The first press puts the cursor up; the next anchors
// a highlight of that kind at the cursor; the same kind again drops the
// highlight and keeps the cursor; the other kind switches the highlight.
func (t *transcript) pressVisual(kind visualKind) {
	if !t.visual.on {
		t.enterVisual()
		return
	}
	switch t.visual.kind {
	case visualNone:
		t.visual.kind, t.visual.anchor = kind, t.visual.cursor
	case kind:
		t.visual.kind = visualNone
	default:
		t.visual.kind = kind
	}
}

// enterVisual puts the cursor up. It starts on the first row of the node
// selection when there is one (the reader has already pointed at something),
// else on the top row of the bottommost node on screen: the newest thing in
// view, which is what a reader who just watched a reply land wants to mark.
func (t *transcript) enterVisual() {
	seed, ok := t.visualSeed()
	if t.selection.active {
		t.clearSelection()
	}
	t.closePanels()
	t.stopFollowing()
	t.buildIndex()
	if !ok {
		seed, ok = t.visualSeed()
	}
	if !ok {
		return
	}
	t.visual = visualSelection{on: true, cursor: seed}
}

// visualSeed is where the cursor starts; see enterVisual.
func (t *transcript) visualSeed() (visualPoint, bool) {
	t.buildIndex()
	if t.selection.active {
		if span, ok := t.nodeSpanOf(t.selection.focus.nodeRef); ok {
			if p, ok := t.visualPointAt(span.first, 0); ok {
				return p, true
			}
		}
	}
	top, bottom := t.viewportLines()
	// The bottommost node that starts on screen, then its first row.
	var seed visualPoint
	found := false
	for i := bottom - 1; i >= top; i-- {
		p, ok := t.visualPointAt(i, 0)
		if !ok {
			continue
		}
		if !found || p.ref != seed.ref {
			// Walk up to this node's first row inside the viewport.
			first := i
			for j := i - 1; j >= top; j-- {
				q, ok := t.visualPointAt(j, 0)
				if !ok || q.ref != p.ref {
					break
				}
				first = j
			}
			p, _ = t.visualPointAt(first, 0)
			return p, true
		}
	}
	return seed, found
}

func (t *transcript) leaveVisual() { t.visual = visualSelection{} }

// visualMove shifts the cursor by delta lines, stepping over rows that belong
// to no node, and keeps it on screen. The column is kept across lines and
// clamped to the row it lands on.
func (t *transcript) visualMove(delta int) {
	if !t.visual.active() || delta == 0 {
		return
	}
	t.buildIndex()
	cur, ok := t.visualLineOf(t.visual.cursor)
	if !ok {
		return
	}
	step := 1
	if delta < 0 {
		step = -1
	}
	target := cur + delta
	if target < 0 {
		target = 0
	}
	if target >= t.index.total {
		target = t.index.total - 1
	}
	// Land on a node row: past the target in the direction of travel, then
	// back toward it, then give up and stay.
	land := -1
	for i := target; i >= 0 && i < t.index.total; i += step {
		if _, ok := t.visualPointAt(i, 0); ok {
			land = i
			break
		}
	}
	if land < 0 {
		for i := target; i >= 0 && i < t.index.total; i -= step {
			if _, ok := t.visualPointAt(i, 0); ok {
				land = i
				break
			}
		}
	}
	if land < 0 {
		return
	}
	p, _ := t.visualPointAt(land, t.visual.cursor.col)
	t.visual.cursor = p
	t.visualClampCol()
	t.visualEnsureVisible(land)
}

// visualMoveCol shifts the cursor's column, clamped to the row's visible
// width. The column is a cursor property, so it moves in every kind: a
// line-wise highlight ignores it, and the cursor still has to be somewhere.
func (t *transcript) visualMoveCol(delta int) {
	if !t.visual.active() {
		return
	}
	t.visual.cursor.col += delta
	if t.visual.cursor.col < 0 {
		t.visual.cursor.col = 0
	}
	t.visualClampCol()
}

func (t *transcript) visualClampCol() {
	i, ok := t.visualLineOf(t.visual.cursor)
	if !ok {
		return
	}
	// The row's text, not its padding: a cursor parked in the blank tail
	// of a short row points at nothing.
	w := runewidth.StringWidth(strings.TrimRight(render.StripEscapes(t.lineText(i)), " "))
	if w == 0 {
		t.visual.cursor.col = 0
	} else if t.visual.cursor.col >= w {
		t.visual.cursor.col = w - 1
	}
}

// visualJump moves the cursor to the first or last node row of the window.
func (t *transcript) visualJump(toEnd bool) {
	if !t.visual.active() {
		return
	}
	t.buildIndex()
	if toEnd {
		t.visualMove(t.index.total)
	} else {
		t.visualMove(-t.index.total)
	}
}

// visualEnsureVisible scrolls the viewport the least it must so that line i
// is on screen.
func (t *transcript) visualEnsureVisible(i int) {
	body, _ := t.layout(len(t.footLines()))
	if i < t.offset {
		t.offset = i
	} else if i >= t.offset+body {
		t.offset = i - body + 1
	}
}

// lineText is the undecorated row at absolute line i.
func (t *transcript) lineText(i int) string {
	k := t.index.entryAt(i)
	if k < 0 {
		return ""
	}
	e := &t.index.entries[k]
	rel := i - entryRowsStart(e)
	if rel < 0 || e.isGap() || rel >= len(e.rows) {
		return ""
	}
	return e.rows[rel].text
}

// ---------------------------------------------------------------------------
// What the selection holds.

// visualText is the selected text as the reader sees it: rows joined by
// newlines, columns narrowed in char mode, escapes stripped.
func (t *transcript) visualText() string {
	s := t.visualSpan()
	if !s.active() {
		return ""
	}
	var b strings.Builder
	for i := s.loLine; i <= s.hiLine; i++ {
		plain := render.StripEscapes(t.lineText(i))
		if from, to, ok := s.cols(i, t.w); ok && s.kind == visualChar {
			plain = sliceColumns(plain, from, to)
		}
		if i > s.loLine {
			b.WriteByte('\n')
		}
		b.WriteString(strings.TrimRight(plain, " "))
	}
	return b.String()
}

// sliceColumns cuts plain (no escapes) to display columns [from, to).
func sliceColumns(plain string, from, to int) string {
	col := 0
	start, end := -1, len(plain)
	for i, r := range plain {
		w := runewidth.RuneWidth(r)
		if start < 0 && col+w > from {
			start = i
		}
		if col >= to {
			end = i
			break
		}
		col += w
	}
	if start < 0 {
		return ""
	}
	return plain[start:end]
}

// ---------------------------------------------------------------------------
// Painting.

// The wash is re-emitted after every reset inside the range, as
// decorateNodeRow does for a whole row, so inline styling cannot end it
// early; a row shorter than `to` is padded so the paint reaches the column
// the reader's eye expects. The colours are the palette's (term.SelectWash,
// term.Cursor); with colour off the row is returned untouched.

// cursorCell paints the block cursor over display column col. restore is
// the body to re-arm after the cell when the cursor stands inside a wash,
// since the cell's own close is a full reset; washTo bounds it, so a cursor
// on the wash's last cell does not re-open the wash past its end.
func cursorCell(row string, col int, restore string, washTo int) string {
	return paintColumns(row, col, col+1, term.Cursor(), restore, washTo)
}

// washColumns paints the highlight; see paintColumns.
func washColumns(row string, from, to int) string {
	return paintColumns(row, from, to, term.SelectWash(), "", 0)
}

// paintColumns is the one column painter: body is a bare SGR body applied
// over [from, to), re-armed after every reset the row carries inside it,
// and closed with a reset followed by restore while the column is still
// short of restoreUntil.
func paintColumns(row string, from, to int, body, restore string, restoreUntil int) string {
	const reset = "\x1b[0m"
	if from >= to || body == "" {
		return row
	}
	closeAt := func(col int) string {
		if restore != "" && col < restoreUntil {
			return reset + restore
		}
		return reset
	}
	var b strings.Builder
	b.Grow(len(row) + 32)
	col, inside := 0, false
	for i := 0; i < len(row); {
		if row[i] == '\x1b' {
			j := skipANSI(row, i)
			b.WriteString(row[i:j])
			if inside {
				isReset := row[i:j] == reset || row[i:j] == "\x1b[m"
				switch {
				case col < to:
					// The row's own styling inside the range (a reset, or a
					// heading's background) must not end the paint: re-arm
					// after every escape while the range continues.
					b.WriteString(body)
				case isReset:
					// At the edge the row's reset is the close.
					if restore != "" && col < restoreUntil {
						b.WriteString(restore)
					}
					inside = false
				}
			}
			i = j
			continue
		}
		r, size := utf8.DecodeRuneInString(row[i:])
		w := runewidth.RuneWidth(r)
		// A rune whose cells INTERSECT the range is painted whole: a wide
		// rune cannot be half painted, and a cursor standing on its second
		// cell is standing on it.
		if !inside && col+w > from && col < to {
			b.WriteString(body)
			inside = true
		}
		if inside && col >= to {
			b.WriteString(closeAt(col))
			inside = false
		}
		b.WriteString(row[i : i+size])
		col += w
		i += size
	}
	if col < to {
		if !inside {
			b.WriteString(body)
			inside = true
		}
		if pad := from; pad > col {
			// Nothing of the row reaches the range: the reader still sees the
			// cursor cell, so pad up to it and paint one cell.
			b.WriteString(reset)
			b.WriteString(strings.Repeat(" ", pad-col))
			b.WriteString(body)
			col = pad
		}
		b.WriteString(strings.Repeat(" ", to-col))
	}
	if inside {
		b.WriteString(closeAt(col))
	}
	return b.String()
}

// ---------------------------------------------------------------------------
// The vim motions. Each moves the cursor; with a highlight anchored, moving
// the cursor is what extends it. Word motions treat the row's visible text
// as vim does (a word is a run of word characters or a run of other
// non-blanks) and cross rows the way vim crosses lines: w at the end of a
// row lands on the next row's first word, b at the start of a row on the
// previous row's last.

// visualRowPlain is the cursor row's text without escapes, as runes and
// display columns.
func (t *transcript) visualRowPlain(line int) []rune {
	return []rune(strings.TrimRight(render.StripEscapes(t.lineText(line)), " "))
}

// colOfRune and runeOfCol translate between rune indices and display columns
// on one row; wide runes take two columns.
func colOfRune(row []rune, i int) int {
	col := 0
	for k := 0; k < i && k < len(row); k++ {
		col += runeCells(row[k])
	}
	return col
}

func runeOfCol(row []rune, col int) int {
	c := 0
	for i, r := range row {
		w := runeCells(r)
		if c+w > col {
			return i
		}
		c += w
	}
	return len(row)
}

// wordClass is vim's: 0 blank, 1 word (letters, digits, _), 2 other.
func wordClass(r rune) int {
	switch {
	case r == ' ' || r == '\t':
		return 0
	case r == '_' || unicode.IsLetter(r) || unicode.IsDigit(r):
		return 1
	}
	return 2
}

// visualWord is w (dir>0) or b (dir<0); toEnd makes w into e.
func (t *transcript) visualWord(dir int, toEnd bool) {
	if !t.visual.active() {
		return
	}
	t.buildIndex()
	line, ok := t.visualCursorLine()
	if !ok {
		return
	}
	row := t.visualRowPlain(line)
	i := runeOfCol(row, t.visual.cursor.col)
	if i >= len(row) && len(row) > 0 {
		i = len(row) - 1
	}
	switch {
	case dir > 0 && !toEnd:
		// w: past the current run, then past blanks.
		if i < len(row) {
			c := wordClass(row[i])
			for i < len(row) && wordClass(row[i]) == c {
				i++
			}
			for i < len(row) && wordClass(row[i]) == 0 {
				i++
			}
		}
		if i >= len(row) {
			// Next row's first non-blank.
			if next, ok := t.visualNextRow(line, 1); ok {
				t.visual.cursor, _ = t.visualPointAt(next, 0)
				t.visualEnsureVisible(next)
				t.visualFirstNonBlank()
			}
			return
		}
	case dir > 0 && toEnd:
		// e: to the end of the current (or next) run.
		i++
		for i < len(row) && wordClass(row[i]) == 0 {
			i++
		}
		if i >= len(row) {
			if next, ok := t.visualNextRow(line, 1); ok {
				t.visual.cursor, _ = t.visualPointAt(next, 0)
				t.visualEnsureVisible(next)
				t.visual.cursor.col = 0
				t.visualWord(1, true)
			}
			return
		}
		c := wordClass(row[i])
		for i+1 < len(row) && wordClass(row[i+1]) == c {
			i++
		}
	default:
		// b: back over blanks, then to the start of the run. Nothing but
		// blanks behind the cursor means the row's start, which hops.
		j := i - 1
		for j > 0 && wordClass(row[j]) == 0 {
			j--
		}
		if i <= 0 || j < 0 || wordClass(row[j]) == 0 {
			if prev, ok := t.visualNextRow(line, -1); ok {
				p, _ := t.visualPointAt(prev, 0)
				t.visual.cursor = p
				t.visualEnsureVisible(prev)
				prow := t.visualRowPlain(prev)
				t.visual.cursor.col = colOfRune(prow, len(prow))
				t.visualWord(-1, false)
			}
			return
		}
		i--
		for i > 0 && wordClass(row[i]) == 0 {
			i--
		}
		c := wordClass(row[i])
		for i > 0 && wordClass(row[i-1]) == c {
			i--
		}
	}
	t.visual.cursor.col = colOfRune(row, i)
	t.visualClampCol()
}

// visualNextRow is the nearest node row past line in direction dir that
// carries text: a word motion has nothing to land on in a blank row.
func (t *transcript) visualNextRow(line, dir int) (int, bool) {
	for i := line + dir; i >= 0 && i < t.index.total; i += dir {
		if _, ok := t.visualPointAt(i, 0); ok && len(t.visualRowPlain(i)) > 0 {
			return i, true
		}
	}
	return 0, false
}

// visualRowStart is 0; visualFirstNonBlank is ^; visualRowEnd is $.
func (t *transcript) visualRowStart() {
	if t.visual.active() {
		t.visual.cursor.col = 0
	}
}

func (t *transcript) visualFirstNonBlank() {
	if !t.visual.active() {
		return
	}
	line, ok := t.visualCursorLine()
	if !ok {
		return
	}
	row := t.visualRowPlain(line)
	i := 0
	for i < len(row) && wordClass(row[i]) == 0 {
		i++
	}
	if i >= len(row) {
		i = 0
	}
	t.visual.cursor.col = colOfRune(row, i)
}

func (t *transcript) visualRowEnd() {
	if !t.visual.active() {
		return
	}
	line, ok := t.visualCursorLine()
	if !ok {
		return
	}
	row := t.visualRowPlain(line)
	if len(row) == 0 {
		t.visual.cursor.col = 0
		return
	}
	t.visual.cursor.col = colOfRune(row, len(row)-1)
}

// visualScreen is H (where<0), M (0) or L (>0): the top, middle or bottom
// node row of the viewport.
func (t *transcript) visualScreen(where int) {
	if !t.visual.active() {
		return
	}
	t.buildIndex()
	top, bottom := t.viewportLines()
	if bottom <= top {
		return
	}
	target := top
	switch {
	case where > 0:
		target = bottom - 1
	case where == 0:
		target = top + (bottom-top)/2
	}
	dir := 1
	if where > 0 {
		dir = -1
	}
	for i := target; i >= top && i < bottom; i += dir {
		if p, ok := t.visualPointAt(i, t.visual.cursor.col); ok {
			t.visual.cursor = p
			t.visualClampCol()
			return
		}
	}
}

// visualParagraph is } (dir>0) or {: the first row of the next or previous
// node, which is the paragraph a transcript has.
func (t *transcript) visualParagraph(dir int) {
	if !t.visual.active() {
		return
	}
	t.buildIndex()
	line, ok := t.visualCursorLine()
	if !ok {
		return
	}
	cur := t.visual.cursor.ref
	i := line
	if dir < 0 {
		// To this node's first row; if already there, the previous node's.
		for {
			prev, ok := t.visualNextRow(i, -1)
			if !ok {
				break
			}
			p, _ := t.visualPointAt(prev, 0)
			if p.ref != cur {
				if i == line {
					// Already on the first row: go to the previous node's first.
					cur = p.ref
					i = prev
					continue
				}
				break
			}
			i = prev
		}
	} else {
		for {
			next, ok := t.visualNextRow(i, 1)
			if !ok {
				break
			}
			i = next
			if p, _ := t.visualPointAt(i, 0); p.ref != cur {
				break
			}
		}
	}
	if i == line {
		return
	}
	p, _ := t.visualPointAt(i, t.visual.cursor.col)
	t.visual.cursor = p
	t.visualClampCol()
	t.visualEnsureVisible(i)
}

// visualLandSearch puts the cursor on a search hit: line, and the column the
// match begins at. A search in visual mode moves the cursor, and with a
// highlight up that extends the wash to the match.
func (t *transcript) visualLandSearch(line int, q string) {
	if !t.visual.active() {
		return
	}
	p, ok := t.visualPointAt(line, 0)
	if !ok {
		return
	}
	row := t.visualRowPlain(line)
	col := 0
	if k := strings.Index(string(row), q); k >= 0 {
		col = colOfRune(row, len([]rune(string(row)[:k])))
	}
	p.col = col
	t.visual.cursor = p
	t.visualEnsureVisible(line)
}
