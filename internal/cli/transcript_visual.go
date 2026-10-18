package cli

// Visual mode: v/V put a cursor on the rows, the next press anchors a highlight.
// Node selection and the cursor are exclusive. A point is held as (node, row
// within node, column), never a line number: line space is rebuilt every frame and
// a page landing above shifts every absolute line. Chrome rows belong to no node
// and cannot hold the cursor.

import (
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/render"
	"github.com/jack-work/figaro/internal/term"
	"github.com/mattn/go-runewidth"
)

type visualKind uint8

const (
	visualNone visualKind = iota // no highlight: the cursor alone
	visualChar                   // v: column to column
	visualLine                   // V: whole rows
)

type visualPoint struct {
	ref nodeRef
	row int // index among the rows that belong to ref, at this width
	col int // display column; only visualChar reads it
}

type visualSelection struct {
	on     bool
	cursor visualPoint
	kind   visualKind
	anchor visualPoint // the other end, when kind != visualNone
}

func (s visualSelection) active() bool { return s.on }

func (s visualSelection) highlighted() bool { return s.on && s.kind != visualNone }

func (s visualSelection) focus() visualPoint { return s.cursor }

type visualSpan struct {
	kind           visualKind
	loLine, hiLine int
	loCol, hiCol   int
}

func (s visualSpan) active() bool { return s.kind != visualNone }

// Line mode washes the whole row; char mode from loCol on the first line to hiCol
// inclusive on the last.
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
		// The focus is past the row's end: one cell still marks the cursor.
		to = from + 1
	}
	return from, to, true
}

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

// Binary search: entries are in turn order.
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

// An endpoint whose node left the window resolves to the window's edge, so the
// wash never vanishes while the other end is on screen.
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

func (t *transcript) edgeToward(p visualPoint, other int) (int, int) {
	k := t.index.entryAt(other)
	if k >= 0 && p.ref.turn < t.index.entries[k].turn {
		return 0, 0
	}
	return t.index.total - 1, t.w
}

func (t *transcript) visualCursorLine() (int, bool) { return t.visualLineOf(t.visual.cursor) }

// First press: cursor; next: anchor a highlight of that kind; same kind again:
// drop it; other kind: switch.
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

// Starts on the node selection when there is one, else the bottommost node on screen.
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
	for i := bottom - 1; i >= top; i-- {
		p, ok := t.visualPointAt(i, 0)
		if !ok {
			continue
		}
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
	return visualPoint{}, false
}

func (t *transcript) leaveVisual() { t.visual = visualSelection{} }

// Steps over rows that belong to no node; the column is kept and clamped to the
// landing row.
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
	// back toward it.
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

// The column moves in every kind: a line-wise highlight ignores it, but the cursor
// is still somewhere.
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
	// The row's text, not its padding.
	w := runewidth.StringWidth(strings.TrimRight(render.StripEscapes(t.lineText(i)), " "))
	if w == 0 {
		t.visual.cursor.col = 0
	} else if t.visual.cursor.col >= w {
		t.visual.cursor.col = w - 1
	}
}

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

func (t *transcript) visualEnsureVisible(i int) {
	body, _ := t.layout(len(t.footLines()))
	// The sticky header covers rows: the floor is the offset plus what it hides.
	if i >= t.offset+body {
		t.offset = i - body + 1
	}
	// The header is a function of the offset: a scroll up can bring a
	// question's pin into view, so re-read the floor after every move.
	for pass := 0; pass < 3; pass++ {
		head := t.headRows()
		if i >= t.offset+head {
			break
		}
		t.offset = max(i-head, 0)
	}
}

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

// The wash is re-emitted after every reset inside the range; with colour off the
// row is returned untouched.

// restore re-arms the wash after the cell (the cell's close is a full reset);
// washTo bounds it so a cursor on the wash's last cell does not re-open it.
func cursorCell(row string, col int, restore string, washTo int) string {
	return paintColumns(row, col, col+1, term.Cursor(), restore, washTo)
}

func washColumns(row string, from, to int) string {
	return paintColumns(row, from, to, term.SelectWash(), "", 0)
}

