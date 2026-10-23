package cli

import (
	"strings"
	"testing"
	"time"

	"github.com/jack-work/figaro/api/livedoc"
	ldrender "github.com/jack-work/figaro/internal/livelog/render"
	"github.com/jack-work/figaro/sdk/aria"
)

func composerPager(t *testing.T, width, height int) (*transcript, *string) {
	t.Helper()
	client := aria.NewClient()
	client.SetClosedLimit(transcriptTailLimit)
	client.Apply(aria.Page{Parts: []aria.TurnPart{{Turn: aria.Turn{
		ID: 1, Inquiry: "a question", Sealed: true,
		// A node with no Src is not quotable.
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
	if len(rows) != 1 {
		t.Fatalf("the drawer costs %d rows for one line of draft:\n%s", len(rows), strings.Join(rows, "\n"))
	}
}

// More spells the drawer's gestures into the rule above it: a row it already
// had, so turning more on moves nothing.
func TestComposerHintsRideTheRuleUnderMore(t *testing.T) {
	tr, _ := composerPager(t, 72, 24)
	tr.key('>')
	typeInto(tr, "hello")
	if strings.Contains(stripANSI(tr.transcriptRule()), "composer") {
		t.Fatal("the drawer named itself without being asked")
	}
	before := len(tr.footLines())
	tr.view.(*ariaView).settings.more = true
	rule := stripANSI(tr.transcriptRule())
	if !strings.Contains(rule, composerGlyph+" composer") || !strings.Contains(rule, "^S send") {
		t.Fatalf("more must spell the drawer's gestures on its rule: %q", rule)
	}
	if after := len(tr.footLines()); after != before {
		t.Fatalf("more changed the drawer's height from %d to %d rows", before, after)
	}
}

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

// No tokenizer: a newline and a quote mean themselves.
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

// Esc: insert to normal, normal to closed, draft kept. ^C spends it.
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

func TestComposerFromASelectionCarriesTheCoordinate(t *testing.T) {
	tr, sent := composerPager(t, 72, 24)
	// The second v anchors a highlight.
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

func TestComposerCompletesPromptThingsNotVerbs(t *testing.T) {
	tr, _ := composerPager(t, 72, 24)
	asked := ""
	tr.composerCompleter = func(line string) []string {
		asked = line
		return []string{"@mantra\tthe bound form's mantra"}
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

// pressKeys goes through dispatch: in normal mode Enter is a binding.
func pressKeys(tr *transcript, s string) {
	for i := 0; i < len(s); i++ {
		tr.key(s[i])
	}
}

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
	cases := []struct {
		name string
		keys string
		want int
	}{
		{"line start", "0", 12},
		{"first text", "^", 14},
		{"line end", "$", 27},
		{"word forward", "w", 21},
		{"word back", "b", 6},
		{"down a line", "j", 30},
		{"up a line", "k", 2},
		{"buffer end", "G", len([]rune(text))},
		{"buffer top", "gg", 0},
		{"left", "h", 13},
		{"right", "l", 15},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			tr := composerAt(t, text, 14)
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

// d, c, x, p and r are inert: a half-implemented edit is worse than none.
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

func TestComposerMarkAndSpan(t *testing.T) {
	tr := composerAt(t, "alpha bravo\ncharlie", 0)
	tr.key('v')
	pressKeys(tr, "ww")
	lo, hi, ok := tr.composerSpan()
	if !ok {
		t.Fatal("v marked nothing")
	}
	// The mark covers the cursor's own rune, as vim's does.
	if got := tr.draft.selectionText(lo, hi); got != "alpha bravo\n" {
		t.Fatalf("char mark covers %q", got)
	}
	pressKeys(tr, "j")
	tr.key('V')
	lo, hi, _ = tr.composerSpan()
	if got := tr.draft.selectionText(lo, hi); got != "alpha bravo\ncharlie" {
		t.Fatalf("line mark covers %q", got)
	}
	if text, ok := tr.composerYankText(); !ok || text != "alpha bravo\ncharlie" {
		t.Fatalf("y would copy %q (ok=%v)", text, ok)
	}
	if tr.composerHighlighted() {
		t.Fatal("the mark survived the yank")
	}
	if text, ok := tr.composerYankText(); !ok || text != "alpha bravo\ncharlie" {
		t.Fatalf("an unmarked yank copies %q (ok=%v)", text, ok)
	}
}

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
	tr.key('n')
	if tr.draft.cursor != 20 {
		t.Fatalf("one match, so n wraps onto it: cursor %d", tr.draft.cursor)
	}
	tr.key('/')
	pressKeys(tr, "zebra")
	tr.key('\r')
	if tr.draft.cursor != 20 {
		t.Fatalf("a missing query moved the cursor to %d", tr.draft.cursor)
	}
}

func TestSearchBoxNamesItsTarget(t *testing.T) {
	tr := composerAt(t, "alpha", 0)
	tr.key('/')
	pressKeys(tr, "al")
	rows := plainRows(tr.inputDrawerLines())
	if _, ok := rowContaining(rows, composerGlyph+"/al"); !ok {
		t.Fatalf("the search box does not name the drawer:\n%s", strings.Join(rows, "\n"))
	}
}

func TestCommandLineOverTheDrawerComesBack(t *testing.T) {
	tr := composerAt(t, "a draft worth keeping", 0)
	tr.key(':')
	if tr.box != boxCommand {
		t.Fatalf("':' did not open the command line: box=%v", tr.box)
	}
	typeInto(tr, "ls")
	tr.key(0x1b)
	if tr.box != boxComposer || tr.composerMode != composerNormal {
		t.Fatalf("Esc did not come back to the drawer: box=%v mode=%v", tr.box, tr.composerMode)
	}
	if tr.draft.String() != "a draft worth keeping" {
		t.Fatalf("the detour spent the draft: %q", tr.draft.String())
	}
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

// Windows Terminal and conhost claim Alt+Enter, so ^S must send.
func TestComposerSendsOnCtrlS(t *testing.T) {
	for _, mode := range []string{"insert", "normal"} {
		t.Run(mode, func(t *testing.T) {
			tr, sent := composerPager(t, 72, 24)
			tr.key('>')
			typeInto(tr, "ship it")
			if mode == "normal" {
				tr.key(0x1b)
			}
			tr.key(0x13)
			if *sent != "ship it" {
				t.Fatalf("^S in %s mode sent %q", mode, *sent)
			}
			if tr.box != boxNone {
				t.Fatalf("^S in %s mode left the composer open", mode)
			}
		})
	}
}

func TestComposerSendsOnCtrlSReportedAsCSIu(t *testing.T) {
	tr, sent := composerPager(t, 72, 24)
	tr.key('>')
	typeInto(tr, "ship it")
	tr.dispatch(keyEvent{ctrl: 's', mode: modeBox})
	if *sent != "ship it" {
		t.Fatalf("CSI-u ^S sent %q", *sent)
	}
}

// ^S must still walk the command line's history search forward.
func TestCtrlSStillSearchesTheCommandHistory(t *testing.T) {
	tr, sent := composerPager(t, 72, 24)
	tr.cmdline.remember("send -- older")
	tr.cmdline.remember("ls -H")
	tr.key(':')
	tr.key(0x12)
	if !tr.cmdline.searching() {
		t.Fatal("^R did not start a history search")
	}
	tr.key(0x13)
	if !tr.cmdline.searching() {
		t.Fatal("^S ended the command line's search instead of walking it")
	}
	if *sent != "" {
		t.Fatalf("^S in the command line sent %q", *sent)
	}
	if tr.box != boxCommand {
		t.Fatalf("^S closed the command line: box=%v", tr.box)
	}
}
