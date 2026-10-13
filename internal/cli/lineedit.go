package cli

import (
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/mattn/go-runewidth"
)

type lineEditor struct {
	runes  []rune
	cursor int // rune index in [0, len(runes)]

	// A terminal may split a multi-byte rune across reads.
	pending []byte

	// hindex == len(history) means not browsing; stash holds the line being typed.
	history []string
	hindex  int
	stash   string

	// origin is the line as fetched: what M-r reverts to.
	origin string

	// yankAt/yankLen mark the last yank, which yank-pop replaces.
	kills   []string
	killIdx int
	yankAt  int
	yankLen int

	undo []lineSnapshot

	// Consecutive kills accumulate, M-y only follows a yank, and a run of typing is
	// one undo step: all three are defined against lastOp.
	lastOp lineOp

	// yankArg is how far back the last M-. reached.
	yankArg int

	search *histSearch

	lastArgAt  int
	lastArgLen int
}

type lineOp uint8

const (
	opOther lineOp = iota
	opInsert
	opKill
	opYank
	opYankArg
)

type lineSnapshot struct {
	runes  []rune
	cursor int
}

const (
	lineEditorHistoryMax = 200
	lineEditorKillRing   = 10
	lineEditorUndoMax    = 100
)

func (e *lineEditor) reset() {
	e.runes = e.runes[:0]
	e.cursor = 0
	e.pending = e.pending[:0]
	e.hindex = len(e.history)
	e.stash = ""
	e.origin = ""
	e.undo = e.undo[:0]
	e.lastOp = opOther
	e.yankArg = 0
	e.search = nil
	// The kill ring survives the box closing.
}

func (e *lineEditor) String() string { return string(e.runes) }
func (e *lineEditor) empty() bool    { return len(e.runes) == 0 }

func (e *lineEditor) set(s string) {
	e.runes = append(e.runes[:0], []rune(s)...)
	e.cursor = len(e.runes)
	e.pending = e.pending[:0]
}

// A run of typing collapses into one undo step.
func (e *lineEditor) snapshot() {
	if len(e.undo) == lineEditorUndoMax {
		copy(e.undo, e.undo[1:])
		e.undo = e.undo[:len(e.undo)-1]
	}
	e.undo = append(e.undo, lineSnapshot{
		runes:  append([]rune(nil), e.runes...),
		cursor: e.cursor,
	})
}

// mark snapshots unless this continues a run of the same collapsible op, then sets
// lastOp.
func (e *lineEditor) mark(op lineOp) {
	if !(op == opInsert && e.lastOp == opInsert) {
		e.snapshot()
	}
	e.lastOp = op
}

func (e *lineEditor) undoOne() bool {
	if len(e.undo) == 0 {
		return false
	}
	s := e.undo[len(e.undo)-1]
	e.undo = e.undo[:len(e.undo)-1]
	e.runes = append(e.runes[:0], s.runes...)
	e.cursor = min(s.cursor, len(e.runes))
	e.lastOp = opOther
	return true
}

func (e *lineEditor) revert() {
	e.mark(opOther)
	e.set(e.origin)
	e.lastOp = opOther
}

// insertByte returns false for a byte that is not text.
func (e *lineEditor) insertByte(b byte) bool {
	if len(e.pending) > 0 || b >= utf8.RuneSelf {
		e.pending = append(e.pending, b)
		if !utf8.FullRune(e.pending) {
			if len(e.pending) >= utf8.UTFMax {
				e.pending = e.pending[:0] // malformed: drop rather than grow
			}
			return true // consumed, waiting for the rest
		}
		r, _ := utf8.DecodeRune(e.pending)
		e.pending = e.pending[:0]
		if r == utf8.RuneError {
			return true
		}
		e.insertRune(r)
		return true
	}
	if b < 0x20 || b == 0x7f {
		return false
	}
	e.insertRune(rune(b))
	return true
}

func (e *lineEditor) insertRune(r rune) {
	e.mark(opInsert)
	e.runes = append(e.runes, 0)
	copy(e.runes[e.cursor+1:], e.runes[e.cursor:])
	e.runes[e.cursor] = r
	e.cursor++
}

// Control runes other than newline are dropped: a stray escape would paint as geometry.
func (e *lineEditor) insertPasted(s string, lines bool) {
	// CRLF is one line break.
	s = strings.ReplaceAll(s, "\r\n", "\n")
	for _, r := range s {
		switch {
		case r == '\r' || r == '\n':
			if lines {
				e.insertRune('\n')
				continue
			}
			e.insertRune(' ')
		case r == '\t':
			e.insertRune(' ')
		case r < 0x20 || r == 0x7f:
		default:
			e.insertRune(r)
		}
	}
}

