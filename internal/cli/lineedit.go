package cli

// A one-line text editor with readline's emacs keymap behind it: a cursor,
// two kinds of word, a kill ring, an undo stack, history, and an incremental
// history search.
//
// It exists because the pager's two existing input boxes are `q += string(b)`
// with `b >= 0x20 && b < 0x7f` -- no cursor, no motions, no history, and no
// characters outside ASCII. Measured, before this file: typing `café` into the
// '/' box yields "caf", silently. A command line needs all four, and the search
// box should eventually be this same editor with a different acceptor.
//
// THE POINT OF THE WHOLE FILE: the ':' box must feel like a shell prompt,
// because that is what it looks like and what its grammar is (the CLI's own).
// Every method here is one readline function, under readline's name; which
// keys reach them is keymap.go's business, and what a terminal sends for those
// keys is key_input.go's. The division matters -- the first version of this box
// had correct behaviour bound to encodings the terminal never sends.
//
// The functions readline has that this one deliberately does not are listed at
// the end of the ':' block in keymap.go, each with its reason.

import (
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/mattn/go-runewidth"
)

// lineEditor is one editable line. Runes, not bytes: the byte-at-a-time input
// path feeds it through insertByte, which accumulates a partial UTF-8 sequence
// until it is a whole rune.
type lineEditor struct {
	runes  []rune
	cursor int // rune index in [0, len(runes)]

	// pending holds the bytes of an incomplete UTF-8 sequence. A terminal
	// delivers a multi-byte rune as separate reads whenever it feels like it,
	// and every box in this program used to drop the tail.
	pending []byte

	// history is oldest-first. hindex == len(history) means "not browsing";
	// stash keeps the line that was being typed when browsing began, so
	// stepping back down to the bottom returns it rather than an empty line.
	history []string
	hindex  int
	stash   string

	// origin is the text of the line as it was FETCHED: what M-r reverts to.
	// readline remembers an original per history entry; a box that edits one
	// line at a time needs exactly one.
	origin string

	// The kill ring. readline's, in miniature: consecutive kills accumulate
	// into the top entry (in the direction they were made), ^Y yanks the top,
	// and M-y rotates. yankAt/yankLen mark what the last yank put in, because
	// yank-pop REPLACES it rather than inserting again.
	kills   []string
	killIdx int
	yankAt  int
	yankLen int

	// undo is a stack of whole-line snapshots. Whole lines, not deltas: a
	// command line is short, an edit is rare, and a delta log is a second
	// representation of the same thing that can disagree with it.
	undo []lineSnapshot

	// lastOp is what the PREVIOUS action was. Three readline behaviours are
	// defined in terms of it -- consecutive kills accumulate, M-y only follows
	// a yank, and a run of typing is one undo step -- so it is state the
	// editor owns rather than something each caller remembers to pass.
	lastOp lineOp

	// yankArg is M-.'s cursor: how many history entries back the last
	// yank-last-arg reached, so pressing it again walks further rather than
	// inserting the same word twice.
	yankArg int

	// search is the ^R/^S incremental history search, nil when not searching.
	search *histSearch

	// lastArgAt/lastArgLen mark what M-. inserted, for the same reason
	// yankAt/yankLen do.
	lastArgAt  int
	lastArgLen int
}

// lineOp names the previous action, for the three bindings whose meaning
// depends on it.
type lineOp uint8

const (
	opOther lineOp = iota
	opInsert
	opKill
	opYank
	opYankArg
)

// lineSnapshot is one undo step.
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
	// THE KILL RING SURVIVES the box closing, exactly as it survives a line at
	// a shell prompt: what you cut before is what ^Y pastes now.
}

func (e *lineEditor) String() string { return string(e.runes) }
func (e *lineEditor) empty() bool    { return len(e.runes) == 0 }

// set replaces the whole line and parks the cursor at the end.
func (e *lineEditor) set(s string) {
	e.runes = append(e.runes[:0], []rune(s)...)
	e.cursor = len(e.runes)
	e.pending = e.pending[:0]
}

