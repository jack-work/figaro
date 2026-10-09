package cli

// THE COMPOSER'S NORMAL MODE: a minimal vim inside the pit.
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

type composerKeys uint8

const (
	composerInsert composerKeys = iota
	composerNormal
)

func (t *transcript) composerSigil() string {
	if t.composerMode == composerNormal {
		return composerNormalSigil + " "
	}
	return "> "
}

const (
	composerNormalSigil = "✎"
	composerGlyph       = "✎"
)

// composerNormalEnter is Esc in insert mode: the draft stays, the keys change.
// Esc again closes the drawer (and keeps the draft), which is the ladder the
// box has always had, one rung longer.
func composerNormalEnter(t *transcript) {
	t.composerMode = composerNormal
	t.composerDropMark()
	// The cursor sits ON a rune in normal mode, as vim's does, so a cursor
	// parked past the end of the line steps back onto it.
	if t.draft.cursor > 0 && t.draft.cursor == len(t.draft.runes) {
		t.draft.left()
	}
}

// composeInsertAt is i / a / I / A: back to the box that types.
func composerInsertHere(t *transcript)  { t.composerToInsert(func(e *lineEditor) {}) }
func composerInsertAfter(t *transcript) { t.composerToInsert(func(e *lineEditor) { e.right() }) }
func composerInsertHome(t *transcript)  { t.composerToInsert(func(e *lineEditor) { e.home() }) }
func composerInsertEnd(t *transcript)   { t.composerToInsert(func(e *lineEditor) { e.end() }) }

func (t *transcript) composerToInsert(move func(*lineEditor)) {
	t.composerMode = composerInsert
	t.composerDropMark()
	move(&t.draft)
}

// composerDropMark drops the highlight and leaves the cursor.
func (t *transcript) composerDropMark() { t.composerMark, t.composerKind = -1, visualNone }

func (t *transcript) composerHighlighted() bool {
	return t.composerKind != visualNone && t.composerMark >= 0
}

// composerSpan is the marked range as rune indices, [lo, hi), or false.
// A LINE-WISE mark covers whole lines, which is what V means everywhere else.
func (t *transcript) composerSpan() (int, int, bool) {
	if !t.composerHighlighted() {
		return 0, 0, false
	}
	lo, hi := t.composerMark, t.draft.cursor
	if lo > hi {
		lo, hi = hi, lo
	}
	if t.composerKind == visualLine {
		return t.draft.lineStart(lo), min(t.draft.lineEnd(hi)+1, len(t.draft.runes)), true
	}
	return lo, min(hi+1, len(t.draft.runes)), true
}

// composerMarkChar and composerMarkLine are v and V: press to mark from the
// cursor, press again to drop it. The kind switches in place, as it does in
// the transcript.
func composerMarkChar(t *transcript) { t.composerPressMark(visualChar) }
func composerMarkLine(t *transcript) { t.composerPressMark(visualLine) }

func (t *transcript) composerPressMark(kind visualKind) {
	switch {
	case t.composerKind == kind:
		t.composerDropMark()
	case t.composerHighlighted():
		t.composerKind = kind
	default:
		t.composerMark, t.composerKind = t.draft.cursor, kind
	}
}

// composerYankText is what 'y' copies: the marked text, or the whole draft
// when nothing is marked, which is the useful default for a box whose
// contents are the thing you came to copy. The clipboard belongs to the input
// loop (see inputYank), so this answers and does not act.
func (t *transcript) composerYankText() (string, bool) {
	text := t.draft.String()
	if lo, hi, ok := t.composerSpan(); ok {
		text = t.draft.selectionText(lo, hi)
	}
	if strings.TrimSpace(text) == "" {
		return "", false
	}
	t.composerDropMark()
	return text, true
}

// The motions. Each is one of the editor's, because the editor is what holds
// the cursor; the names are vim's, because that is what the fingers know.
func composerLeft(t *transcript)      { t.draft.left() }
func composerRight(t *transcript)     { t.draft.right() }
func composerUpLine(t *transcript)    { t.draft.lineUp() }
func composerDownLine(t *transcript)  { t.draft.lineDown() }
func composerWordFwd(t *transcript)   { t.draft.wordRight() }
func composerWordBack(t *transcript)  { t.draft.wordLeft() }
func composerWordEnd(t *transcript)   { t.draft.wordRight() }
func composerLineStart(t *transcript) { t.draft.home() }
func composerLineEnd(t *transcript)   { t.draft.end() }
func composerFirstText(t *transcript) { t.draft.firstNonBlank() }

// composerTop is the second g of gg: dispatch arms the first, exactly as it
// does for the transcript's own gg.
func composerTop(t *transcript) {
	if t.pendG {
		t.draft.bufferStart()
	}
}
func composerBottom(t *transcript)   { t.draft.bufferEnd() }
func composerParaNext(t *transcript) { t.draft.paragraph(1) }
func composerParaPrev(t *transcript) { t.draft.paragraph(-1) }

// composerFullscreen is F: the drawer takes the pane. One disposition, shared
// with every pit (transcript.full), so opening a form after composing does not
// quietly change how much screen a reader gets.
func composerFullscreen(t *transcript) {
	t.full = !t.full
	t.focused = focusPit
}

// composerCommand is ':' from normal mode: the command line, over the drawer.
// It behaves exactly as it does anywhere else; what is new is that closing it
// comes back here instead of to the transcript.
func composerCommand(t *transcript) {
	t.composerHeld = true
	t.box = boxCommand
	t.jumpNote = ""
	t.cmdline.reset()
	t.menu = nil
}

// composerEscape is Esc in normal mode: it drops a highlight if there is one,
// then puts the drawer away, keeping the draft.
func composerEscape(t *transcript) {
	if t.composerHighlighted() {
		t.composerDropMark()
		return
	}
	t.composerClose(false)
}

// composeSearch is / and ?: the search box, aimed at the draft. The box is
// the pager's own (one search box, one query); what differs is what accepts
// it, which searchAccept decides by what is open.
func composerSearchFwd(t *transcript)  { pagerSearchPrompt(t) }
func composerSearchBack(t *transcript) { pagerSearchPromptBack(t) }

// composerFindNext and composerFindPrev are n and N over the draft.
func composerFindNext(t *transcript) { t.composerFind(t.searchStep()) }
func composerFindPrev(t *transcript) { t.composerFind(-t.searchStep()) }

func (t *transcript) composerFind(dir int) {
	// THE BAR, NOT A NOTE PIT: a note would take the region the drawer is in.
	if t.matchQuery == "" {
		t.setCommandNoteAt("no search yet", alertError)
		return
	}
	if !t.draft.findRune(t.matchQuery, dir) {
		t.setCommandNoteAt("not in the draft: "+t.matchQuery, alertError)
	}
}