func (e *lineEditor) insert(s string) {
	for _, r := range s {
		e.insertRune(r)
	}
}

// A motion ends a run of typing and never snapshots.

func (e *lineEditor) left() {
	e.lastOp = opOther
	if e.cursor > 0 {
		e.cursor--
	}
}

func (e *lineEditor) right() {
	e.lastOp = opOther
	if e.cursor < len(e.runes) {
		e.cursor++
	}
}

// home/end are the ends of the cursor's line, not the buffer.
func (e *lineEditor) home() { e.lastOp = opOther; e.cursor = e.lineStart(e.cursor) }
func (e *lineEditor) end()  { e.lastOp = opOther; e.cursor = e.lineEnd(e.cursor) }

func (e *lineEditor) multiline() bool {
	for _, r := range e.runes {
		if r == '\n' {
			return true
		}
	}
	return false
}

func (e *lineEditor) lineStart(at int) int {
	for i := min(at, len(e.runes)) - 1; i >= 0; i-- {
		if e.runes[i] == '\n' {
			return i + 1
		}
	}
	return 0
}

func (e *lineEditor) lineEnd(at int) int {
	for i := max(at, 0); i < len(e.runes); i++ {
		if e.runes[i] == '\n' {
			return i
		}
	}
	return len(e.runes)
}

func (e *lineEditor) insertNewline() { e.insertRune('\n') }

// The cursor keeps its column and does not wrap.
func (e *lineEditor) lineUp()   { e.lineStep(-1) }
func (e *lineEditor) lineDown() { e.lineStep(1) }

func (e *lineEditor) lineStep(dir int) {
	e.lastOp = opOther
	start := e.lineStart(e.cursor)
	col := e.cursor - start
	if dir < 0 {
		if start == 0 {
			e.cursor = 0
			return
		}
		prev := e.lineStart(start - 1)
		e.cursor = min(prev+col, start-1)
		return
	}
	end := e.lineEnd(e.cursor)
	if end >= len(e.runes) {
		e.cursor = len(e.runes)
		return
	}
	next := end + 1
	e.cursor = min(next+col, e.lineEnd(next))
}

func (e *lineEditor) backspace() {
	if e.cursor == 0 {
		return
	}
	e.mark(opOther)
	e.runes = append(e.runes[:e.cursor-1], e.runes[e.cursor:]...)
	e.cursor--
}

func (e *lineEditor) deleteForward() {
	if e.cursor >= len(e.runes) {
		return
	}
	e.mark(opOther)
	e.runes = append(e.runes[:e.cursor], e.runes[e.cursor+1:]...)
}

// Every kill goes through kill(), which owns the ring.

// backward: an accumulating kill grows the top entry at the front, so ^W ^W reads
// in order.
func (e *lineEditor) kill(lo, hi int, backward bool) {
	if lo < 0 {
		lo = 0
	}
	if hi > len(e.runes) {
		hi = len(e.runes)
	}
	if lo >= hi {
		return
	}
	text := string(e.runes[lo:hi])
	accumulate := e.lastOp == opKill && len(e.kills) > 0
	e.mark(opKill)
	e.runes = append(e.runes[:lo], e.runes[hi:]...)
	if e.cursor > hi {
		e.cursor -= hi - lo
	} else if e.cursor > lo {
		e.cursor = lo
	}
	switch {
	case accumulate && backward:
		e.kills[0] = text + e.kills[0]
	case accumulate:
		e.kills[0] += text
	default:
		e.kills = append([]string{text}, e.kills...)
		if len(e.kills) > lineEditorKillRing {
			e.kills = e.kills[:lineEditorKillRing]
		}
	}
	e.killIdx = 0
}

// At the end of a line ^K takes the newline.
func (e *lineEditor) killToEnd() {
	if end := e.lineEnd(e.cursor); end > e.cursor {
		e.kill(e.cursor, end, false)
		return
	}
	e.kill(e.cursor, min(e.cursor+1, len(e.runes)), false)
}

func (e *lineEditor) killToStart() { e.kill(e.lineStart(e.cursor), e.cursor, true) }

// ^W is whitespace-delimited; M-DEL is word-character-delimited, as in readline.
func (e *lineEditor) killWordBack() { e.kill(e.shellWordStart(), e.cursor, true) }