// ---------------------------------------------------------------------------
// Undo, and the op memory the ring and yank-pop are defined against.
// ---------------------------------------------------------------------------

// snapshot pushes the line onto the undo stack. Every mutating op calls it
// through mark(); a RUN OF TYPING collapses into one step, because undoing a
// sentence one letter at a time is not undo, it is rewind.
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

// mark records "an op of this kind is about to happen": it snapshots unless
// this is a continuation of a run of the same collapsible op, and then sets
// lastOp. Every mutating method starts with it, which is what makes the three
// history-sensitive bindings (kill accumulation, M-y, undo grouping) correct
// by construction rather than by each caller remembering.
func (e *lineEditor) mark(op lineOp) {
	if !(op == opInsert && e.lastOp == opInsert) {
		e.snapshot()
	}
	e.lastOp = op
}

// undoOne is ^_ (and ^X^U, which this box does not bind): one step back.
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

// revert is M-r: back to the line as it was fetched, in one press.
func (e *lineEditor) revert() {
	e.mark(opOther)
	e.set(e.origin)
	e.lastOp = opOther
}

// insertByte feeds one input byte. It returns false for a byte it does not
// consider text (a control byte with a binding elsewhere), so the caller can
// tell "handled as text" from "not for me".
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

// insert puts a whole string in at the cursor: what a completion does.
func (e *lineEditor) insert(s string) {
	for _, r := range s {
		e.insertRune(r)
	}
}

// ---------------------------------------------------------------------------
// The motions. Names are readline's, because the fingers that will press these
// keys learned them there. A motion is not an edit: it ends any run of typing
// (so the next keystroke starts a fresh undo step) and never snapshots.
// ---------------------------------------------------------------------------

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

func (e *lineEditor) home() { e.lastOp = opOther; e.cursor = 0 }
func (e *lineEditor) end()  { e.lastOp = opOther; e.cursor = len(e.runes) }

// backspace deletes the rune BEFORE the cursor (^H, DEL).
func (e *lineEditor) backspace() {
	if e.cursor == 0 {
		return
	}
	e.mark(opOther)
	e.runes = append(e.runes[:e.cursor-1], e.runes[e.cursor:]...)
	e.cursor--
}

// deleteForward deletes the rune AT the cursor (^D, Delete).
func (e *lineEditor) deleteForward() {
	if e.cursor >= len(e.runes) {
		return
	}
	e.mark(opOther)
	e.runes = append(e.runes[:e.cursor], e.runes[e.cursor+1:]...)
}

// ---------------------------------------------------------------------------
// Killing and yanking. Every kill goes through kill(), which owns the ring:
// that is what makes ^Y paste what ^K/^U/^W/M-d/M-DEL cut, and what makes a
// RUN of kills accumulate into one ring entry the way readline's does.
// ---------------------------------------------------------------------------

// kill removes runes [lo, hi) and puts the text on the ring. backward says
// which side of the cursor it came from, because an accumulating kill grows
// the top entry at the end for a forward kill and at the front for a backward
// one -- so ^W ^W yields the two words in reading order, not reversed.
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

// killToEnd is ^K.
func (e *lineEditor) killToEnd() { e.kill(e.cursor, len(e.runes), false) }

// killToStart is ^U. readline's ^U kills to the start of the LINE, which for a
// one-line editor is the whole prefix.
func (e *lineEditor) killToStart() { e.kill(0, e.cursor, true) }

// killWordBack is ^W: kill the whitespace-delimited word before the cursor,
// including the run of spaces that leads to it. readline's ^W is
// whitespace-delimited (unlike M-DEL, which is word-character-delimited), and
// that is the right one here: the tokens in a command line are shell words.
func (e *lineEditor) killWordBack() { e.kill(e.shellWordStart(), e.cursor, true) }

// killWordBackAlpha is M-DEL / M-Backspace: backward-kill-word over READLINE
// words (alphanumeric runs), which is why it is not ^W. On `figaro send --id`
// it takes `id` and leaves the `--`; ^W takes `--id` whole.
func (e *lineEditor) killWordBackAlpha() { e.kill(e.wordStart(), e.cursor, true) }