func paintColumns(row string, from, to int, body, restore string, restoreUntil int) string {
	const reset = "\x1b[0m"
	if from >= to || body == "" {
		return row
	}
	// under is the row's own rendition since its last reset: closing with a
	// bare reset would leave the rest of a coloured line default.
	var under []string
	closeAt := func(col int) string {
		out := strings.Join(under, "")
		// After washColumns the wash is one of the row's own sequences: do
		// not emit it twice.
		if restore != "" && col < restoreUntil && !strings.HasSuffix(out, restore) {
			out += restore
		}
		return reset + out
	}
	var b strings.Builder
	b.Grow(len(row) + 32)
	col, inside := 0, false
	for i := 0; i < len(row); {
		if row[i] == '\x1b' {
			j := skipANSI(row, i)
			under = foldSGR(under, row[i:j])
			b.WriteString(row[i:j])
			if inside {
				isReset := row[i:j] == reset || row[i:j] == "\x1b[m"
				switch {
				case col < to:
					// Re-arm after every escape while the
					// range continues.
					b.WriteString(body)
				case isReset:
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
		// A rune whose cells intersect the range is painted whole: a wide
		// rune cannot be half painted.
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
			// Nothing of the row reaches the range: pad up to it and
			// paint one cell.
			b.WriteString(reset)
			b.WriteString(strings.Repeat(" ", pad-col))
			b.WriteString(body)
			col = pad
		}
		b.WriteString(strings.Repeat(" ", to-col))
	}
	if inside {
		// Every painted row must finish in the default rendition (compactRow).
		b.WriteString(reset)
		if restore != "" && col < restoreUntil {
			b.WriteString(restore)
		}
	}
	return b.String()
}

// A non-SGR escape is not a rendition; a full reset clears what came before.
func foldSGR(under []string, esc string) []string {
	if len(esc) < 3 || esc[1] != '[' || esc[len(esc)-1] != 'm' {
		return under
	}
	if body := esc[2 : len(esc)-1]; body == "" || body == "0" {
		return under[:0]
	}
	return append(under, esc)
}

// The vim motions: a word is a run of word characters or of other non-blanks, and
// w/b cross rows as vim crosses lines.

func (t *transcript) visualRowPlain(line int) []rune {
	return []rune(strings.TrimRight(render.StripEscapes(t.lineText(line)), " "))
}

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

// vim's: 0 blank, 1 word (letters, digits, _), 2 other.
func wordClass(r rune) int {
	switch {
	case r == ' ' || r == '\t':
		return 0
	case r == '_' || unicode.IsLetter(r) || unicode.IsDigit(r):
		return 1
	}
	return 2
}

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
			if next, ok := t.visualNextRow(line, 1); ok {
				t.visual.cursor, _ = t.visualPointAt(next, 0)
				t.visualEnsureVisible(next)
				t.visualFirstNonBlank()
			}
			return
		}
	case dir > 0 && toEnd:
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
		// b: nothing but blanks behind the cursor hops to the previous row.
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

// A word motion has nothing to land on in a blank row.
func (t *transcript) visualNextRow(line, dir int) (int, bool) {
	for i := line + dir; i >= 0 && i < t.index.total; i += dir {
		if _, ok := t.visualPointAt(i, 0); ok && len(t.visualRowPlain(i)) > 0 {
			return i, true
		}
	}
	return 0, false
}

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
		for {
			prev, ok := t.visualNextRow(i, -1)
			if !ok {
				break
			}
			p, _ := t.visualPointAt(prev, 0)
			if p.ref != cur {
				if i == line {
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

// With a highlight up, landing on a hit extends the wash to it.
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

func (s visualSelection) below(base int) bool {
	if !s.on {
		return true
	}
	return base > 0 && s.cursor.ref.turn < base && (s.kind == visualNone || s.anchor.ref.turn < base)
}

// Enter hands off from the cursor to the node selection; the two are exclusive.
func pagerVisualSelect(t *transcript) {
	ref := t.visual.cursor.ref
	note, onNote := t.commentAtCursor()
	if ref.note > 0 {
		note, onNote = ref, true
	}
	t.leaveVisual()
	if onNote {
		if t.selectRef(note, false) {
			if !t.openComments()[note] {
				t.toggleExpansionOf(note)
			}
			t.ensureSelectionVisible()
		}
		return
	}
	if !ref.valid() {
		return
	}
	t.selectRef(blockOf(ref), false)
	t.ensureSelectionVisible()
}

// An aria id attends; anything else goes to the path hook, which decides whether
// it is a file.
func pagerVisualAttend(t *transcript) {
	text := t.visualTextUnderCursor()
	if text == "" {
		t.note("nothing under the cursor")
		return
	}
	if looksLikeAriaID(text) {
		if t.attendAria == nil {
			t.note("this session cannot attend (no shell to bind)")
			return
		}
		t.leaveVisual()
		t.attendAria(text)
		return
	}
	if t.openPath == nil {
		t.note(text + " is not an aria id")
		return
	}
	t.openPath(text)
}

// n/N from the transcript land the cursor on the hit.
func pagerFindNextVisual(t *transcript) { t.findRepeatVisual(1) }
func pagerFindPrevVisual(t *transcript) { t.findRepeatVisual(-1) }

func (t *transcript) findRepeatVisual(delta int) {
	if t.matchQuery == "" {
		return
	}
	if !t.visual.on {
		t.enterVisual()
		// The search walks from the offset, where the reader was reading.
		if p, ok := t.visualPointAt(t.offset, 0); ok {
			t.visual.cursor = p
		}
	}
	t.findRepeat(delta)
}

func looksLikeAriaID(s string) bool {
	if rpc.ValidateAriaRef(s) != nil {
		return false
	}
	_, id := rpc.SplitNode(s)
	if strings.HasPrefix(id, "@") {
		return true
	}
	if len(id) < rpc.ShortIDLen {
		return false
	}
	for _, r := range id {
		if !((r >= '0' && r <= '9') || (r >= 'a' && r <= 'f')) {
			return false
		}
	}
	return true
}

func (t *transcript) visualTextUnderCursor() string {
	if t.visual.highlighted() {
		if text := strings.TrimSpace(t.visualText()); text != "" {
			return text
		}
	}
	line, ok := t.visualCursorLine()
	if !ok {
		return ""
	}
	row := t.visualRowPlain(line)
	i := runeOfCol(row, t.visual.cursor.col)
	if i < 0 || i >= len(row) || unicode.IsSpace(row[i]) {
		return ""
	}
	lo, hi := i, i
	for lo > 0 && !unicode.IsSpace(row[lo-1]) {
		lo--
	}
	for hi+1 < len(row) && !unicode.IsSpace(row[hi+1]) {
		hi++
	}
	return strings.Trim(string(row[lo:hi+1]), "`'\"()[]{}<>,;:")
}
