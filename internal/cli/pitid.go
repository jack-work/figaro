package cli

// WHAT IS OPEN, and the one place that answers it. The pit identity is the
// source; the keyMode is derived from it, and nothing else in the program may
// compute one from what is on screen. See plans/status-bar-and-modes.md §2.

import "strings"

// pitID names a pit: a type rather than a string, so the table below can be
// exhaustive and a typo is a compile error.
type pitID string

const (
	pitNothing       pitID = ""
	pitQueue         pitID = "queue"
	pitNotifications pitID = "notifications"
	pitCommand       pitID = "command"
	pitSearch        pitID = "search"
	pitHelp          pitID = "help"
	pitStatus        pitID = "status"
	pitNote          pitID = "message"
	pitOutput        pitID = "output"
	// A dropped queue is its own pit: the queue is what is waiting, these are
	// what will never be answered.
	pitDropped pitID = "dropped"
	// Live form views arrive named "form listen", "form show" and so on: the
	// family is matched by prefix, see face().
	pitForm pitID = "form"
)

// pitFace is how a pit presents itself: the glyph on the bar, the word beside
// it under verbose, and the marker on its selected row.
type pitFace struct {
	glyph     string
	name      string
	selection string
	keys      keyMode
}

// pitFaces is THE table: every pit has a row, including the ones that
// contribute no token to the bar (an empty glyph). The glyphs are musical on
// purpose -- the queue is a staff, notifications a clef, a selected row a note
// -- and single-width on purpose, except where noted: an ambiguous-width glyph
// makes the bar's layout a property of the reader's terminal.
var pitFaces = map[pitID]pitFace{
	pitNothing:       {"", "", "", modeTranscript},
	pitQueue:         {"𝄚", "queue", "♭", modePanel},
	pitNotifications: {"𝄞", "notifications", "♩", modePanel},
	pitCommand:       {"∴", "command", "", modeJump},
	pitSearch:        {"⌕", "search", "", modeSearch},
	pitHelp:          {"?", "help", "", modePanel},
	pitStatus:        {"!", "status", "", modePanel},
	pitNote:          {"", "", "", modePanel},
	pitOutput:        {"", "", "♭", modePanel},
	pitDropped:       {"𝄽", "dropped", "♭", modePanel},
	// 웃 is the form itself: a figure, arms out -- the dummy an outfit is
	// folded onto. Wide (East Asian W, two cells) rather than ambiguous, so
	// runewidth reports 2 everywhere and the bar's arithmetic holds. Its
	// selected row is ⌖, the pin in the body rather than a note in a score.
	pitForm: {"웃", "form", "⌖", modePanel},
}

// face is the pit's presentation, or the empty face for one nobody has named:
// no token, modePanel, which is a safe thing for a new pit to do.
func (d pitID) face() pitFace {
	if f, ok := pitFaces[d]; ok {
		return f
	}
	// The live verbs are a family, named for the command that opened them, so
	// a new sub-verb does not arrive glyphless.
	if i := strings.IndexByte(string(d), ' '); i > 0 {
		if f, ok := pitFaces[pitID(string(d)[:i])]; ok {
			return f
		}
	}
	return pitFace{keys: modePanel}
}

// keys is the KEYBOARD half, derived. Nothing else in the program may compute
// a keyMode from what is on screen.
func (d pitID) keys() keyMode { return d.face().keys }

// token is what the pit contributes to the bar: the glyph alone, and the name
// only under `m`.
//
//	𝄚 · ✓ · 123abc · test        9.8k/1.0m (1.0%)   default
//	𝄚 queue · done ✓ · 123abc · test · …            with `m`
func (d pitID) token(verbose bool) string {
	f := d.face()
	switch {
	case f.glyph == "":
		return ""
	case verbose && f.name != "":
		return f.glyph + " " + f.name
	default:
		return f.glyph
	}
}

// selectionGlyph marks the row under the cursor, per pit: a queue row is a
// thing you are about to act on, a notification a thing you are reading.
func (d pitID) selectionGlyph() string {
	if g := d.face().selection; g != "" {
		return g
	}
	return "▸"
}
