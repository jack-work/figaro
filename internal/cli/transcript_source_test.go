package cli

import (
	"strings"
	"testing"
	"time"
	"unicode"

	"github.com/jack-work/figaro/api/livedoc"
	"github.com/jack-work/figaro/api/quote"
	"github.com/jack-work/figaro/internal/livelog/aria"
	ldrender "github.com/jack-work/figaro/internal/livelog/render"
	"github.com/jack-work/figaro/internal/render"
)

// The corpus: the shapes agent output actually takes. For each, every
// rendered row that carries text must align, and the runes the rows claim
// must be the runes the source holds at those offsets, in order.
var alignCorpus = []struct {
	name, source string
}{
	{"plain paragraph", "The retry loop backs off exponentially, but the jitter is applied before the cap rather than after, so a long tail of clients stampede on the ninth retry."},
	{"emphasis", "This is **bold** and this is _italic_ and this is `code` in a sentence that wraps because it is long enough to wrap at sixty columns."},
	{"heading and list", "## Findings\n\n- first point about the thing\n- second point, with **weight**\n  - a nested point under the second\n- third"},
	{"numbered list", "1. open the file\n2. read the header\n3. seek past it\n4. read the body"},
	{"link", "See [the plan](https://example.com/plans/x.md) for the whole design, and [this](https://example.com/y) too."},
	{"code fence", "Before:\n\n```go\nfunc a() int {\n\treturn 1\n}\n```\n\nAfter the fence."},
	{"table", "| key | value |\n|---|---|\n| head_chars | 480 |\n| tail_chars | 160 |"},
	{"cjk", "日本語のテキストは幅が二倍です。これは折り返されるべき長い行で、六十列を超えます。"},
	{"emoji", "Done ✅ and then 🚀 launched, with a 🐛 found along the way and fixed."},
	{"blockquote", "> a quoted line that is long enough to wrap around at sixty columns of width\n> and a second"},
	{"mixed", "Summary:\n\n1. **Parse** the token\n2. Resolve it against the `log`\n\n> refused before append\n\nThen `quote.Format` emits it."},
}

func TestAlignRows_CorpusEveryTextRowAligns(t *testing.T) {
	for _, c := range alignCorpus {
		rows := render.Prose(c.source, 60)
		got := alignRows(c.source, rows)
		src := []rune(c.source)
		lastEnd := 0
		for i, r := range got {
			plain := strings.TrimSpace(render.StripEscapes(rows[i]))
			if strings.IndexFunc(plain, func(r rune) bool { return unicode.IsLetter(r) || unicode.IsDigit(r) }) < 0 {
				continue // chrome glamour draws: rules, table borders
			}
			if !r.aligned {
				t.Errorf("%s: row %d %q did not align", c.name, i, plain)
				continue
			}
			if r.start < lastEnd {
				t.Errorf("%s: row %d starts at %d, before the previous row's end %d", c.name, i, r.start, lastEnd)
			}
			lastEnd = r.end
			// Every matched column stands for the rune it claims.
			pr := []rune(render.StripEscapes(rows[i]))
			col := 0
			for _, rr := range pr {
				w := runeCells(rr)
				if k := r.cols[col]; k >= 0 && src[k] != rr {
					t.Errorf("%s: row %d col %d says source[%d]=%q, row has %q", c.name, i, col, k, src[k], rr)
				}
				col += w
			}
		}
	}
}

// A row the renderer made up entirely (a rule, a table border) must NOT
// align: widening depends on it saying so.
func TestAlignRows_ChromeRowsDoNotAlign(t *testing.T) {
	got := alignRows("some text", []string{"────────────", "some text"})
	if got[0].aligned {
		t.Fatal("a rule aligned to prose")
	}
	if !got[1].aligned || got[1].start != 0 || got[1].end != len("some text") {
		t.Fatalf("the text row = %+v", got[1])
	}
}

