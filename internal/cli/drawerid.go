package cli

// WHAT IS OPEN, and the one place that answers it.
//
// The bottom of the pager has two questions about the same fact: what does the
// KEYBOARD do (a keyMode, see keymap.go), and what does the READER see (a
// glyph and a name in the status bar). Both used to be derived, separately,
// from `inSearch`/`inJump`/`drawer.kind` — three booleans consulted by four
// files, which for one frame could disagree.
//
// They are different axes and they stay two types, because `help`, `status` and
// `queue` are three different things on screen while sharing modePanel. But
// they are NOT independent: the drawer identity is the SOURCE and the keyMode
// is DERIVED from it. That is what makes "one owner" true rather than
// aspirational. See plans/status-bar-and-modes.md §2.

import "strings"

// drawerID names a drawer. It is the string the drawer already carried —
// `showList("queue", …)` — promoted to a type so the table below can be
// exhaustive and a typo is a compile error rather than a silently nameless
// drawer.
type drawerID string

const (
	drawerNothing       drawerID = ""
	drawerQueue         drawerID = "queue"
	drawerNotifications drawerID = "notifications"
	drawerCommand       drawerID = "command"
	drawerSearch        drawerID = "search"
	drawerHelp          drawerID = "help"
	drawerStatus        drawerID = "status"
	drawerNote          drawerID = "message"
	drawerOutput        drawerID = "output"
	// A FORM IS ITS OWN THING, and it must not borrow the notification clef:
	// they are different in kind (a form is state you are reading, a
	// notification is an event that happened to you) and reading a bass clef
	// where you expected a treble is worse than having no glyph at all. Live
	// form views arrive named "form listen", "form show" and so on, so the
	// family is matched by prefix -- see face().
	drawerForm drawerID = "form"
)

// drawerFace is how a drawer presents itself: the glyph that leads the status
// bar, the word beside it under verbose, and the glyph marking its selected
// row. An empty glyph is not an oversight — see the table.
type drawerFace struct {
	glyph     string
	name      string
	selection string
	keys      keyMode
}

// drawerFaces is THE table. Every drawer has a row, including the ones with
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
var drawerFaces = map[drawerID]drawerFace{
	drawerNothing:       {"", "", "", modeTranscript},
	drawerQueue:         {"𝄚", "queue", "♭", modePanel},
	drawerNotifications: {"𝄞", "notifications", "♩", modePanel},
	drawerCommand:       {"∴", "command", "", modeJump},
	drawerSearch:        {"⌕", "search", "", modeSearch},
	drawerHelp:          {"?", "help", "", modePanel},
	drawerStatus:        {"!", "status", "", modePanel},
	drawerNote:          {"", "", "", modePanel},
	drawerOutput:        {"", "", "♭", modePanel},
	// 𝄢 is the bass clef: the ground a piece is written over, which is what a
	// form is to an aria. ♮ (natural) marks its selected row -- a third
	// selection glyph, distinct from the queue's ♭ and notifications' ♩,
	// because a reader should be able to tell which list they are in from the
	// row under the cursor alone.
	drawerForm: {"𝄢", "form", "♮", modePanel},
}

// face is the drawer's presentation, or the empty face for one nobody has
// named. A missing row is deliberately not a panic: an unnamed drawer draws no
// token and takes modePanel, which is what the pager did before this table
// existed and is a safe thing for a new drawer to do before it is described.
func (d drawerID) face() drawerFace {
	if f, ok := drawerFaces[d]; ok {
		return f
	}
	// THE LIVE VERBS ARE A FAMILY, not a fixed set: they are named for the
	// command that opened them ("form listen", "form show", "state show"), so
	// matching the family by prefix is what keeps a new sub-verb from arriving
	// glyphless.
	if i := strings.IndexByte(string(d), ' '); i > 0 {
		if f, ok := drawerFaces[drawerID(string(d)[:i])]; ok {
			return f
		}
	}
	return drawerFace{keys: modePanel}
}

// keys is the KEYBOARD half, derived. Nothing else in the program may compute
// a keyMode from what is on screen.
func (d drawerID) keys() keyMode { return d.face().keys }

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
func (d drawerID) token(verbose bool) string {
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

// selectionGlyph marks the row under the cursor. It is PER DRAWER, not one
// global marker: the spec gives the queue a flat and notifications a quarter
// note, and they are read in different postures — a queue row is a thing you
// are about to act on, a notification is a thing you are reading.
func (d drawerID) selectionGlyph() string {
	if g := d.face().selection; g != "" {
		return g
	}
	return "▸"
}
