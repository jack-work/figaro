package cli

import (
	"strings"
	"testing"
	"time"

	"github.com/jack-work/figaro/api/livedoc"
	"github.com/jack-work/figaro/internal/livelog/aria"
	ldrender "github.com/jack-work/figaro/internal/livelog/render"
)

// draftPager is a pager with a send hook that records rather than dials.
func draftPager(t *testing.T, width, height int) (*transcript, *string) {
	t.Helper()
	client := aria.NewClient()
	client.SetClosedLimit(transcriptTailLimit)
	client.Apply(aria.Page{Parts: []aria.TurnPart{{Turn: aria.Turn{
		ID: 1, Inquiry: "a question", Sealed: true,
		// Src is what makes a selection quotable: a coordinate names a
		// passage of the IR, and a node with no provenance has none.
		Nodes: []livedoc.Node{{
			Type: livedoc.NodeProse, Src: []livedoc.Src{{LT: 412, Block: 0}},
			Markdown: "AN ANSWER, long enough to have a second line of its own prose",
		}},
	}}}}, aria.Notify)
	tr := newTranscript(ldrender.NewFakeTerminal(width, height), width, height,
		&ariaView{settings: &renderSettings{}}, client, "aria1234", time.Unix(0, 0))
	tr.enter()
	tr.follow = false
	tr.buildIndex()
	var sent string
	tr.sendDraft = func(text string) { sent = text }
	return tr, &sent
}

// '>' OPENS THE DRAFT, and the drawer says so. The sigil is the key that
// opened it, so a reader can tell the two boxes apart at a glance.
func TestDraftOpensOnAngleBracket(t *testing.T) {
	tr, _ := draftPager(t, 72, 24)
	tr.key('>')
	if tr.box != boxPrompt {
		t.Fatalf("'>' left the box %v", tr.box)
	}
	if tr.mode() != modeBox {
		t.Fatalf("the draft does not own the keyboard: mode %v", tr.mode())
	}
	typeInto(tr, "hello")
	rows := plainRows(tr.inputDrawerLines())
	if _, ok := rowContaining(rows, "> hello"); !ok {
		t.Fatalf("the drawer does not show what was typed:\n%s", strings.Join(rows, "\n"))
	}
	if _, ok := rowContaining(rows, "M-Enter sends"); !ok {
		t.Fatalf("the drawer must say what sends it:\n%s", strings.Join(rows, "\n"))
	}
}

// ENTER IS A NEWLINE IN A DRAFT. This is the whole reason the draft is not
// the ':' box: a prompt has lines in it, and a box whose Enter submits can
// only ever carry the first of them.
func TestDraftEnterIsANewline(t *testing.T) {
	tr, sent := draftPager(t, 72, 24)
	tr.key('>')
	typeInto(tr, "first")
	tr.key('\r')
	typeInto(tr, "second")
	if got := tr.draft.String(); got != "first\nsecond" {
		t.Fatalf("draft is %q", got)
	}
	if *sent != "" {
		t.Fatalf("Enter sent %q; only M-Enter sends", *sent)
	}
	if tr.box != boxPrompt {
		t.Fatal("Enter closed the draft")
	}
	// The drawer draws one row per line, which is what the painter counts.
	rows := plainRows(tr.inputDrawerLines())
	if _, ok := rowContaining(rows, "> first"); !ok {
		t.Fatalf("no first line:\n%s", strings.Join(rows, "\n"))
	}
	if _, ok := rowContaining(rows, "second"); !ok {
		t.Fatalf("no second line:\n%s", strings.Join(rows, "\n"))
	}
	if _, ok := rowContaining(rows, "2 lines"); !ok {
		t.Fatalf("the hint must count the lines:\n%s", strings.Join(rows, "\n"))
	}
}

