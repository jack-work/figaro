package cli

// The pit identity is the source of the keyMode; nothing else may derive one from
// the screen.

import (
	"strings"

	"github.com/jack-work/figaro/api/glyph"
)

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
	pitModels  pitID = "models"
	pitReaped  pitID = "reaped"
	pitTroupe  pitID = "troupe"
	pitConfirm pitID = "confirm"
	pitLogin   pitID = "login"
	pitAsk     pitID = "answer"
	pitSendTo  pitID = "send"
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
	pitQueue:         {glyph.PitQueue, "queue", glyph.MarkQueue, modePanel},
	pitNotifications: {glyph.PitNotifications, "notifications", glyph.MarkNotifications, modePanel},
	pitCommand:       {glyph.PitCommand, "command", "", modeBox},
	pitComposer:      {glyph.Composer, "composer", "", modeBox},
	pitSearch:        {glyph.PitSearch, "search", "", modeSearch},
	pitHelp:          {glyph.PitHelp, "help", "", modePanel},
	pitStatus:        {glyph.PitStatus, "status", "", modePanel},
	pitNote:          {"", "", "", modePanel},
	pitOutput:        {"", "", glyph.MarkQueue, modePanel},
	pitDropped:       {glyph.PitDropped, "dropped", glyph.MarkQueue, modePanel},
	// 웃 is East Asian Wide, not ambiguous: runewidth reports 2 everywhere.
	pitForm:       {glyph.PitFolk, "form", glyph.MarkForm, modePanel},
	pitModels:     {glyph.PitFolk, "models", glyph.MarkModels, modePanel},
	pitReaped:     {glyph.PitFolk, "reaped", glyph.MarkReaped, modePanel},
	pitTroupe:     {glyph.PitFolk, "troupe", glyph.MarkTroupe, modePanel},
	pitConfirm:    {glyph.PitConfirm, "confirm", "", modeConfirm},
	pitCompletion: {"", "completion", glyph.MarkCompletion, modeBox},
	pitDecision:   {glyph.PitDecision, "decision", "", modePanel},
	pitLogin:      {glyph.PitKey, "login", glyph.MarkLogin, modePanel},
	pitAsk:        {glyph.PitKey, "answer", "", modeBox},
	pitSendTo:     {glyph.Send, "send", "", modeBox},
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

var passivePits = map[pitID]bool{
	pitQueue: true,
}

func (d pitID) passive() bool { return passivePits[d] }

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
	return glyph.Select
}
