package cli

// The composer's normal mode: motions, marks and search over the draft; no editing
// verbs.

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

func composerNormalEnter(t *transcript) {
	t.composerMode = composerNormal
	t.composerDropMark()
	// The cursor sits on a rune in normal mode, so one parked past the end
	// steps back.
	if t.draft.cursor > 0 && t.draft.cursor == len(t.draft.runes) {
		t.draft.left()
	}
}

func composerInsertHere(t *transcript)  { t.composerToInsert(func(e *lineEditor) {}) }
func composerInsertAfter(t *transcript) { t.composerToInsert(func(e *lineEditor) { e.right() }) }
func composerInsertHome(t *transcript)  { t.composerToInsert(func(e *lineEditor) { e.home() }) }
func composerInsertEnd(t *transcript)   { t.composerToInsert(func(e *lineEditor) { e.end() }) }

func (t *transcript) composerToInsert(move func(*lineEditor)) {
	t.composerMode = composerInsert
	t.composerDropMark()
	move(&t.draft)
}

func (t *transcript) composerDropMark() { t.composerMark, t.composerKind = -1, visualNone }

func (t *transcript) composerHighlighted() bool {
	return t.composerKind != visualNone && t.composerMark >= 0
}

// A line-wise mark covers whole lines.
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

// The same kind again drops the mark; the other kind switches in place.
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

// With nothing marked 'y' copies the whole draft. The input loop owns the clipboard.
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

// dispatch arms pendG on the first g.
func composerTop(t *transcript) {
	if t.pendG {
		t.draft.bufferStart()
	}
}
func composerBottom(t *transcript)   { t.draft.bufferEnd() }
func composerParaNext(t *transcript) { t.draft.paragraph(1) }
func composerParaPrev(t *transcript) { t.draft.paragraph(-1) }

// t.full is shared with every pit.
func composerFullscreen(t *transcript) {
	t.full = !t.full
	t.focused = focusPit
}

// composerHeld brings the command line back here on close.
func composerCommand(t *transcript) {
	t.composerHeld = true
	t.box = boxCommand
	t.jumpNote = ""
	t.cmdline.reset()
	t.menu = nil
}

func composerEscape(t *transcript) {
	if t.composerHighlighted() {
		t.composerDropMark()
		return
	}
	t.composerClose(false)
}

// One search box; searchAccept decides by what is open.
func composerSearchFwd(t *transcript)  { pagerSearchPrompt(t) }
func composerSearchBack(t *transcript) { pagerSearchPromptBack(t) }

func composerFindNext(t *transcript) { t.composerFind(t.searchStep()) }
func composerFindPrev(t *transcript) { t.composerFind(-t.searchStep()) }

func (t *transcript) composerFind(dir int) {
	// The bar, not a note pit: a note would take the drawer's region.
	if t.matchQuery == "" {
		t.setCommandNoteAt("no search yet", alertError)
		return
	}
	if !t.draft.findRune(t.matchQuery, dir) {
		t.setCommandNoteAt("not in the draft: "+t.matchQuery, alertError)
	}
}