// M-Enter sends the bytes AS TYPED: no tokenizer, so a newline and a quote
// mean themselves. `:send -- ` cannot carry either, which is the defect this
// box exists to fix.
func TestDraftSendsItsBytesWhole(t *testing.T) {
	tr, sent := draftPager(t, 72, 24)
	tr.key('>')
	typeInto(tr, `he said "don't" --id 12`)
	tr.key('\r')
	typeInto(tr, "and then a second line")
	tr.dispatch(keyEvent{meta: 0x0d, alt: true, mode: modeBox})

	want := "he said \"don't\" --id 12\nand then a second line"
	if *sent != want {
		t.Fatalf("sent %q, want %q", *sent, want)
	}
	if tr.box != boxNone || tr.draft.String() != "" {
		t.Fatalf("a sent draft must be spent: box=%v draft=%q", tr.box, tr.draft.String())
	}
}

// ESC KEEPS THE DRAFT, ^C spends it. A draft may be a page of pasted text,
// and a key that is "close this" everywhere else in the pager must not also
// be "throw that away".
func TestEscapeKeepsTheDraftAndCtrlCSpendsIt(t *testing.T) {
	tr, _ := draftPager(t, 72, 24)
	tr.key('>')
	typeInto(tr, "an expensive paste")
	tr.key(0x1b)
	if tr.box != boxNone {
		t.Fatal("Esc did not close the drawer")
	}
	if tr.draft.String() != "an expensive paste" {
		t.Fatalf("Esc spent the draft: %q", tr.draft.String())
	}
	tr.key('>')
	if tr.draft.String() != "an expensive paste" {
		t.Fatalf("reopening lost the draft: %q", tr.draft.String())
	}
	tr.key(0x03)
	if tr.box != boxNone || tr.draft.String() != "" {
		t.Fatalf("^C must abandon it: box=%v draft=%q", tr.box, tr.draft.String())
	}
}

// A paste arrives as TEXT, tabs and newlines and all, and with no box open it
// opens the draft: pasting into a pager is a prompt being written.
func TestPasteOpensTheDraftAndKeepsItsLines(t *testing.T) {
	tr, _ := draftPager(t, 72, 24)
	tr.pasteText("first line\r\nsecond\tline\n\x1b[31mthird\n")
	if tr.box != boxPrompt {
		t.Fatalf("a paste did not open the draft: box=%v", tr.box)
	}
	want := "first line\nsecond line\n[31mthird\n"
	if got := tr.draft.String(); got != want {
		t.Fatalf("draft is %q, want %q", got, want)
	}
}

// The command box still folds a paste onto its one line: a newline it cannot
// represent must not become a newline it draws.
func TestPasteIntoTheCommandBoxStaysOneLine(t *testing.T) {
	tr, _ := draftPager(t, 72, 24)
	tr.key(':')
	tr.pasteText("send -- one\ntwo")
	if got := tr.cmdline.String(); strings.Contains(got, "\n") {
		t.Fatalf("the command line took a newline: %q", got)
	}
	if got := tr.cmdline.String(); got != "send -- one two" {
		t.Fatalf("command line is %q", got)
	}
}

// '>' WITH A HIGHLIGHT UP QUOTES IT: the draft opens holding the range, and
// the submit expands it into the coordinate the daemon resolves. The same
// placeholder the ':' box uses, so there is one grammar for one idea.
func TestDraftFromASelectionCarriesTheCoordinate(t *testing.T) {
	tr, sent := draftPager(t, 72, 24)
	// v puts a cursor up; the second press anchors a highlight, which is what
	// there has to be for '>' to have anything to quote.
	tr.key('v')
	tr.key('v')
	tr.key('w')
	if !tr.visual.highlighted() {
		t.Fatalf("the fixture produced no highlight to quote: %+v", tr.visual)
	}
	tr.key('>')
	if !strings.HasPrefix(tr.draft.String(), visualRangePlaceholder) {
		t.Fatalf("the draft does not hold the range: %q", tr.draft.String())
	}
	typeInto(tr, "what did you mean?")
	tr.dispatch(keyEvent{meta: 0x0d, alt: true, mode: modeBox})
	if !strings.HasSuffix(*sent, "what did you mean?") {
		t.Fatalf("sent %q", *sent)
	}
	if strings.HasPrefix(*sent, visualRangePlaceholder) {
		t.Fatalf("the placeholder went out unexpanded: %q", *sent)
	}
	if !strings.HasPrefix(*sent, "<") || !strings.Contains(*sent, ">!") {
		t.Fatalf("want a quote coordinate at the head of the prompt: %q", *sent)
	}
}

