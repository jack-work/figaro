package cli

import (
	"strings"
	"testing"
	"time"

	"github.com/jack-work/figaro/api/livedoc"
	"github.com/jack-work/figaro/internal/livelog/aria"
	ldrender "github.com/jack-work/figaro/internal/livelog/render"
)

// composerPager is a pager with a send hook that records rather than dials.
func composerPager(t *testing.T, width, height int) (*transcript, *string) {
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
	tr.sendComposer = func(text string) { sent = text }
	return tr, &sent
}

// '>' OPENS THE DRAFT, and the drawer says so. The sigil is the key that
// opened it, so a reader can tell the two boxes apart at a glance.
func TestComposerOpensOnAngleBracket(t *testing.T) {
	tr, _ := composerPager(t, 72, 24)
	tr.key('>')
	if tr.box != boxComposer {
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
	// THE SIGIL IS THE WHOLE INDICATOR. No hint row, no title: a reader who
	// wants words presses M-m (see TestComposerTitleOnlyUnderVerbose).
	if len(rows) != 1 {
		t.Fatalf("the drawer costs %d rows for one line of draft:\n%s", len(rows), strings.Join(rows, "\n"))
	}
}

// The words live under M-m, which is where every other "spell it out" lives.
func TestComposerTitleOnlyUnderVerbose(t *testing.T) {
	tr, _ := composerPager(t, 72, 24)
	tr.key('>')
	typeInto(tr, "hello")
	if _, found := rowContaining(plainRows(tr.inputDrawerLines()), "composer"); found {
		t.Fatal("the drawer named itself without being asked")
	}
	// THROUGH THE KEY, not through the field: a title a reader cannot reach is
	// not a feature. M-m is an input-level chord, so the fixture flips the
	// setting the input loop would flip and asserts the row follows.
	tr.view.(*ariaView).settings.verbose = true
	rows := plainRows(tr.inputDrawerLines())
	if _, ok := rowContaining(rows, composerGlyph+" composer"); !ok {
		t.Fatalf("M-m must title the drawer:\n%s", strings.Join(rows, "\n"))
	}
	if _, ok := rowContaining(rows, "M-⏎ send"); !ok {
		t.Fatalf("the title is where the gesture is spelled out:\n%s", strings.Join(rows, "\n"))
	}
}

// ENTER IS A NEWLINE IN A DRAFT. This is the whole reason the draft is not
// the ':' box: a prompt has lines in it, and a box whose Enter submits can
// only ever carry the first of them.
func TestComposerEnterIsANewline(t *testing.T) {
	tr, sent := composerPager(t, 72, 24)
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
	if tr.box != boxComposer {
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
	if len(rows) != 2 {
		t.Fatalf("two lines of draft drew %d rows:\n%s", len(rows), strings.Join(rows, "\n"))
	}
}

// M-Enter sends the bytes AS TYPED: no tokenizer, so a newline and a quote
// mean themselves. `:send -- ` cannot carry either, which is the defect this
// box exists to fix.
func TestComposerSendsItsBytesWhole(t *testing.T) {
	tr, sent := composerPager(t, 72, 24)
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

// ESC IS A LADDER: insert to normal, normal to closed, and the draft survives
// both rungs. ^C is the key that spends it, as it is at a shell prompt.
func TestEscapeKeepsTheDraftAndCtrlCSpendsIt(t *testing.T) {
	tr, _ := composerPager(t, 72, 24)
	tr.key('>')
	typeInto(tr, "an expensive paste")
	tr.key(0x1b)
	if tr.box != boxComposer || tr.composerMode != composerNormal {
		t.Fatalf("the first Esc must enter normal mode: box=%v mode=%v", tr.box, tr.composerMode)
	}
	tr.key(0x1b)
	if tr.box != boxNone {
		t.Fatal("the second Esc did not close the drawer")
	}
	if tr.draft.String() != "an expensive paste" {
		t.Fatalf("Esc spent the draft: %q", tr.draft.String())
	}
	tr.key('>')
	if tr.draft.String() != "an expensive paste" {
		t.Fatalf("reopening lost the draft: %q", tr.draft.String())
	}
	if tr.composerMode != composerInsert {
		t.Fatal("'>' must come back in insert mode: it is the key that types")
	}
	tr.key(0x03)
	if tr.box != boxNone || tr.draft.String() != "" {
		t.Fatalf("^C must abandon it: box=%v draft=%q", tr.box, tr.draft.String())
	}
}

// A paste arrives as TEXT, tabs and newlines and all, and with no box open it
// opens the draft: pasting into a pager is a prompt being written.
func TestPasteOpensTheComposerAndKeepsItsLines(t *testing.T) {
	tr, _ := composerPager(t, 72, 24)
	tr.pasteText("first line\r\nsecond\tline\n\x1b[31mthird\n")
	if tr.box != boxComposer {
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
	tr, _ := composerPager(t, 72, 24)
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
func TestComposerFromASelectionCarriesTheCoordinate(t *testing.T) {
	tr, sent := composerPager(t, 72, 24)
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
func TestComposerCompletesPromptThingsNotVerbs(t *testing.T) {
	tr, _ := composerPager(t, 72, 24)
	asked := ""
	tr.composerCompleter = func(line string) []string {
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
func TestEmptyComposerSendsNothing(t *testing.T) {
	tr, sent := composerPager(t, 72, 24)
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
	tr, _ := composerPager(t, 72, 24)
	tr.key('>')
	typeInto(tr, "a prompt")
	tr.key(0x1b)
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
func TestComposerMotionsAreLineLocal(t *testing.T) {
	tr, _ := composerPager(t, 72, 24)
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

// ---------------------------------------------------------------------------
// The composer's normal mode: the transcript's own vocabulary, over the
// draft. Every case here watches the CURSOR or the MARK, which is the thing
// the keymap oracle's signature cannot see.
// ---------------------------------------------------------------------------

// pressKeys drives bytes through DISPATCH rather than into the editor, which
// is the difference between typing and pressing: in insert mode Enter makes a
// newline, in normal mode it is a binding. typeInto writes into the box and
// cannot reach either.
func pressKeys(tr *transcript, s string) {
	for i := 0; i < len(s); i++ {
		tr.key(s[i])
	}
}

// composerAt opens the drawer on text and drops into normal mode with the
// cursor at a known rune.
func composerAt(t *testing.T, text string, cursor int) *transcript {
	t.Helper()
	tr, _ := composerPager(t, 72, 24)
	tr.key('>')
	pressKeys(tr, text)
	tr.key(0x1b)
	if tr.draft.String() != text {
		t.Fatalf("the fixture typed %q, not %q", tr.draft.String(), text)
	}
	tr.draft.cursor = cursor
	return tr
}

func TestComposerNormalMotions(t *testing.T) {
	const text = "alpha bravo\n  charlie delta\nend"
	// "alpha bravo\n  charlie delta\nend": the cursor starts on the 'c' of
	// charlie, rune 14.
	cases := []struct {
		name string
		keys string
		want int
	}{
		{"line start", "0", 12},
		{"first text", "^", 14},
		{"line end", "$", 27},
		{"word forward", "w", 21}, // readline's forward-word: the end of charlie
		{"word back", "b", 6},     // already at a word start, so the one before
		{"down a line", "j", 30},  // the column is kept
		{"up a line", "k", 2},
		{"buffer end", "G", len([]rune(text))},
		{"buffer top", "gg", 0},
		{"left", "h", 13},
		{"right", "l", 15},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			tr := composerAt(t, text, 14) // on 'c' of charlie
			pressKeys(tr, c.keys)
			if tr.draft.cursor != c.want {
				t.Fatalf("%q left the cursor at %d, want %d", c.keys, tr.draft.cursor, c.want)
			}
			if tr.draft.String() != text {
				t.Fatalf("a motion edited the draft: %q", tr.draft.String())
			}
		})
	}
}

// NO EDITING VERBS. d, c, x, p and r are inert, deliberately: this mode is for
// reading a draft, and a key that half-implemented an edit would be worse than
// one that does nothing.
func TestComposerNormalHasNoEditingVerbs(t *testing.T) {
	const text = "alpha bravo"
	for _, k := range []byte{'d', 'c', 'x', 'p', 'r', 'D', 'C', 'X', 'P', 'u'} {
		tr := composerAt(t, text, 3)
		tr.key(k)
		if got := tr.draft.String(); got != text {
			t.Fatalf("%q changed the draft to %q", string(k), got)
		}
		if tr.composerMode != composerNormal {
			t.Fatalf("%q left normal mode", string(k))
		}
	}
}

// v and V mark from the cursor; y copies what is marked. The span is what the
// drawer washes, so the test reads the span rather than the paint.
func TestComposerMarkAndSpan(t *testing.T) {
	tr := composerAt(t, "alpha bravo\ncharlie", 0)
	tr.key('v')
	pressKeys(tr, "ww")
	lo, hi, ok := tr.composerSpan()
	if !ok {
		t.Fatal("v marked nothing")
	}
	// Two forward-words from 0 land on the newline that ends the first line,
	// and the mark covers the cursor's own rune, as vim's does.
	if got := tr.draft.selectionText(lo, hi); got != "alpha bravo\n" {
		t.Fatalf("char mark covers %q", got)
	}
	// V takes whole lines, wherever the cursor stands in them: with the cursor
	// moved into the second line, the mark covers both.
	pressKeys(tr, "j")
	tr.key('V')
	lo, hi, _ = tr.composerSpan()
	if got := tr.draft.selectionText(lo, hi); got != "alpha bravo\ncharlie" {
		t.Fatalf("line mark covers %q", got)
	}
	if text, ok := tr.composerYankText(); !ok || text != "alpha bravo\ncharlie" {
		t.Fatalf("y would copy %q (ok=%v)", text, ok)
	}
	// Yanking drops the mark, as it does in a pit.
	if tr.composerHighlighted() {
		t.Fatal("the mark survived the yank")
	}
	// With nothing marked, y takes the whole draft: the useful default for a
	// box whose contents are what you came to copy.
	if text, ok := tr.composerYankText(); !ok || text != "alpha bravo\ncharlie" {
		t.Fatalf("an unmarked yank copies %q (ok=%v)", text, ok)
	}
}

// i a I A go back to the box that types, each at its own place.
func TestComposerReturnsToInsert(t *testing.T) {
	cases := map[byte]int{'i': 3, 'a': 4, 'I': 0, 'A': 11}
	for k, want := range cases {
		tr := composerAt(t, "alpha bravo", 3)
		tr.key(k)
		if tr.composerMode != composerInsert {
			t.Fatalf("%q did not return to insert", string(k))
		}
		if tr.draft.cursor != want {
			t.Fatalf("%q left the cursor at %d, want %d", string(k), tr.draft.cursor, want)
		}
		typeInto(tr, "X")
		if !strings.Contains(tr.draft.String(), "X") {
			t.Fatalf("%q did not leave a box that types: %q", string(k), tr.draft.String())
		}
	}
}

// THE SEARCH TARGETS THE DRAWER. A query typed with the composer open must
// move the draft's cursor, not the conversation behind it.
func TestSearchTargetsTheComposerDraft(t *testing.T) {
	tr := composerAt(t, "alpha bravo\ncharlie delta", 0)
	before := tr.offset
	tr.key('/')
	pressKeys(tr, "delta")
	tr.key('\r')
	if tr.draft.cursor != 20 {
		t.Fatalf("the search left the draft cursor at %d, want 20", tr.draft.cursor)
	}
	if tr.offset != before {
		t.Fatalf("the search moved the conversation (offset %d -> %d)", before, tr.offset)
	}
	// n and N repeat it over the draft, wrapping.
	tr.key('n')
	if tr.draft.cursor != 20 {
		t.Fatalf("one match, so n wraps onto it: cursor %d", tr.draft.cursor)
	}
	// A query the draft does not hold reports, and moves nothing.
	tr.key('/')
	pressKeys(tr, "zebra")
	tr.key('\r')
	if tr.draft.cursor != 20 {
		t.Fatalf("a missing query moved the cursor to %d", tr.draft.cursor)
	}
}

// The search box says what it is aimed at with one glyph, which is all the
// room a box being typed into can spare.
func TestSearchBoxNamesItsTarget(t *testing.T) {
	tr := composerAt(t, "alpha", 0)
	tr.key('/')
	pressKeys(tr, "al")
	rows := plainRows(tr.inputDrawerLines())
	if _, ok := rowContaining(rows, composerGlyph+"/al"); !ok {
		t.Fatalf("the search box does not name the drawer:\n%s", strings.Join(rows, "\n"))
	}
}

// ':' FROM NORMAL MODE IS A DETOUR, not a dismissal: the command line opens
// over the drawer and closing it comes back to the draft, in the mode it was
// left in.
func TestCommandLineOverTheDrawerComesBack(t *testing.T) {
	tr := composerAt(t, "a draft worth keeping", 0)
	tr.key(':')
	if tr.box != boxCommand {
		t.Fatalf("':' did not open the command line: box=%v", tr.box)
	}
	typeInto(tr, "ls")
	tr.key(0x1b) // Esc closes the command line
	if tr.box != boxComposer || tr.composerMode != composerNormal {
		t.Fatalf("Esc did not come back to the drawer: box=%v mode=%v", tr.box, tr.composerMode)
	}
	if tr.draft.String() != "a draft worth keeping" {
		t.Fatalf("the detour spent the draft: %q", tr.draft.String())
	}
	// And a line that RUNS also comes back: the draft is still what the reader
	// is writing.
	ran := ""
	tr.command = func(line string) { ran = line }
	tr.key(':')
	typeInto(tr, "ls")
	tr.key('\r')
	if ran != "ls" {
		t.Fatalf("the command did not run: %q", ran)
	}
	if tr.box != boxComposer {
		t.Fatalf("a run command left the drawer closed: box=%v", tr.box)
	}
}

// F hands the pane to the drawer, the same disposition every pit shares.
func TestComposerFullscreenTakesThePane(t *testing.T) {
	tr := composerAt(t, strings.Repeat("a line of the draft\n", 30), 0)
	small := len(tr.inputDrawerLines())
	tr.key('F')
	big := len(tr.inputDrawerLines())
	if big <= small {
		t.Fatalf("F did not grow the drawer: %d -> %d rows", small, big)
	}
	if !tr.full {
		t.Fatal("F did not set the pager's fullscreen disposition")
	}
	tr.key('F')
	if got := len(tr.inputDrawerLines()); got != small {
		t.Fatalf("F again left %d rows, want %d", got, small)
	}
}

// M-Enter still sends from normal mode: the gesture is about the draft, not
// about which keymap is holding the keyboard.
func TestComposerSendsFromNormalMode(t *testing.T) {
	tr, sent := composerPager(t, 72, 24)
	tr.key('>')
	typeInto(tr, "ready to go")
	tr.key(0x1b)
	tr.dispatch(keyEvent{meta: 0x0d, alt: true, mode: modeComposer})
	if *sent != "ready to go" {
		t.Fatalf("sent %q", *sent)
	}
	if tr.box != boxNone {
		t.Fatal("the drawer stayed open after a send")
	}
}
