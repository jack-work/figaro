package cli

// A one-line text editor with emacs/readline motions, a cursor, and history.
//
// It exists because the pager's two existing input boxes are `q += string(b)`
// with `b >= 0x20 && b < 0x7f` -- no cursor, no motions, no history, and no
// characters outside ASCII. Measured, before this file: typing `café` into the
// '/' box yields "caf", silently. A command line needs all four, and the search
// box should eventually be this same editor with a different acceptor.

import (
	"strings"
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
}

const lineEditorHistoryMax = 200

func (e *lineEditor) reset() {
	e.runes = e.runes[:0]
	e.cursor = 0
	e.pending = e.pending[:0]
	e.hindex = len(e.history)
	e.stash = ""
}

func (e *lineEditor) String() string { return string(e.runes) }
func (e *lineEditor) empty() bool    { return len(e.runes) == 0 }

// set replaces the whole line and parks the cursor at the end.
func (e *lineEditor) set(s string) {
	e.runes = append(e.runes[:0], []rune(s)...)
	e.cursor = len(e.runes)
	e.pending = e.pending[:0]
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
// keys learned them there.
// ---------------------------------------------------------------------------

func (e *lineEditor) left() {
	if e.cursor > 0 {
		e.cursor--
	}
}

func (e *lineEditor) right() {
	if e.cursor < len(e.runes) {
		e.cursor++
	}
}

func (e *lineEditor) home() { e.cursor = 0 }
func (e *lineEditor) end()  { e.cursor = len(e.runes) }

// backspace deletes the rune BEFORE the cursor (^H, DEL).
func (e *lineEditor) backspace() {
	if e.cursor == 0 {
		return
	}
	e.runes = append(e.runes[:e.cursor-1], e.runes[e.cursor:]...)
	e.cursor--
}

// deleteForward deletes the rune AT the cursor (^D, Delete).
func (e *lineEditor) deleteForward() {
	if e.cursor >= len(e.runes) {
		return
	}
	e.runes = append(e.runes[:e.cursor], e.runes[e.cursor+1:]...)
}

// killToEnd is ^K.
func (e *lineEditor) killToEnd() { e.runes = e.runes[:e.cursor] }

// killToStart is ^U. readline's ^U kills to the start of the LINE, which for a
// one-line editor is the whole prefix.
func (e *lineEditor) killToStart() {
	e.runes = append(e.runes[:0], e.runes[e.cursor:]...)
	e.cursor = 0
}

// killWordBack is ^W: kill the whitespace-delimited word before the cursor,
// including the run of spaces that leads to it. readline's ^W is
// whitespace-delimited (unlike M-DEL, which is word-character-delimited), and
// that is the right one here: the tokens in a command line are shell words.
func (e *lineEditor) killWordBack() {
	i := e.cursor
	for i > 0 && e.runes[i-1] == ' ' {
		i--
	}
	for i > 0 && e.runes[i-1] != ' ' {
		i--
	}
	e.runes = append(e.runes[:i], e.runes[e.cursor:]...)
	e.cursor = i
}

// wordLeft / wordRight are M-b / M-f.
func (e *lineEditor) wordLeft() {
	i := e.cursor
	for i > 0 && e.runes[i-1] == ' ' {
		i--
	}
	for i > 0 && e.runes[i-1] != ' ' {
		i--
	}
	e.cursor = i
}

func (e *lineEditor) wordRight() {
	i := e.cursor
	for i < len(e.runes) && e.runes[i] == ' ' {
		i++
	}
	for i < len(e.runes) && e.runes[i] != ' ' {
		i++
	}
	e.cursor = i
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

// historyPrev is ^P / Up: one step toward older.
func (e *lineEditor) historyPrev() {
	if e.hindex == 0 || len(e.history) == 0 {
		return
	}
	if e.hindex == len(e.history) {
		e.stash = e.String() // keep what was being typed
	}
	e.hindex--
	e.set(e.history[e.hindex])
}

// historyNext is ^N / Down: one step toward newer, ending at what was stashed.
func (e *lineEditor) historyNext() {
	if e.hindex >= len(e.history) {
		return
	}
	e.hindex++
	if e.hindex == len(e.history) {
		e.set(e.stash)
		return
	}
	e.set(e.history[e.hindex])
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