// stepTo walks the cursor to the top of the window and then down until its
// row contains needle.
func stepTo(t *testing.T, tr *transcript, needle string) {
	t.Helper()
	tr.key('g')
	tr.key('g')
	for i := 0; i < 60; i++ {
		line, _ := tr.visualLineOf(tr.visual.cursor)
		if strings.Contains(render.StripEscapes(tr.lineText(line)), needle) {
			return
		}
		tr.key('j')
	}
	t.Fatalf("no row containing %q within reach", needle)
}

func sourceFixture(t *testing.T) (*transcript, string) {
	t.Helper()
	const body = "The retry loop backs off exponentially, but the jitter is applied before the cap rather than after, so a long tail of clients stampede on the ninth retry.\n\nA second paragraph, **with weight**, that also wraps at this width."
	ft := ldrender.NewFakeTerminal(60, 30)
	client := aria.NewClient()
	client.Apply(aria.Page{Parts: []aria.TurnPart{{Turn: aria.Turn{
		ID: 3, Sealed: true, Inquiry: "why does it stampede?", LTs: []uint64{400, 412}, Nodes: []livedoc.Node{
			{Type: livedoc.NodeProse, Markdown: body, Src: []livedoc.Src{{LT: 412, Block: 0}}},
			{Type: livedoc.NodeTool, Name: "bash", Status: livedoc.StatusOK, Output: "l1\nl2\nl3", Src: []livedoc.Src{{LT: 413, Block: 0}, {LT: 414, Block: 0}}},
		},
	}}}}, aria.Notify)
	tr := newTranscript(ft, 60, 30, ldrender.NodeText{}, client, "aria1234", time.Now())
	tr.enter()
	tr.render()
	return tr, body
}

// V over the first two rows of a wrapped paragraph names the source runes
// those rows were rendered from, at the node's Src coordinate.
func TestVisualRange_LineModeOverWrappedProse(t *testing.T) {
	tr, body := sourceFixture(t)
	tr.key('V')
	stepTo(t, tr, "The retry")
	tr.key('V') // anchor the line-wise highlight here
	tr.key('j')
	r, note, err := tr.visualRange()
	if err != "" {
		t.Fatalf("visualRange: %s", err)
	}
	if r.Start.LT != 412 || r.Start.Block != 0 || !r.Offsets || r.Span() {
		t.Fatalf("range = %+v", r)
	}
	if r.Start.Offset != 0 {
		t.Fatalf("first row of the paragraph starts at rune %d, want 0", r.Start.Offset)
	}
	got := string([]rune(body)[r.Start.Offset:r.End.Offset])
	if !strings.HasPrefix(got, "The retry loop") || strings.Contains(got, "second paragraph") {
		t.Fatalf("two rows resolved to %q", got)
	}
	if note != "" {
		t.Fatalf("an aligned selection reported a widening: %s", note)
	}
	tok := quote.Format(r)
	if !strings.HasPrefix(tok, "<412.0:0-") || !strings.HasSuffix(tok, ">!") {
		t.Fatalf("token = %q", tok)
	}
}

// v over a few columns names exactly those runes.
func TestVisualRange_CharModeNamesTheColumns(t *testing.T) {
	tr, body := sourceFixture(t)
	tr.key('v')
	stepTo(t, tr, "The retry")
	line, _ := tr.visualLineOf(tr.visual.cursor)
	plain := []rune(render.StripEscapes(tr.lineText(line)))
	first := 0
	for first < len(plain) && plain[first] == ' ' {
		first++
	}
	for range first + 4 { // onto "retry"
		tr.key('l')
	}
	tr.key('v') // anchor
	for range 4 {
		tr.key('l')
	}
	r, _, err := tr.visualRange()
	if err != "" {
		t.Fatalf("visualRange: %s", err)
	}
	got := string([]rune(body)[r.Start.Offset:r.End.Offset])
	if got != "retry" {
		t.Fatalf("five columns over 'retry' resolved to %q (range %+v)", got, r)
	}
}

// The question is quotable at the turn's opening LT once the turn is sealed.
func TestVisualRange_InquiryUsesTheTurnsFirstLT(t *testing.T) {
	tr, _ := sourceFixture(t)
	tr.key('V')
	stepTo(t, tr, "stampede?") // the question's row
	tr.key('V')
	r, _, err := tr.visualRange()
	if err != "" {
		t.Fatalf("visualRange over the question: %s", err)
	}
	if r.Start.LT != 400 {
		t.Fatalf("question resolved to lt %d, want 400", r.Start.LT)
	}
}