// killWordForward is M-d: kill to the end of the current or next word.
func (e *lineEditor) killWordForward() { e.kill(e.cursor, e.wordEnd(), false) }

// deleteHorizontalSpace is M-\: close up the whitespace around the cursor.
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
	// NOT A KILL: readline does not put horizontal space on the ring, and a ^Y
	// that pastes back three spaces you deleted on purpose is a bug.
	e.mark(opOther)
	e.runes = append(e.runes[:lo], e.runes[hi:]...)
	e.cursor = lo
}

// yank is ^Y: insert the top of the ring at the cursor.
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

// yankPop is M-y: rotate the ring and REPLACE what the last yank inserted.
// Only meaningful directly after a yank, which is what lastOp is for.
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

// insertText splices a string in at the cursor WITHOUT touching lastOp: the
// yanks own their op memory, and going through insertRune would clobber it.
func (e *lineEditor) insertText(s string) {
	add := []rune(s)
	e.runes = append(e.runes, add...)
	copy(e.runes[e.cursor+len(add):], e.runes[e.cursor:len(e.runes)-len(add)])
	copy(e.runes[e.cursor:], add)
	e.cursor += len(add)
}

// ---------------------------------------------------------------------------
// Words. TWO DEFINITIONS, deliberately, because readline has two: ^W cuts a
// shell word (whitespace-delimited) and M-b/M-f/M-d/M-DEL walk readline words
// (runs of alphanumerics). Collapsing them would make `--id` one token for
// every key or two tokens for every key, and both are wrong half the time.
// ---------------------------------------------------------------------------

func isLineSpace(r rune) bool { return r == ' ' || r == '\t' }

func isWordRune(r rune) bool {
	return r == '_' || unicode.IsLetter(r) || unicode.IsDigit(r)
}

// shellWordStart is where ^W's kill begins: back over spaces, then back over
// everything that is not a space.
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

// wordStart is M-b's destination: back over non-word runes, then to the front
// of the word.
func (e *lineEditor) wordStart() int { return e.wordStartBefore(e.cursor) }

// wordEnd is M-f's destination: forward over non-word runes, then to the end
// of the word.
func (e *lineEditor) wordEnd() int { return e.wordEndAfter(e.cursor) }

// wordStartBefore and wordEndAfter are the same two walks from an arbitrary
// index: transpose-words needs four boundaries and cannot move the cursor to
// find each one.
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

// wordLeft / wordRight are M-b / M-f.
func (e *lineEditor) wordLeft()  { e.lastOp = opOther; e.cursor = e.wordStart() }
func (e *lineEditor) wordRight() { e.lastOp = opOther; e.cursor = e.wordEnd() }

// ---------------------------------------------------------------------------
// Transposing and case, the rest of readline's "changing text" block.
// ---------------------------------------------------------------------------

// transposeChars is ^T: swap the two runes around the cursor and step past
// them. At the end of the line it swaps the last two, which is the whole point
// of the key -- `:oepn` typed too fast is fixed by ^T at the end.
func (e *lineEditor) transposeChars() {
	if len(e.runes) < 2 {
		return
	}
	i := e.cursor
	if i >= len(e.runes) {
		i = len(e.runes) - 1 // at the end: the last two
	}
	if i == 0 {
		return
	}
	e.mark(opOther)
	e.runes[i-1], e.runes[i] = e.runes[i], e.runes[i-1]
	e.cursor = min(i+1, len(e.runes))
}