func (e *lineEditor) killWordBackAlpha() { e.kill(e.wordStart(), e.cursor, true) }

func (e *lineEditor) killWordForward() { e.kill(e.cursor, e.wordEnd(), false) }

func (e *lineEditor) deleteHorizontalSpace() {
	lo, hi := e.cursor, e.cursor
	for lo > 0 && isLineSpace(e.runes[lo-1]) {
		lo--
	}
	for hi < len(e.runes) && isLineSpace(e.runes[hi]) {
		hi++
	}
	if lo == hi {
		return
	}
	// Not a kill: readline keeps horizontal space off the ring.
	e.mark(opOther)
	e.runes = append(e.runes[:lo], e.runes[hi:]...)
	e.cursor = lo
}

func (e *lineEditor) yank() bool {
	if len(e.kills) == 0 {
		return false
	}
	e.mark(opOther)
	at := e.cursor
	text := e.kills[e.killIdx]
	e.insertText(text)
	e.yankAt, e.yankLen = at, len([]rune(text))
	e.lastOp = opYank
	return true
}

// yankPop replaces what the last yank inserted; only directly after a yank.
func (e *lineEditor) yankPop() bool {
	if e.lastOp != opYank || len(e.kills) < 2 {
		return false
	}
	e.mark(opOther)
	e.runes = append(e.runes[:e.yankAt], e.runes[e.yankAt+e.yankLen:]...)
	e.cursor = e.yankAt
	e.killIdx = (e.killIdx + 1) % len(e.kills)
	text := e.kills[e.killIdx]
	e.insertText(text)
	e.yankLen = len([]rune(text))
	e.lastOp = opYank
	return true
}

// Bypasses lastOp: the yanks own their op memory.
func (e *lineEditor) insertText(s string) {
	add := []rune(s)
	e.runes = append(e.runes, add...)
	copy(e.runes[e.cursor+len(add):], e.runes[e.cursor:len(e.runes)-len(add)])
	copy(e.runes[e.cursor:], add)
	e.cursor += len(add)
}

// Two word definitions, as readline has: ^W cuts shell words, M-b/M-f/M-d/M-DEL
// walk alphanumeric runs.

func isLineSpace(r rune) bool { return r == ' ' || r == '\t' }

func isWordRune(r rune) bool {
	return r == '_' || unicode.IsLetter(r) || unicode.IsDigit(r)
}

func (e *lineEditor) shellWordStart() int {
	i := e.cursor
	for i > 0 && isLineSpace(e.runes[i-1]) {
		i--
	}
	for i > 0 && !isLineSpace(e.runes[i-1]) {
		i--
	}
	return i
}

func (e *lineEditor) wordStart() int { return e.wordStartBefore(e.cursor) }

func (e *lineEditor) wordEnd() int { return e.wordEndAfter(e.cursor) }

func (e *lineEditor) wordStartBefore(i int) int {
	for i > 0 && !isWordRune(e.runes[i-1]) {
		i--
	}
	for i > 0 && isWordRune(e.runes[i-1]) {
		i--
	}
	return i
}

func (e *lineEditor) wordEndAfter(i int) int {
	for i < len(e.runes) && !isWordRune(e.runes[i]) {
		i++
	}
	for i < len(e.runes) && isWordRune(e.runes[i]) {
		i++
	}
	return i
}

func (e *lineEditor) wordLeft()  { e.lastOp = opOther; e.cursor = e.wordStart() }
func (e *lineEditor) wordRight() { e.lastOp = opOther; e.cursor = e.wordEnd() }

// At the end of the line ^T swaps the last two runes.
func (e *lineEditor) transposeChars() {
	if len(e.runes) < 2 {
		return
	}
	i := e.cursor
	if i >= len(e.runes) {
		i = len(e.runes) - 1
	}
	if i == 0 {
		return
	}
	e.mark(opOther)
	e.runes[i-1], e.runes[i] = e.runes[i], e.runes[i-1]
	e.cursor = min(i+1, len(e.runes))
}