// A folded tool shows its output's tail; the coordinate is offset back into
// the whole block.
func TestVisualRange_ToolTailIsOffsetIntoTheBlock(t *testing.T) {
	tr, _ := sourceFixture(t)
	tr.key('V')
	stepTo(t, tr, "l2")
	tr.key('V')
	r, _, err := tr.visualRange()
	if err != "" {
		t.Fatalf("visualRange over a tool: %s", err)
	}
	if r.Start.LT != 414 {
		t.Fatalf("tool output resolved to lt %d, want the result's 414", r.Start.LT)
	}
	out := []rune("l1\nl2\nl3")
	if got := string(out[r.Start.Offset:r.End.Offset]); got != "l2" {
		t.Fatalf("tool row resolved to %q, want l2", got)
	}
}

// `:<,>send -- text` reaches the runner as `send -- <lt.block:a-b>! text`: the
// range moves from before the verb to the head of the prompt.
func TestVisual_ColonRangeExpandsIntoThePrompt(t *testing.T) {
	tr, _ := sourceFixture(t)
	var got string
	tr.command = func(line string) { got = line }
	tr.key('V')
	stepTo(t, tr, "The retry")
	tr.key('V')
	tr.key(':')
	for _, r := range "send -- what about this?" {
		tr.key(byte(r))
	}
	tr.key('\r')
	if !strings.HasPrefix(got, "send -- <412.0:0-") || !strings.HasSuffix(got, ">! what about this?") {
		t.Fatalf("runner received %q", got)
	}
	// Submitting leaves visual mode entirely and keeps the place.
	if tr.visual.active() {
		t.Fatal("the mode outlived the command it was spent on")
	}
	if tr.follow {
		t.Fatal("a plain submit re-followed the tail; only M-Enter snaps")
	}
}

func TestVisual_ColonRangeRefusals(t *testing.T) {
	tr, _ := sourceFixture(t)
	ran := false
	tr.command = func(string) { ran = true }
	// No highlight at all.
	tr.key(':')
	for _, r := range "<,>send -- x" {
		tr.key(byte(r))
	}
	tr.key('\r')
	if ran || !strings.Contains(tr.jumpNote, "no highlight") {
		t.Fatalf("ran=%v note=%q", ran, tr.jumpNote)
	}
	// A cursor but no highlight: ':' is the plain box, and a typed range
	// still refuses.
	tr.key('V')
	stepTo(t, tr, "The retry")
	tr.key(':')
	if tr.cmdline.String() != "" {
		t.Fatalf("':' with only a cursor preloaded %q", tr.cmdline.String())
	}
	for _, r := range "<,>send -- x" {
		tr.key(byte(r))
	}
	tr.key('\r')
	if ran || !strings.Contains(tr.jumpNote, "no highlight") {
		t.Fatalf("ran=%v note=%q", ran, tr.jumpNote)
	}
	// A highlight, but no prompt after --.
	tr.key('V')
	tr.key(':')
	for _, r := range "send" {
		tr.key(byte(r))
	}
	tr.key('\r')
	if ran || !strings.Contains(tr.jumpNote, "--") {
		t.Fatalf("ran=%v note=%q", ran, tr.jumpNote)
	}
}

// A qualified token typed by hand in the range's position is moved the same
// way the placeholder is.
func TestExpandRange_HandTypedToken(t *testing.T) {
	tr, _ := sourceFixture(t)
	got, _, err := tr.expandRange("<412.0:3-9>!send -- hello")
	if err != "" || got != "send -- <412.0:3-9>! hello" {
		t.Fatalf("got %q err %q", got, err)
	}
	if got, _, err := tr.expandRange("send -- <412.0:3-9>! hello"); err != "" || got != "send -- <412.0:3-9>! hello" {
		t.Fatalf("a token already in place moved: %q %q", got, err)
	}
}