// transposeWords is M-t: drag the word before the cursor past the word after
// it, leaving the cursor after both. At the end of the line that is the last
// two words, which is the case the key is actually pressed in.
func (e *lineEditor) transposeWords() {
	// Word 2 is the one the cursor is in or before; word 1 is the one behind
	// it. Each boundary is found from the previous one, so a cursor sitting in
	// the middle of either word gives the same answer as one sitting between.
	end2 := e.wordEndAfter(e.cursor)
	start2 := e.wordStartBefore(end2)
	end1 := start2
	for end1 > 0 && !isWordRune(e.runes[end1-1]) {
		end1--
	}
	start1 := e.wordStartBefore(end1)
	if start1 >= end1 || start2 >= end2 {
		return // fewer than two words to swap
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

// caseWord is M-u / M-l / M-c: apply a case change from the cursor to the end
// of the current or next word, and leave the cursor there.
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

// ---------------------------------------------------------------------------
// History.
// ---------------------------------------------------------------------------

// remember pushes an accepted line. Consecutive duplicates collapse, because a
// history full of the same command is a history you have to scroll past.
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

// fetch loads a history entry (or the stash) into the line. ONE DOOR, because
// every history motion has to do the same three things -- stash the line being
// typed the first time you leave it, remember what was fetched so M-r can
// revert to it, and end whatever op run was in progress.
func (e *lineEditor) fetch(idx int) {
	if e.hindex == len(e.history) && idx != e.hindex {
		e.stash = e.String() // keep what was being typed
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

// historyPrev is ^P / Up: one step toward older.
func (e *lineEditor) historyPrev() {
	if e.hindex == 0 || len(e.history) == 0 {
		return
	}
	e.fetch(e.hindex - 1)
}

// historyNext is ^N / Down: one step toward newer, ending at what was stashed.
func (e *lineEditor) historyNext() {
	if e.hindex >= len(e.history) {
		return
	}
	e.fetch(e.hindex + 1)
}

// historyFirst / historyLast are M-< / M->: the ends of the list.
func (e *lineEditor) historyFirst() {
	if len(e.history) == 0 {
		return
	}
	e.fetch(0)
}

func (e *lineEditor) historyLast() { e.fetch(len(e.history)) }

// historyPrefix is M-p / M-n: non-incremental search for a history entry
// starting with what is already typed. The text left of the cursor is the
// pattern, which is what makes it "type `:se` then M-p" rather than a mode.
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

// yankLastArg is M-. / M-_: insert the last word of the previous command, and
// on a repeat REPLACE it with the last word of the one before that. At a shell
// this is how you reuse the path you just typed; here it is how you reuse the
// aria id you just addressed.
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
		// A repeat REPLACES what the previous M-. put in rather than piling a
		// second word on top of it.
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

// ---------------------------------------------------------------------------
// The incremental history search: ^R and ^S.
//
// It is a MODE INSIDE THE BOX rather than a mode of the pager, because that is
// what it is at a shell: the line you were typing is held, the needle grows and
// shrinks with what you type, and any editing key ends the search and leaves
// you with whatever it found. The pager knows only that the box's prompt reads
// differently while it runs (see prompt).
// ---------------------------------------------------------------------------

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

// beginSearch is the first ^R (or ^S).
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

// searchAgain is a repeated ^R/^S: same needle, next match in that direction.
// With an empty needle it reuses the last accepted line's, as readline does.
func (e *lineEditor) searchAgain(dir int) {
	if e.search == nil {
		e.beginSearch(dir)
		return
	}
	e.search.dir = dir
	e.step(dir)
}

// searchType extends the needle.
func (e *lineEditor) searchType(r rune) {
	if e.search == nil {
		return
	}
	e.search.needle = append(e.search.needle, r)
	e.step(0)
}

// searchBackspace shortens it, and re-runs from the held position: shortening
// a needle must be able to UNDO a failure, not stay stuck on it.
func (e *lineEditor) searchBackspace() {
	if e.search == nil || len(e.search.needle) == 0 {
		return
	}
	e.search.needle = e.search.needle[:len(e.search.needle)-1]
	e.search.idx = e.search.hindex
	e.step(0)
}

// step runs the search. dir 0 means "from where we are" (a needle changed);
// non-zero means "the next one past this match".
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

// endSearch leaves the search KEEPING what it found -- Esc, or any editing key,
// which is readline's rule. The found line becomes the line being edited, and
// the history cursor moves to where the search landed so ^P walks on from
// there.
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

// abortSearch is ^G: put the line back the way it was before ^R.
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

// prompt is what the box's prefix reads: ':' normally, and readline's own
// i-search prompt while a search runs, because a mode you cannot see is a mode
// you cannot leave.
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

// ---------------------------------------------------------------------------
// Rendering.
// ---------------------------------------------------------------------------

// render draws the line with a prefix and a visible cursor, clipped to w
// columns with the CURSOR KEPT IN VIEW: a line longer than the pane scrolls
// under a fixed prompt rather than hiding the thing being typed.
func (e *lineEditor) render(prefix string, w int) string {
	if w <= 0 {
		return ""
	}
	pw := runewidth.StringWidth(prefix)
	avail := w - pw
	if avail < 4 {
		return clipToWidth(prefix, w)
	}
	// The cursor sits at a rune index; find the window of runes that fits and
	// contains it. One column is reserved so the cursor can sit past the end.
	lo := 0
	for {
		width := 0
		hi := lo
		for hi < len(e.runes) && width+runewidth.RuneWidth(e.runes[hi]) < avail {
			width += runewidth.RuneWidth(e.runes[hi])
			hi++
		}
		if e.cursor <= hi || lo >= len(e.runes) {
			return prefix + renderWithCursor(e.runes[lo:hi], e.cursor-lo)
		}
		lo++
	}
}

// wrap draws the line ACROSS ROWS instead of scrolling it under the prompt: a
// command long enough to leave the pane is a command you cannot read, and the
// pit is a region with rows to spare. Gluck: "the command mode content should
// be wrapped, not single line. it should abide by the same max limits of the
// other pits."
//
// The prompt sits on the first row and the continuations are indented under
// it, so the text stays in one column. Past `rows` the window slides to keep
// the CURSOR visible -- the same promise the single-row form made, one
// dimension up: what you are typing is always on the screen.
func (e *lineEditor) wrap(prefix string, w, rows int) []string {
	if w <= 0 || rows < 1 {
		return nil
	}
	pw := runewidth.StringWidth(prefix)
	avail := w - pw
	if avail < 4 || rows == 1 {
		return []string{e.render(prefix, w)}
	}
	// Lay the runes out in rows of `avail` columns, remembering which row the
	// cursor lands in.
	var lines [][]rune
	var cur []rune
	width, at := 0, 0
	for i, r := range e.runes {
		rw := runewidth.RuneWidth(r)
		if width+rw > avail-1 {
			lines = append(lines, cur)
			cur, width = nil, 0
		}
		if i == e.cursor {
			at = len(lines)
		}
		cur = append(cur, r)
		width += rw
	}
	lines = append(lines, cur)
	if e.cursor >= len(e.runes) {
		at = len(lines) - 1
	}
	// The window of rows that fits, holding the cursor's row.
	top := 0
	if at >= rows {
		top = at - rows + 1
	}
	end := min(top+rows, len(lines))
	out := make([]string, 0, end-top)
	pad := strings.Repeat(" ", pw)
	seen := 0
	for i := top; i < end; i++ {
		head := pad
		if i == 0 {
			head = prefix
		}
		// The cursor's rune index within this row.
		idx := -1
		if i == at {
			idx = e.cursor - runeCountBefore(lines, i)
		}
		out = append(out, head+renderWithCursor(lines[i], idx))
		seen += len(lines[i])
	}
	return out
}

// runeCountBefore is how many runes precede row i.
func runeCountBefore(lines [][]rune, i int) int {
	n := 0
	for k := 0; k < i && k < len(lines); k++ {
		n += len(lines[k])
	}
	return n
}

// renderWithCursor paints the cursor cell in reverse video. The pager owns the
// whole grid and hides the real cursor, so the caret has to be drawn.
func renderWithCursor(runes []rune, at int) string {
	var b strings.Builder
	for i, r := range runes {
		if i == at {
			b.WriteString("\x1b[7m")
			b.WriteRune(r)
			b.WriteString("\x1b[27m")
			continue
		}
		b.WriteRune(r)
	}
	if at >= len(runes) {
		b.WriteString("\x1b[7m \x1b[27m") // the cursor past the end of the line
	}
	return b.String()
}