// At the end of the line M-t swaps the last two words.
func (e *lineEditor) transposeWords() {
	end2 := e.wordEndAfter(e.cursor)
	start2 := e.wordStartBefore(end2)
	end1 := start2
	for end1 > 0 && !isWordRune(e.runes[end1-1]) {
		end1--
	}
	start1 := e.wordStartBefore(end1)
	if start1 >= end1 || start2 >= end2 {
		return
	}
	e.mark(opOther)
	w1 := append([]rune(nil), e.runes[start1:end1]...)
	w2 := append([]rune(nil), e.runes[start2:end2]...)
	mid := append([]rune(nil), e.runes[end1:start2]...)
	tail := append([]rune(nil), e.runes[end2:]...)
	e.runes = append(e.runes[:start1], w2...)
	e.runes = append(e.runes, mid...)
	e.runes = append(e.runes, w1...)
	e.cursor = len(e.runes)
	e.runes = append(e.runes, tail...)
}

func (e *lineEditor) caseWord(mode rune) {
	start := e.cursor
	for start < len(e.runes) && !isWordRune(e.runes[start]) {
		start++
	}
	end := start
	for end < len(e.runes) && isWordRune(e.runes[end]) {
		end++
	}
	if start == end {
		return
	}
	e.mark(opOther)
	for i := start; i < end; i++ {
		switch mode {
		case 'u':
			e.runes[i] = unicode.ToUpper(e.runes[i])
		case 'l':
			e.runes[i] = unicode.ToLower(e.runes[i])
		case 'c':
			if i == start {
				e.runes[i] = unicode.ToUpper(e.runes[i])
			} else {
				e.runes[i] = unicode.ToLower(e.runes[i])
			}
		}
	}
	e.cursor = end
}

// Consecutive duplicates collapse.
func (e *lineEditor) remember(line string) {
	line = strings.TrimSpace(line)
	if line == "" {
		e.hindex = len(e.history)
		return
	}
	if n := len(e.history); n == 0 || e.history[n-1] != line {
		e.history = append(e.history, line)
		if len(e.history) > lineEditorHistoryMax {
			e.history = append(e.history[:0], e.history[len(e.history)-lineEditorHistoryMax:]...)
		}
	}
	e.hindex = len(e.history)
	e.stash = ""
}

// Every history motion goes through fetch: stash the typed line, record origin for
// M-r, end the op run.
func (e *lineEditor) fetch(idx int) {
	if e.hindex == len(e.history) && idx != e.hindex {
		e.stash = e.String()
	}
	e.hindex = idx
	if idx >= len(e.history) {
		e.set(e.stash)
	} else {
		e.set(e.history[idx])
	}
	e.origin = e.String()
	e.undo = e.undo[:0] // a different line has a different past
	e.lastOp = opOther
}

func (e *lineEditor) historyPrev() {
	if e.hindex == 0 || len(e.history) == 0 {
		return
	}
	e.fetch(e.hindex - 1)
}

func (e *lineEditor) historyNext() {
	if e.hindex >= len(e.history) {
		return
	}
	e.fetch(e.hindex + 1)
}

func (e *lineEditor) historyFirst() {
	if len(e.history) == 0 {
		return
	}
	e.fetch(0)
}

func (e *lineEditor) historyLast() { e.fetch(len(e.history)) }

// The text left of the cursor is the pattern.
func (e *lineEditor) historyPrefix(dir int) bool {
	prefix := string(e.runes[:e.cursor])
	for i := e.hindex + dir; i >= 0 && i < len(e.history); i += dir {
		if strings.HasPrefix(e.history[i], prefix) {
			at := e.cursor
			e.fetch(i)
			e.cursor = min(at, len(e.runes)) // the pattern stays under the cursor
			return true
		}
	}
	return false
}

// A repeat replaces the inserted word with the last word of the entry before.
func (e *lineEditor) yankLastArg() bool {
	if len(e.history) == 0 {
		return false
	}
	back := 1
	if e.lastOp == opYankArg {
		back = e.yankArg + 1
	}
	if back > len(e.history) {
		return false
	}
	fields := strings.Fields(e.history[len(e.history)-back])
	if len(fields) == 0 {
		return false
	}
	word := fields[len(fields)-1]
	repeat := e.lastOp == opYankArg // mark() clears it: ask first
	e.mark(opOther)
	if repeat {
		e.runes = append(e.runes[:e.lastArgAt], e.runes[e.lastArgAt+e.lastArgLen:]...)
		e.cursor = e.lastArgAt
	}
	e.lastArgAt = e.cursor
	e.insertText(word)
	e.lastArgLen = len([]rune(word))
	e.yankArg = back
	e.lastOp = opYankArg
	return true
}

// The i-search is a mode inside the box; any editing key ends it keeping what it found.

