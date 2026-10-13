package cli

// The ':' line is a command box; the '>' drawer is the composer, a draft that is
// not parsed: `:send -- <text>` is tokenized like a shell line, so a newline ends
// it and a quote changes the words.

import (
	"strings"

	"github.com/jack-work/figaro/internal/cmdkit"
)

type boxKind uint8

const (
	boxNone boxKind = iota
	boxCommand
	boxComposer
)

func (t *transcript) boxSigil() string {
	if t.box == boxComposer {
		return t.composerSigil()
	}
	return ":"
}

// Only under M-m.
func (t *transcript) composerTitle() []string {
	if t.box != boxComposer || !t.verbose() {
		return nil
	}
	parts := []string{composerGlyph + " composer"}
	if t.composerMode == composerNormal {
		parts = []string{composerGlyph + " normal", "i insert"}
	}
	if n := strings.Count(t.draft.String(), "\n"); n > 0 {
		parts = append(parts, plural(n+1, "line"))
	}
	parts = append(parts, "^S send")
	return []string{pitGray(clipToWidth("  "+strings.Join(parts, " · "), t.w))}
}

func (t *transcript) boxOpen() bool { return t.box != boxNone }

// Every readline action goes through editor, so a chord works in both boxes by
// construction.
func (t *transcript) editor() *lineEditor {
	if t.box == boxComposer {
		return &t.draft
	}
	return &t.cmdline
}

func (t *transcript) boxRows() int {
	if t.box == boxComposer {
		return composerDrawerRows
	}
	return pickerRows
}

const composerDrawerRows = 12

// The draft is not cleared: a box put away with Esc comes back as it was.
func pagerComposerBox(t *transcript) {
	t.box, t.jumpNote = boxComposer, ""
	t.menu = nil
	// '>' opens in insert mode however the drawer was last left.
	t.composerMode = composerInsert
	t.composerDropMark()
	t.draft.end()
}

// The draft opens holding the range placeholder; the submit expands it. The
// highlight stays.
func pagerVisualComposer(t *transcript) {
	pagerComposerBox(t)
	if t.visual.highlighted() && !strings.HasPrefix(t.draft.String(), visualRangePlaceholder) {
		t.draft.home()
		t.draft.insert(visualRangePlaceholder + " ")
	}
}

// In a draft Enter is a newline: a paste arrives with CRs, and a submitting Enter
// would send the first line and drop the rest.
func boxEnter(t *transcript) {
	if t.box == boxComposer {
		if t.acceptCompletion() {
			return
		}
		t.edit(func(e *lineEditor) { e.insertNewline() })
		return
	}
	t.boxSubmit(false)
}

// The placeholder is expanded here, under the render lock: it reads the index and
// the selection, and the next keystroke may move either.
func composerSubmit(t *transcript) {
	t.draft.endSearch()
	if t.acceptCompletion() {
		return
	}
	text := strings.TrimRight(t.draft.String(), "\n")
	if strings.TrimSpace(text) == "" {
		t.composerClose(true)
		return
	}
	expanded, note, err := t.expandComposerRange(text)
	if err != "" {
		// The bar, not a note: a note is a pit, and the composer outranks
		// it (see openPit).
		t.setCommandNoteAt(err, alertError)
		return
	}
	if t.sendComposer == nil {
		t.setCommandNoteAt("sending needs a live session", alertError)
		return
	}
	t.draft.remember(composerFirstLine(text))
	t.composerClose(true)
	if note != "" {
		t.setCommandNoteAt(note, alertInfo)
	} else {
		t.jumpNote = ""
	}
	t.leaveVisual()
	pagerTail(t)
	t.sendComposer(expanded)
}

// A prompt quotes by beginning with the token: only the first word is replaced.
func (t *transcript) expandComposerRange(text string) (string, string, string) {
	if !strings.HasPrefix(text, visualRangePlaceholder) {
		return text, "", ""
	}
	if !t.visual.highlighted() {
		return "", "", "no highlight: the range was dropped, or never made (v / V at the cursor)"
	}
	token, note, err := t.visualCoordinate()
	if err != "" {
		return "", "", "range: " + err
	}
	rest := strings.TrimLeft(text[len(visualRangePlaceholder):], " \t")
	if rest == "" {
		return token, note, ""
	}
	return token + " " + rest, note, ""
}

func (t *transcript) composerResume() {
	if !t.composerHeld {
		return
	}
	t.composerHeld = false
	t.box = boxComposer
}

// Esc keeps the draft (a paste is expensive to lose); ^C and a send spend it.
func (t *transcript) composerClose(clear bool) {
	t.box = boxNone
	t.clearCompletions()
	if clear {
		t.draft.reset()
	}
}

func composerFirstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

// With no box open a paste opens the draft.
func (t *transcript) pasteText(text string) {
	if text == "" {
		return
	}
	if !t.boxOpen() {
		pagerComposerBox(t)
	}
	t.editor().endSearch()
	t.clearCompletions()
	t.editor().insertPasted(text, t.box == boxComposer)
	t.render()
}

// Tab in a draft offers form references and paths, not verbs and flags.
func (t *transcript) draftCompleter() func(string) []string {
	if t.composerCompleter == nil {
		return nil
	}
	return t.composerCompleter
}

// Default pool: paths and form keys are local questions, so a fixture answers like
// a live pager.
func composerCandidates(line string) []string {
	return completePromptContext(&cmdkit.CompleteContext{Describe: true, Current: lastWord(line)})
}