// Tab in a draft completes what belongs in a PROMPT. The command box's pool
// would offer verbs and flags to a reader typing a sentence.
func TestDraftCompletesPromptThingsNotVerbs(t *testing.T) {
	tr, _ := draftPager(t, 72, 24)
	asked := ""
	tr.promptCompleter = func(line string) []string {
		asked = line
		return []string{"@mantra\tthe board's mantra"}
	}
	tr.completer = func(string) []string {
		t.Fatal("the draft asked the command line's completer")
		return nil
	}
	tr.key('>')
	typeInto(tr, "see @man")
	tr.key(0x09)
	if asked == "" {
		t.Fatal("Tab asked nobody")
	}
	if got := tr.draft.String(); got != "see @mantra " {
		t.Fatalf("Tab left the draft %q", got)
	}
}

// An empty draft sends nothing: M-Enter on it is a key that puts the drawer
// away, not a prompt made of whitespace.
func TestEmptyDraftSendsNothing(t *testing.T) {
	tr, sent := draftPager(t, 72, 24)
	tr.key('>')
	tr.key('\r')
	tr.key(' ')
	tr.dispatch(keyEvent{meta: 0x0d, alt: true, mode: modeBox})
	if *sent != "" {
		t.Fatalf("an empty draft sent %q", *sent)
	}
	if tr.box != boxNone {
		t.Fatal("the drawer stayed open")
	}
}

// The two boxes do not share a buffer: a draft left open must not appear in
// the command line, which runs what it holds.
func TestTheTwoBoxesKeepSeparateBuffers(t *testing.T) {
	tr, _ := draftPager(t, 72, 24)
	tr.key('>')
	typeInto(tr, "a prompt")
	tr.key(0x1b)
	tr.key(':')
	if tr.cmdline.String() != "" {
		t.Fatalf("the command line holds the draft: %q", tr.cmdline.String())
	}
	typeInto(tr, "ls")
	tr.key(0x1b)
	tr.key('>')
	if tr.draft.String() != "a prompt" {
		t.Fatalf("the draft holds the command: %q", tr.draft.String())
	}
}

// ^A/^E and ^P/^N are the LINE's in a draft, which is emacs: a box with lines
// in it has a line to move along, and the command box has a history instead.
func TestDraftMotionsAreLineLocal(t *testing.T) {
	tr, _ := draftPager(t, 72, 24)
	tr.key('>')
	typeInto(tr, "alpha")
	tr.key('\r')
	typeInto(tr, "beta")
	tr.key(0x01) // ^A
	if tr.draft.cursor != 6 {
		t.Fatalf("^A went to %d, want the start of the second line (6)", tr.draft.cursor)
	}
	tr.key(0x10) // ^P
	if tr.draft.cursor != 0 {
		t.Fatalf("^P went to %d, want the first line (0)", tr.draft.cursor)
	}
	tr.key(0x05) // ^E
	if tr.draft.cursor != 5 {
		t.Fatalf("^E went to %d, want the end of the first line (5)", tr.draft.cursor)
	}
	tr.key(0x0e) // ^N
	if tr.draft.cursor != 10 {
		t.Fatalf("^N went to %d, want the end of the second line (10)", tr.draft.cursor)
	}
	if tr.draft.String() != "alpha\nbeta" {
		t.Fatalf("a motion edited the draft: %q", tr.draft.String())
	}
}