type histSearch struct {
	needle []rune
	dir    int // -1 backward (^R), +1 forward (^S)
	idx    int // history index of the current match; len(history) = the held line
	failed bool

	// held is the line as it was when the search began: what ^G restores.
	held   string
	cursor int
	hindex int
}

func (e *lineEditor) searching() bool { return e.search != nil }

func (e *lineEditor) beginSearch(dir int) {
	e.lastOp = opOther
	e.search = &histSearch{
		dir:    dir,
		idx:    e.hindex,
		held:   e.String(),
		cursor: e.cursor,
		hindex: e.hindex,
	}
}

// An empty needle reuses the last accepted one, as readline does.
func (e *lineEditor) searchAgain(dir int) {
	if e.search == nil {
		e.beginSearch(dir)
		return
	}
	e.search.dir = dir
	e.step(dir)
}

func (e *lineEditor) searchType(r rune) {
	if e.search == nil {
		return
	}
	e.search.needle = append(e.search.needle, r)
	e.step(0)
}

// Re-runs from the held position so shortening can undo a failure.
func (e *lineEditor) searchBackspace() {
	if e.search == nil || len(e.search.needle) == 0 {
		return
	}
	e.search.needle = e.search.needle[:len(e.search.needle)-1]
	e.search.idx = e.search.hindex
	e.step(0)
}

// dir 0: from here (the needle changed); otherwise the next match past this one.
func (e *lineEditor) step(dir int) {
	s := e.search
	needle := string(s.needle)
	if needle == "" {
		s.failed = false
		return
	}
	start := s.idx
	if dir != 0 {
		start += dir
	} else if s.dir < 0 && start >= len(e.history) {
		start = len(e.history) - 1
	}
	for i := start; i >= 0 && i < len(e.history); i += sign(s.dir) {
		if strings.Contains(e.history[i], needle) {
			s.idx, s.failed = i, false
			e.set(e.history[i])
			if at := strings.Index(e.history[i], needle); at >= 0 {
				e.cursor = len([]rune(e.history[i][:at]))
			}
			return
		}
	}
	s.failed = true
}

func sign(d int) int {
	if d < 0 {
		return -1
	}
	return 1
}

// Esc or any editing key ends the search keeping what it found; the history cursor
// follows.
func (e *lineEditor) endSearch() {
	if e.search == nil {
		return
	}
	if !e.search.failed && e.search.idx < len(e.history) && len(e.search.needle) > 0 {
		e.hindex = e.search.idx
		e.origin = e.String()
	}
	e.search = nil
	e.lastOp = opOther
}

func (e *lineEditor) abortSearch() {
	if e.search == nil {
		return
	}
	s := e.search
	e.search = nil
	e.set(s.held)
	e.cursor = min(s.cursor, len(e.runes))
	e.hindex = s.hindex
	e.lastOp = opOther
}

// Reads readline's i-search prompt while a search runs.
func (e *lineEditor) prompt(base string) string {
	if e.search == nil {
		return base
	}
	name := "reverse-i-search"
	if e.search.dir > 0 {
		name = "i-search"
	}
	if e.search.failed {
		name = "failed " + name
	}
	return "(" + name + ")`" + string(e.search.needle) + "': "
}

// Clipped to w columns with the cursor kept in view.
func (e *lineEditor) render(prefix string, w int) string {
	if w <= 0 {
		return ""
	}
	pw := runewidth.StringWidth(prefix)
	avail := w - pw
	if avail < 4 {
		return clipToWidth(prefix, w)
	}
	// One column is reserved so the cursor can sit past the end.
	lo := 0
	for {
		width := 0
		hi := lo
		for hi < len(e.runes) && width+runewidth.RuneWidth(e.runes[hi]) < avail {
			width += runewidth.RuneWidth(e.runes[hi])
			hi++
		}
		if e.cursor <= hi || lo >= len(e.runes) {
			return prefix + renderWithCursor(flattenNewlines(e.runes[lo:hi]), e.cursor-lo, -1, -1)
		}
		lo++
	}
}

// Past `rows` the window follows the cursor.
func (e *lineEditor) wrap(prefix string, w, rows int) []string {
	return e.wrapWash(prefix, w, rows, -1, -1)
}

