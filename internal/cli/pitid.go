package cli

// The pit identity is the source of the keyMode; nothing else may derive one from
// the screen.

import "strings"

type pitID string

const (
	pitNothing       pitID = ""
	pitQueue         pitID = "queue"
	pitNotifications pitID = "notifications"
	pitCommand       pitID = "command"
	pitComposer      pitID = "composer"
	pitSearch        pitID = "search"
	pitHelp          pitID = "help"
	pitStatus        pitID = "status"
	pitNote          pitID = "message"
	pitOutput        pitID = "output"
	pitDropped       pitID = "dropped"
	// "form listen", "form show", ...: matched by prefix in face().
	pitForm    pitID = "form"
	pitConfirm pitID = "confirm"
	// Drawn inside the command pit, never opened as its own; the row exists
	// for its selection marker.
	pitCompletion pitID = "completion"
)

type pitFace struct {
	glyph     string
	name      string
	selection string
	keys      keyMode
}

// Glyphs must not be ambiguous-width: the bar's layout would depend on the
// reader's terminal.
var pitFaces = map[pitID]pitFace{
	pitNothing:       {"", "", "", modeTranscript},
	pitQueue:         {"𝄚", "queue", "♭", modePanel},
	pitNotifications: {"𝄞", "notifications", "♩", modePanel},
	pitCommand:       {"∴", "command", "", modeBox},
	pitComposer:      {"✎", "composer", "", modeBox},
	pitSearch:        {"⌕", "search", "", modeSearch},
	pitHelp:          {"?", "help", "", modePanel},
	pitStatus:        {"!", "status", "", modePanel},
	pitNote:          {"", "", "", modePanel},
	pitOutput:        {"", "", "♭", modePanel},
	pitDropped:       {"𝄽", "dropped", "♭", modePanel},
	// 웃 is East Asian Wide, not ambiguous: runewidth reports 2 everywhere.
	pitForm:       {"웃", "form", "⌖", modePanel},
	pitConfirm:    {"[y/N]", "confirm", "", modeConfirm},
	pitCompletion: {"", "completion", "♪", modeBox},
}

func (d pitID) face() pitFace {
	if f, ok := pitFaces[d]; ok {
		return f
	}
	if i := strings.IndexByte(string(d), ' '); i > 0 {
		if f, ok := pitFaces[pitID(string(d)[:i])]; ok {
			return f
		}
	}
	return pitFace{keys: modePanel}
}

func (d pitID) keys() keyMode { return d.face().keys }

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

func (d pitID) selectionGlyph() string {
	if g := d.face().selection; g != "" {
		return g
	}
	return "▸"
}
