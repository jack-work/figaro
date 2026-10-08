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
	"fmt"
	"strings"

	"github.com/jack-work/figaro/internal/cmdkit"
)

type boxKind uint8

const (
	boxNone boxKind = iota
	boxCommand
	boxPrompt
)

func (k boxKind) sigil() string {
	if k == boxPrompt {
		return "> "
	}
	return ":"
}

func (t *transcript) boxOpen() bool { return t.box != boxNone }

// editor is the buffer the keys are editing. Every readline action goes
// through it, so a chord added to the keymap works in both boxes by
// construction rather than by being bound twice.
func (t *transcript) editor() *lineEditor {
	if t.box == boxPrompt {
		return &t.draft
	}
	return &t.cmdline
}

// boxRows is how tall the box itself may grow before it scrolls under its own
// prompt. A command line is one thing you read in a glance; a draft is a
// paragraph, and a reader pasting one needs to see it.
func (t *transcript) boxRows() int {
	if t.box == boxPrompt {
		return draftRows
	}
	return pickerRows
}

const draftRows = 12

// pagerPromptBox is '>': open the draft. The draft is NOT cleared, so a box
// put away with Esc comes back as it was.
func pagerPromptBox(t *transcript) {
	t.box, t.jumpNote = boxPrompt, ""
	t.menu = nil
	t.draft.end()
}

// pagerVisualPrompt is '>' with a selection up: the draft opens holding the
// range placeholder, which the submit expands into the coordinate that quotes
// the passage. The highlight stays, so the reader can see what they are
// quoting while they type about it.
func pagerVisualPrompt(t *transcript) {
	pagerPromptBox(t)
	if t.visual.highlighted() && !strings.HasPrefix(t.draft.String(), visualRangePlaceholder) {
		t.draft.home()
		t.draft.insert(visualRangePlaceholder + " ")
	}
}

// boxEnter is Enter: in the command line it runs the line; in a draft it is a
// NEWLINE, which is the whole reason the draft is a different box. A paste
// arrives as bytes with CRs in it, and a box whose Enter submits would send
// the first line of a paragraph and discard the rest.
func boxEnter(t *transcript) {
	if t.box == boxPrompt {
		if t.acceptCompletion() {
			return
		}
		t.edit(func(e *lineEditor) { e.insertNewline() })
		return
	}
	t.boxSubmit(false)
}

// draftSubmit is Alt+Enter (and Ctrl+Enter): send what is in the draft, as
// its own bytes. The coordinate placeholder is expanded HERE, under the
// render lock, because it is read off the index and the selection and the
// next keystroke may move either.
func draftSubmit(t *transcript) {
	t.draft.endSearch()
	if t.acceptCompletion() {
		return
	}
	text := strings.TrimRight(t.draft.String(), "\n")
	if strings.TrimSpace(text) == "" {
		t.draftClose(true)
		return
	}
	expanded, note, err := t.expandDraftRange(text)
	if err != "" {
		t.noteOrClear(err)
		return
	}
	if t.sendDraft == nil {
		t.noteOrClear("sending needs a live session")
		return
	}
	t.draft.remember(draftFirstLine(text))
	t.draftClose(true)
	if note != "" {
		t.setCommandNoteAt(note, alertInfo)
	} else {
		t.jumpNote = ""
	}
	t.leaveVisual()
	pagerTail(t)
	t.sendDraft(expanded)
}

// expandDraftRange rewrites a leading `<,>` into the fully qualified
// coordinate the daemon resolves. A prompt quotes by BEGINNING with the
// token, so the expansion is a replacement of the first word and nothing
// else: there is no verb here to move it past.
func (t *transcript) expandDraftRange(text string) (string, string, string) {
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

// draftClose puts the drawer away. clear says whether the draft goes with it:
// Esc keeps it (a paste is expensive to lose), ^C and a send spend it.
func (t *transcript) draftClose(clear bool) {
	t.box = boxNone
	t.clearCompletions()
	if clear {
		t.draft.reset()
	}
}

func draftFirstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

// draftHint is the one row of chrome the draft wears: what sends it, and how
// much of it there is once there is enough to lose track of.
func draftHint(e *lineEditor) string {
	hint := "M-Enter sends · Enter is a newline · Esc keeps it"
	if n := strings.Count(e.String(), "\n"); n > 0 {
		hint = fmt.Sprintf("%d lines · %s", n+1, hint)
	}
	return hint
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
		pagerPromptBox(t)
	}
	t.editor().endSearch()
	t.clearCompletions()
	t.editor().insertPasted(text, t.box == boxPrompt)
	t.render()
}

// draftCompletions is Tab's pool in a draft: what belongs in a PROMPT, which
// is form references and paths, not verbs and flags. The command line's
// completer answers a different question and would offer `--id` where a
// reader is typing a sentence.
func (t *transcript) draftCompleter() func(string) []string {
	if t.promptCompleter == nil {
		return nil
	}
	return t.promptCompleter
}

// promptCandidates is the default pool, for a transcript with no session
// behind it: paths and form keys are both local questions, so a fixture and a
// live pager answer the same way.
func promptCandidates(line string) []string {
	return completePromptContext(&cmdkit.CompleteContext{Describe: true, Current: lastWord(line)})
}