func (e *lineEditor) wrapWash(prefix string, w, rows, lo, hi int) []string {
	if w <= 0 || rows < 1 {
		return nil
	}
	pw := runewidth.StringWidth(prefix)
	avail := w - pw
	if avail < 4 || rows == 1 {
		return []string{e.render(prefix, w)}
	}
	lines, at := e.layout(avail)
	top := 0
	if at >= rows {
		top = at - rows + 1
	}
	end := min(top+rows, len(lines))
	out := make([]string, 0, end-top)
	pad := strings.Repeat(" ", pw)
	for i := top; i < end; i++ {
		head := pad
		if i == 0 {
			head = prefix
		}
		idx := -1
		if i == at {
			idx = e.cursor - lines[i].start
		}
		out = append(out, head+renderWithCursor(lines[i].runes, idx, lo-lines[i].start, hi-lines[i].start))
	}
	return out
}

// The index is carried, not recomputed: a row break on a newline consumes a rune
// in no row.
type visualRow struct {
	runes []rune
	start int
}

func (e *lineEditor) layout(avail int) ([]visualRow, int) {
	rows := []visualRow{{start: 0}}
	at, width := 0, 0
	for i, r := range e.runes {
		if i == e.cursor {
			at = len(rows) - 1
		}
		if r == '\n' {
			rows = append(rows, visualRow{start: i + 1})
			width = 0
			continue
		}
		rw := runewidth.RuneWidth(r)
		if width+rw > avail-1 {
			rows = append(rows, visualRow{start: i})
			width = 0
		}
		last := &rows[len(rows)-1]
		last.runes = append(last.runes, r)
		width += rw
	}
	if e.cursor >= len(e.runes) {
		at = len(rows) - 1
	}
	return rows, at
}

// A newline drawn into a one-row view would desync the painter.
func flattenNewlines(runes []rune) []rune {
	out := make([]rune, len(runes))
	for i, r := range runes {
		if r == '\n' {
			r = ' '
		}
		out[i] = r
	}
	return out
}

// The pager hides the real cursor, so the caret is drawn in reverse video.
func renderWithCursor(runes []rune, at int, washFrom, washTo int) string {
	var b strings.Builder
	washing := false
	for i, r := range runes {
		if i == at {
			if washing {
				b.WriteString("\x1b[27m")
				washing = false
			}
			b.WriteString("\x1b[7m")
			b.WriteRune(r)
			b.WriteString("\x1b[27m")
			continue
		}
		if !washing && i >= washFrom && i < washTo {
			b.WriteString("\x1b[7m")
			washing = true
		} else if washing && i >= washTo {
			b.WriteString("\x1b[27m")
			washing = false
		}
		b.WriteRune(r)
	}
	if washing {
		b.WriteString("\x1b[27m")
	}
	if at >= len(runes) {
		b.WriteString("\x1b[7m \x1b[27m") // the cursor past the end of the line
	}
	return b.String()
}

func (e *lineEditor) firstNonBlank() {
	e.lastOp = opOther
	i := e.lineStart(e.cursor)
	end := e.lineEnd(i)
	for i < end && (e.runes[i] == ' ' || e.runes[i] == '\t') {
		i++
	}
	e.cursor = i
}

func (e *lineEditor) bufferStart() { e.lastOp = opOther; e.cursor = 0 }
func (e *lineEditor) bufferEnd()   { e.lastOp = opOther; e.cursor = len(e.runes) }

func (e *lineEditor) paragraph(dir int) {
	e.lastOp = opOther
	for {
		before := e.cursor
		e.lineStep(dir)
		if e.cursor == before {
			return
		}
		if e.lineEnd(e.cursor) == e.lineStart(e.cursor) {
			return
		}
	}
}

// Searches from the cursor, wrapping once; leaves the cursor on the match.
func (e *lineEditor) findRune(q string, dir int) bool {
	if q == "" || len(e.runes) == 0 {
		return false
	}
	hay := []rune(strings.ToLower(string(e.runes)))
	needle := []rune(strings.ToLower(q))
	at := func(i int) bool {
		if i < 0 || i+len(needle) > len(hay) {
			return false
		}
		for k, r := range needle {
			if hay[i+k] != r {
				return false
			}
		}
		return true
	}
	n := len(hay)
	for step := 1; step <= n; step++ {
		i := ((e.cursor+dir*step)%n + n) % n
		if at(i) {
			e.lastOp = opOther
			e.cursor = i
			return true
		}
	}
	return false
}

func (e *lineEditor) selectionText(lo, hi int) string {
	lo, hi = max(min(lo, hi), 0), min(max(lo, hi), len(e.runes))
	if lo >= hi {
		return ""
	}
	return string(e.runes[lo:hi])
}

func (e *lineEditor) rowCount(avail int) int {
	rows, _ := e.layout(max(avail, 1))
	return len(rows)
}
