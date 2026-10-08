package cli

// THE TWO BOXES: one editor, two doors.
//
//	:send --id 9b7a -- a question      ← ':', a command line (the CLI's grammar)
//	> a question, pasted, on several   ← '>', a draft (the prompt's own bytes)
//	  lines, quotes and all
//
// They share the editor (lineedit.go), the emacs keymap (keymap.go), the
// completion menu (transcript_complete.go) and the drawer they draw in. What
// differs is four things, and they are all in this file: the sigil, what Enter
// does, where a submit goes, and what Tab asks for candidates.
//
// The draft exists because the command line cannot carry a prompt. `:send --
// <text>` is tokenized like a shell line, so a newline ends it and a quote
// changes what the words are; a prompt is NEITHER of those things. It is a
// block of the reader's bytes, and the only honest way to take one in a
// terminal is a box that does not parse.

import (
	"strings"

	"github.com/jack-work/figaro/internal/cmdkit"
)

type boxKind uint8

const (
	boxNone boxKind = iota
	boxCommand
	boxCompose
)

// boxSigil is the one indicator the drawer wears: ':' for the command line,
// '>' for a draft being typed, '✎' for one being moved around in. The mode is
// the prompt, so the drawer costs no row to say what it is.
func (t *transcript) boxSigil() string {
	if t.box == boxCompose {
		return t.composeSigil()
	}
	return ":"
}

// composeTitle is the drawer's heading, and it exists ONLY under M-m: the
// toggle that already means "spell it out" is where the words belong, and
// without it the sigil says everything a reader needs.
func (t *transcript) composeTitle() []string {
	if t.box != boxCompose || !t.verbose() {
		return nil
	}
	parts := []string{composeGlyph + " compose"}
	if t.composeMode == composeNormal {
		parts = []string{composeGlyph + " normal", "i insert"}
	}
	if n := strings.Count(t.compose.String(), "\n"); n > 0 {
		parts = append(parts, plural(n+1, "line"))
	}
	parts = append(parts, "M-⏎ send")
	return []string{pitGray(clipToWidth("  "+strings.Join(parts, " · "), t.w))}
}

func (t *transcript) boxOpen() bool { return t.box != boxNone }

// editor is the buffer the keys are editing. Every readline action goes
// through it, so a chord added to the keymap works in both boxes by
// construction rather than by being bound twice.
func (t *transcript) editor() *lineEditor {
	if t.box == boxCompose {
		return &t.compose
	}
	return &t.cmdline
}

// boxRows is how tall the box itself may grow before it scrolls under its own
// prompt. A command line is one thing you read in a glance; a draft is a
// paragraph, and a reader pasting one needs to see it.
func (t *transcript) boxRows() int {
	if t.box == boxCompose {
		return composeDrawerRows
	}
	return pickerRows
}

const composeDrawerRows = 12

// pagerComposeBox is '>': open the draft. The draft is NOT cleared, so a box
// put away with Esc comes back as it was.
func pagerComposeBox(t *transcript) {
	t.box, t.jumpNote = boxCompose, ""
	t.menu = nil
	// '>' IS THE KEY THAT TYPES, so it opens in insert mode however the drawer
	// was last left: a reader who pressed it means to write, not to navigate.
	t.composeMode = composeInsert
	t.composeDropMark()
	t.compose.end()
}

// pagerVisualCompose is '>' with a selection up: the draft opens holding the
// range placeholder, which the submit expands into the coordinate that quotes
// the passage. The highlight stays, so the reader can see what they are
// quoting while they type about it.
func pagerVisualCompose(t *transcript) {
	pagerComposeBox(t)
	if t.visual.highlighted() && !strings.HasPrefix(t.compose.String(), visualRangePlaceholder) {
		t.compose.home()
		t.compose.insert(visualRangePlaceholder + " ")
	}
}

// boxEnter is Enter: in the command line it runs the line; in a draft it is a
// NEWLINE, which is the whole reason the draft is a different box. A paste
// arrives as bytes with CRs in it, and a box whose Enter submits would send
// the first line of a paragraph and discard the rest.
func boxEnter(t *transcript) {
	if t.box == boxCompose {
		if t.acceptCompletion() {
			return
		}
		t.edit(func(e *lineEditor) { e.insertNewline() })
		return
	}
	t.boxSubmit(false)
}

// composeSubmit is Alt+Enter (and Ctrl+Enter): send what is in the draft, as
// its own bytes. The coordinate placeholder is expanded HERE, under the
// render lock, because it is read off the index and the selection and the
// next keystroke may move either.
func composeSubmit(t *transcript) {
	t.compose.endSearch()
	if t.acceptCompletion() {
		return
	}
	text := strings.TrimRight(t.compose.String(), "\n")
	if strings.TrimSpace(text) == "" {
		t.composeClose(true)
		return
	}
	expanded, note, err := t.expandComposeRange(text)
	if err != "" {
		t.noteOrClear(err)
		return
	}
	if t.sendCompose == nil {
		t.noteOrClear("sending needs a live session")
		return
	}
	t.compose.remember(composeFirstLine(text))
	t.composeClose(true)
	if note != "" {
		t.setCommandNoteAt(note, alertInfo)
	} else {
		t.jumpNote = ""
	}
	t.leaveVisual()
	pagerTail(t)
	t.sendCompose(expanded)
}

// expandComposeRange rewrites a leading `<,>` into the fully qualified
// coordinate the daemon resolves. A prompt quotes by BEGINNING with the
// token, so the expansion is a replacement of the first word and nothing
// else: there is no verb here to move it past.
func (t *transcript) expandComposeRange(text string) (string, string, string) {
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

// composeResume reopens the drawer a ':' line was taken out of, in the mode it
// was left in. composeHeld is what remembers there was one.
func (t *transcript) composeResume() {
	if !t.composeHeld {
		return
	}
	t.composeHeld = false
	t.box = boxCompose
}

// composeClose puts the drawer away. clear says whether the draft goes with it:
// Esc keeps it (a paste is expensive to lose), ^C and a send spend it.
func (t *transcript) composeClose(clear bool) {
	t.box = boxNone
	t.clearCompletions()
	if clear {
		t.compose.reset()
	}
}

func composeFirstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

// pasteText takes a bracketed paste. WITH NO BOX OPEN IT OPENS THE DRAFT:
// a pasted paragraph in a pager is a prompt being written, and the gesture
// that would otherwise be required ('>' first, then paste) is a step the
// reader has already decided on by pasting.
func (t *transcript) pasteText(text string) {
	if text == "" {
		return
	}
	if !t.boxOpen() {
		pagerComposeBox(t)
	}
	t.editor().endSearch()
	t.clearCompletions()
	t.editor().insertPasted(text, t.box == boxCompose)
	t.render()
}

// draftCompletions is Tab's pool in a draft: what belongs in a PROMPT, which
// is form references and paths, not verbs and flags. The command line's
// completer answers a different question and would offer `--id` where a
// reader is typing a sentence.
func (t *transcript) draftCompleter() func(string) []string {
	if t.composeCompleter == nil {
		return nil
	}
	return t.composeCompleter
}

// composeCandidates is the default pool, for a transcript with no session
// behind it: paths and form keys are both local questions, so a fixture and a
// live pager answer the same way.
func composeCandidates(line string) []string {
	return completePromptContext(&cmdkit.CompleteContext{Describe: true, Current: lastWord(line)})
}
