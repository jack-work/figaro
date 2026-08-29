package cli

// WHAT IS OPEN, and the one place that answers it.
//
// The bottom of the pager has two questions about the same fact: what does the
// KEYBOARD do (a keyMode, see keymap.go), and what does the READER see (a
// glyph and a name in the status bar). Both used to be derived, separately,
// from `inSearch`/`inJump`/`pit.kind` — three booleans consulted by four
// files, which for one frame could disagree.
//
// They are different axes and they stay two types, because `help`, `status` and
// `queue` are three different things on screen while sharing modePanel. But
// they are NOT independent: the pit identity is the SOURCE and the keyMode
// is DERIVED from it. That is what makes "one owner" true rather than
// aspirational. See plans/status-bar-and-modes.md §2.

import "strings"

// pitID names a pit. It is the string the pit already carried —
// `showList("queue", …)` — promoted to a type so the table below can be
// exhaustive and a typo is a compile error rather than a silently nameless
// pit.
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
	// A DROPPED QUEUE IS ITS OWN PIT, not the queue: the queue is what is
	// waiting, and these are what will never be answered. It keeps the queue's
	// flat, because the rows are the same kind of thing and `y` still yanks a
	// message's text; 𝄽 -- a rest -- is what a dropped message now is.
	pitDropped pitID = "dropped"
	// A FORM IS ITS OWN THING, and it must not borrow the notification clef:
	// they are different in kind (a form is state you are reading, a
	// notification is an event that happened to you) and reading a bass clef
	// where you expected a treble is worse than having no glyph at all. Live
	// form views arrive named "form listen", "form show" and so on, so the
	// family is matched by prefix -- see face().
	pitForm pitID = "form"
)

// pitFace is how a pit presents itself: the glyph that leads the status
// bar, the word beside it under verbose, and the glyph marking its selected
// row. An empty glyph is not an oversight — see the table.
type pitFace struct {
	glyph     string
	name      string
	selection string
	keys      keyMode
}

// pitFaces is THE table. Every pit has a row, including the ones with
// nothing to show: if this is the source of `keys()`, then a message and a
// hosted live view are identities too. They simply contribute no token to the
// bar, which is what an empty glyph means.
//
// The glyphs are Gluck's, and they are musical on purpose: the queue is a
// staff (𝄚), notifications are a clef (𝄞), a selected row is a note (♭ / ♩).
// Search is ⌕ (U+2315) and not 🔍 — the bar is width-sensitive and every other
// glyph here is single-width, while the emoji magnifier is double-width and
// font-dependent, which would make the bar's layout a property of the reader's
// terminal rather than of the program.
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
	// 웃 IS THE FORM ITSELF: a figure, seen from the front, arms out. Gluck --
	// "a form is meant to be like a dummy, like a form onto which a costumer
	// might layer outfits and such, or an artist might study" -- and that is
	// exactly what an aria's form is: the body the outfits are folded onto.
	// The bass clef 𝄢 was a pun on "the ground a piece is written over" and it
	// asked the reader to make the same pun to read the bar.
	//
	// It is a WIDE glyph (East Asian W, two cells) where every other token
	// here is one, and that is allowed for the reason the emoji magnifier was
	// not: W is unambiguous. runewidth reports 2 on every terminal, so the
	// bar's arithmetic is right everywhere; ambiguous-width glyphs are the
	// ones whose layout is a property of the reader's settings.
	//
	// Its SELECTED ROW is not musical, and that is
	// deliberate: ♮ read as a stray accidental sitting in the text rather than
	// as a cursor, and a form is not a phrase -- Gluck: "a form is meant to be
	// like a dummy, like a form onto which a costumer might layer outfits and
	// such, or an artist might study". ⌖ (U+2316, POSITION INDICATOR) is where
	// the pin goes in: the mark on the body, not a note in the score. It is
	// single-width and sits in the same block as search's ⌕, which is the
	// other glyph here chosen for the same reason.
	pitForm: {"웃", "form", "⌖", modePanel},
}

// face is the pit's presentation, or the empty face for one nobody has
// named. A missing row is deliberately not a panic: an unnamed pit draws no
// token and takes modePanel, which is what the pager did before this table
// existed and is a safe thing for a new pit to do before it is described.
func (d pitID) face() pitFace {
	if f, ok := pitFaces[d]; ok {
		return f
	}
	// THE LIVE VERBS ARE A FAMILY, not a fixed set: they are named for the
	// command that opened them ("form listen", "form show", "state show"), so
	// matching the family by prefix is what keeps a new sub-verb from arriving
	// glyphless.
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

// token is what the pit contributes to the left of the status bar: THE GLYPH
// ALONE, and the name only under `m`.
//
//	𝄚 · ✓ · 123abc · test                            9.8k/1.0m (1.0%)   (default)
//	𝄚 queue · done ✓ · 123abc · test · …             (with `m`)
//
// This bar is read by someone who knows the glyphs. Verbose is for the reader
// who does not, and spelling every symbol out by default spends the row's
// width teaching a lesson its only reader has already had. (I had this right,
// then "corrected" it against a sketch that was showing the verbose form.)
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

// selectionGlyph marks the row under the cursor. It is PER PIT, not one
// global marker: the spec gives the queue a flat and notifications a quarter
// note, and they are read in different postures — a queue row is a thing you
// are about to act on, a notification is a thing you are reading.
func (d pitID) selectionGlyph() string {
	if g := d.face().selection; g != "" {
		return g
	}
	return "▸"
}
