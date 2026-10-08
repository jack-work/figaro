package cli

// THE COMPOSE PIT'S NORMAL MODE: a minimal vim inside the drawer.
//
//	✎ what did you mean here?        ← normal: motions, v, /, :, F
//	> what did you mean here?        ← insert: readline, as before
//
// The two sigils are the whole indicator. Insert mode is the box that has
// always been there (emacs bindings, because it is a readline box); normal
// mode is the transcript's own vocabulary pointed at the draft: hjkl, w b e,
// 0 ^ $, gg G, { }, v/V to mark, y to copy, / ? n N to search, F to take the
// pane, ':' for the command line, i/a/I/A back to insert.
//
// NO EDITING VERBS. No d, c, x, p, r. A draft is written in insert mode; this
// mode exists to move around one, mark part of it and look at it, which is
// what a reader of a long pasted prompt actually needs. The absence is
// deliberate, not a stage: every one of those verbs would need an undo story
// the kill ring already tells in insert mode.

import "strings"

type composeKeys uint8

const (
	composeInsert composeKeys = iota
	composeNormal
)

func (t *transcript) composeSigil() string {
	if t.composeMode == composeNormal {
		return composeNormalSigil + " "
	}
	return "> "
}

const (
	composeNormalSigil = "✎"
	composeGlyph       = "✎"
)

// composeNormalEnter is Esc in insert mode: the draft stays, the keys change.
// Esc again closes the drawer (and keeps the draft), which is the ladder the
// box has always had, one rung longer.
func composeNormalEnter(t *transcript) {
	t.composeMode = composeNormal
	t.composeDropMark()
	// The cursor sits ON a rune in normal mode, as vim's does, so a cursor
	// parked past the end of the line steps back onto it.
	if t.compose.cursor > 0 && t.compose.cursor == len(t.compose.runes) {
		t.compose.left()
	}
}

// composeInsertAt is i / a / I / A: back to the box that types.
func composeInsertHere(t *transcript)  { t.composeToInsert(func(e *lineEditor) {}) }
func composeInsertAfter(t *transcript) { t.composeToInsert(func(e *lineEditor) { e.right() }) }
func composeInsertHome(t *transcript)  { t.composeToInsert(func(e *lineEditor) { e.home() }) }
func composeInsertEnd(t *transcript)   { t.composeToInsert(func(e *lineEditor) { e.end() }) }

func (t *transcript) composeToInsert(move func(*lineEditor)) {
	t.composeMode = composeInsert
	t.composeDropMark()
	move(&t.compose)
}

// composeDropMark drops the highlight and leaves the cursor.
func (t *transcript) composeDropMark() { t.composeMark, t.composeKind = -1, visualNone }

func (t *transcript) composeHighlighted() bool {
	return t.composeKind != visualNone && t.composeMark >= 0
}

// composeSpan is the marked range as rune indices, [lo, hi), or false.
// A LINE-WISE mark covers whole lines, which is what V means everywhere else.
func (t *transcript) composeSpan() (int, int, bool) {
	if !t.composeHighlighted() {
		return 0, 0, false
	}
	lo, hi := t.composeMark, t.compose.cursor
	if lo > hi {
		lo, hi = hi, lo
	}
	if t.composeKind == visualLine {
		return t.compose.lineStart(lo), min(t.compose.lineEnd(hi)+1, len(t.compose.runes)), true
	}
	return lo, min(hi+1, len(t.compose.runes)), true
}

// composeMarkChar and composeMarkLine are v and V: press to mark from the
// cursor, press again to drop it. The kind switches in place, as it does in
// the transcript.
func composeMarkChar(t *transcript) { t.composePressMark(visualChar) }
func composeMarkLine(t *transcript) { t.composePressMark(visualLine) }

func (t *transcript) composePressMark(kind visualKind) {
	switch {
	case t.composeKind == kind:
		t.composeDropMark()
	case t.composeHighlighted():
		t.composeKind = kind
	default:
		t.composeMark, t.composeKind = t.compose.cursor, kind
	}
}

// composeYankText is what 'y' copies: the marked text, or the whole draft
// when nothing is marked, which is the useful default for a box whose
// contents are the thing you came to copy. The clipboard belongs to the input
// loop (see inputYank), so this answers and does not act.
func (t *transcript) composeYankText() (string, bool) {
	text := t.compose.String()
	if lo, hi, ok := t.composeSpan(); ok {
		text = t.compose.selectionText(lo, hi)
	}
	if strings.TrimSpace(text) == "" {
		return "", false
	}
	t.composeDropMark()
	return text, true
}

// The motions. Each is one of the editor's, because the editor is what holds
// the cursor; the names are vim's, because that is what the fingers know.
func composeLeft(t *transcript)      { t.compose.left() }
func composeRight(t *transcript)     { t.compose.right() }
func composeUpLine(t *transcript)    { t.compose.lineUp() }
func composeDownLine(t *transcript)  { t.compose.lineDown() }
func composeWordFwd(t *transcript)   { t.compose.wordRight() }
func composeWordBack(t *transcript)  { t.compose.wordLeft() }
func composeWordEnd(t *transcript)   { t.compose.wordRight() }
func composeLineStart(t *transcript) { t.compose.home() }
func composeLineEnd(t *transcript)   { t.compose.end() }
func composeFirstText(t *transcript) { t.compose.firstNonBlank() }

// composeTop is the second g of gg: dispatch arms the first, exactly as it
// does for the transcript's own gg.
func composeTop(t *transcript) {
	if t.pendG {
		t.compose.bufferStart()
	}
}
func composeBottom(t *transcript)   { t.compose.bufferEnd() }
func composeParaNext(t *transcript) { t.compose.paragraph(1) }
func composeParaPrev(t *transcript) { t.compose.paragraph(-1) }

// composeFullscreen is F: the drawer takes the pane. One disposition, shared
// with every pit (transcript.full), so opening a form after composing does not
// quietly change how much screen a reader gets.
func composeFullscreen(t *transcript) {
	t.full = !t.full
	t.focused = focusPit
}

// composeCommand is ':' from normal mode: the command line, over the drawer.
// It behaves exactly as it does anywhere else; what is new is that closing it
// comes back here instead of to the transcript.
func composeCommand(t *transcript) {
	t.composeHeld = true
	t.box = boxCommand
	t.jumpNote = ""
	t.cmdline.reset()
	t.menu = nil
}

// composeEscape is Esc in normal mode: it drops a highlight if there is one,
// then puts the drawer away, keeping the draft.
func composeEscape(t *transcript) {
	if t.composeHighlighted() {
		t.composeDropMark()
		return
	}
	t.composeClose(false)
}

// composeSearch is / and ?: the search box, aimed at the draft. The box is
// the pager's own (one search box, one query); what differs is what accepts
// it, which searchAccept decides by what is open.
func composeSearchFwd(t *transcript)  { pagerSearchPrompt(t) }
func composeSearchBack(t *transcript) { pagerSearchPromptBack(t) }

// composeFindNext and composeFindPrev are n and N over the draft.
func composeFindNext(t *transcript) { t.composeFind(t.searchStep()) }
func composeFindPrev(t *transcript) { t.composeFind(-t.searchStep()) }

func (t *transcript) composeFind(dir int) {
	// THE BAR, NOT A NOTE PIT: a note would take the region the drawer is in.
	if t.matchQuery == "" {
		t.setCommandNoteAt("no search yet", alertError)
		return
	}
	if !t.compose.findRune(t.matchQuery, dir) {
		t.setCommandNoteAt("not in the draft: "+t.matchQuery, alertError)
	}
}
