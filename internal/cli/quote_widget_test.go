package cli

import (
	"strings"
	"testing"
	"time"

	"github.com/jack-work/figaro/api/livedoc"
	"github.com/jack-work/figaro/api/quote"
	"github.com/jack-work/figaro/internal/livelog/aria"
	ldrender "github.com/jack-work/figaro/internal/livelog/render"
)

const quotePassage = "Pronto. The barber works in three strokes: isolate the environment, " +
	"drive the real binary in a real pty, and assert on what the terminal kept rather than " +
	"on what the renderer returned. Everything else is decoration, and decoration is what " +
	"makes a green suite lie to you on the one evening it matters."

// quotedQuestion is a question as the daemon writes it: the quote block, then
// the reader's own words. It goes through quote.Block so the test cannot
// drift from the writer.
func quotedQuestion(t *testing.T, token, question string) string {
	t.Helper()
	r, _, err := quote.Parse(token)
	if err != nil {
		t.Fatal(err)
	}
	return quote.Block("> ", quote.Heading(r, "4939c266", 1, len(quotePassage)), quotePassage) + question
}

func mustMention(t *testing.T, inquiry string) quote.Mention {
	t.Helper()
	m, _, ok := quote.Mentioned(inquiry)
	if !ok {
		t.Fatalf("not a quote block: %q", inquiry)
	}
	return m
}

const quotedQuestionTail = "what did you mean by this?"

// quotedPagerAt is the pager over one turn whose question quotes a passage.
func quotedPagerAt(t *testing.T, width int) (*transcript, string) {
	t.Helper()
	inquiry := quotedQuestion(t, "<5.0:0-180>!", quotedQuestionTail)
	client := aria.NewClient()
	client.SetClosedLimit(transcriptTailLimit)
	client.Apply(aria.Page{Parts: []aria.TurnPart{{Turn: aria.Turn{
		ID: 1, Inquiry: inquiry, Sealed: true,
		Nodes: []livedoc.Node{{Type: livedoc.NodeProse, Markdown: "THEANSWER"}},
	}}}}, aria.Notify)
	tr := newTranscript(ldrender.NewFakeTerminal(width, 40), width, 40,
		&ariaView{settings: &renderSettings{}}, client, "aria1234", time.Unix(0, 0))
	tr.enter()
	tr.follow = false
	tr.buildIndex()
	return tr, inquiry
}

// selectQuestion puts the selection on the turn's opening question, which is
// what Enter acts on.
func selectQuestion(t *testing.T, tr *transcript, turn int, inquiry string) {
	t.Helper()
	p, ok := inquiryPoint(aria.Message{Turn: turn, Inquiry: inquiry})
	if !ok {
		t.Fatal("the question has no selection point")
	}
	tr.selection = nodeSelection{active: true, anchor: p, focus: p}
}

func gutterRows(rows []string) []string {
	var out []string
	for _, r := range rows {
		if strings.HasPrefix(r, strings.TrimSpace(quoteGutter)) {
			out = append(out, r)
		}
	}
	return out
}

func rowContaining(rows []string, s string) (int, bool) {
	for i, r := range rows {
		if strings.Contains(r, s) {
			return i, true
		}
	}
	return 0, false
}

// A quote is CONTEXT for the question under it, so the pager's default is a
// heading that names the passage, a few rows of it, and a count of the rest.
func TestQuoteCardIsBriefAndFoldsTheRest(t *testing.T) {
	tr, _ := quotedPagerAt(t, 72)
	rows := plainRows(tr.lines())
	head, ok := rowContaining(rows, quoteMark)
	if !ok {
		t.Fatalf("no quote heading in:\n%s", strings.Join(rows, "\n"))
	}
	if !strings.Contains(rows[head], "lt 5.0") {
		t.Fatalf("the heading must name the coordinate: %q", rows[head])
	}
	for _, loud := range []string{"aria 4939c266", "turn 1", "chars", "<5.0:0-180>!"} {
		if _, found := rowContaining(rows, loud); found {
			t.Fatalf("%q is detail a folded card did not ask for:\n%s", loud, strings.Join(rows, "\n"))
		}
	}
	if got := len(gutterRows(rows)); got != quoteBriefRows {
		t.Fatalf("folded shows %d rows of the passage, want %d:\n%s", got, quoteBriefRows, strings.Join(rows, "\n"))
	}
	if _, found := rowContaining(rows, quoteClose+" "); !found {
		t.Fatalf("a folded quote must say how much it folded:\n%s", strings.Join(rows, "\n"))
	}
	if _, found := rowContaining(rows, quotedQuestionTail); !found {
		t.Fatalf("the question itself is gone:\n%s", strings.Join(rows, "\n"))
	}
}

// ENTER OVER THE QUESTION OPENS THE CARD, the same gesture that opens a tool
// body, and closes it again. Nothing else reveals the rest of a passage.
func TestQuoteCardOpensOnEnter(t *testing.T) {
	tr, inquiry := quotedPagerAt(t, 72)
	selectQuestion(t, tr, 1, inquiry)

	if !tr.toggleSelectedNodes() {
		t.Fatal("Enter over a folded quote must have something to open")
	}
	open := plainRows(tr.lines())
	head, ok := rowContaining(open, quoteMark)
	if !ok {
		t.Fatalf("the card is gone:\n%s", strings.Join(open, "\n"))
	}
	for _, want := range []string{"lt 5.0", "aria 4939c266", "turn 1", "chars"} {
		if !strings.Contains(open[head], want) {
			t.Fatalf("an open heading must carry %q: %q", want, open[head])
		}
	}
	if !strings.Contains(strings.Join(gutterRows(open), " "), "evening it matters") {
		t.Fatalf("an open card shows the end of the passage:\n%s", strings.Join(open, "\n"))
	}
	if _, found := rowContaining(open, quoteClose+" "); found {
		t.Fatalf("nothing is folded, so nothing should offer to unfold:\n%s", strings.Join(open, "\n"))
	}

	if !tr.toggleSelectedNodes() {
		t.Fatal("Enter again must close it")
	}
	closed := plainRows(tr.lines())
	if _, found := rowContaining(closed, quoteClose+" "); !found {
		t.Fatalf("the card did not fold back:\n%s", strings.Join(closed, "\n"))
	}
}

// A question with no quote in it has nothing for Enter to open, so the key
// stays inert rather than flipping a flag that changes no row.
func TestEnterOverAPlainQuestionIsInert(t *testing.T) {
	client := aria.NewClient()
	client.Apply(aria.Page{Parts: []aria.TurnPart{{Turn: aria.Turn{
		ID: 1, Inquiry: "an ordinary question", Sealed: true,
		Nodes: []livedoc.Node{{Type: livedoc.NodeProse, Markdown: "THEANSWER"}},
	}}}}, aria.Notify)
	tr := newTranscript(ldrender.NewFakeTerminal(72, 40), 72, 40,
		&ariaView{settings: &renderSettings{}}, client, "aria1234", time.Unix(0, 0))
	tr.enter()
	tr.follow = false
	tr.buildIndex()
	selectQuestion(t, tr, 1, "an ordinary question")
	if tr.toggleSelectedNodes() {
		t.Fatal("a question with no card reported something to open")
	}
}

// The count on the closing row is the number of rows opening it reveals.
// Folded and open are each other's oracle: without the second column this
// test would assert that some number was printed, which any number satisfies.
func TestQuoteFoldCountIsWhatOpeningReveals(t *testing.T) {
	m := mustMention(t, quotedQuestion(t, "<5.0:0-180>!", quotedQuestionTail))
	const width = 72
	folded := plainRows(quoteRows(m, width, false))
	open := plainRows(quoteRows(m, width, true))
	hidden := len(gutterRows(open)) - len(gutterRows(folded))
	if hidden <= 0 {
		t.Fatalf("the fixture folds nothing, so it cannot report: %d vs %d rows",
			len(gutterRows(open)), len(gutterRows(folded)))
	}
	want := quoteClose + " " + plural(hidden, "row") + " more"
	if _, found := rowContaining(folded, want); !found {
		t.Fatalf("want a closing row saying %q:\n%s", want, strings.Join(folded, "\n"))
	}
}

// One card, three surfaces. A quote drawn only by the pager would be a second
// renderer for one representation, which is the defect class the shared
// composer exists to remove. `show` is a dump and opens it; the pager and the
// inline view fold it.
func TestQuoteCardAgreesAcrossViews(t *testing.T) {
	const width = 72
	inquiry := quotedQuestion(t, "<5.0:0-180>!", quotedQuestionTail)
	nodes := []livedoc.Node{{Type: livedoc.NodeProse, Markdown: "THEANSWER"}}
	folded := len(gutterRows(plainRows(quoteRows(mustMention(t, inquiry), width, false))))

	t.Run("pager", func(t *testing.T) {
		tr, _ := quotedPagerAt(t, width)
		rows := plainRows(tr.lines())
		if _, ok := rowContaining(rows, quoteMark); !ok {
			t.Fatalf("the pager drew no card:\n%s", strings.Join(rows, "\n"))
		}
		if got := len(gutterRows(rows)); got != folded {
			t.Fatalf("the pager shows %d rows of the passage, folded is %d", got, folded)
		}
	})

	t.Run("inline", func(t *testing.T) {
		ft := ldrender.NewFakeTerminal(width, 24)
		in := ldrender.NewIncipit(ft, &ariaView{settings: &renderSettings{}})
		in.Header = messageHeader
		in.InputHeader = inputHeader
		in.Quote = func(q quote.Mention, w int, expanded bool) []string { return quoteRows(q, w, expanded) }
		in.Rule = func() string { return strings.Repeat("─", width) }
		m := aria.Message{Turn: 1, Inquiry: inquiry, Role: livedoc.RoleOutput, Nodes: nodes}
		in.Open(m)
		in.Freeze(m)
		rows := plainRows(ft.Screen())
		if _, ok := rowContaining(rows, quoteMark); !ok {
			t.Fatalf("the inline view drew no card:\n%s", strings.Join(rows, "\n"))
		}
		if got := len(gutterRows(rows)); got != folded {
			t.Fatalf("the inline view shows %d rows of the passage, folded is %d", got, folded)
		}
	})

	t.Run("show", func(t *testing.T) {
		m := aria.Message{Role: livedoc.RoleOutput, Inquiry: inquiry, Nodes: nodes}
		rows := plainRows(renderTurnRows(m, width, 0, renderSettings{}))
		if _, ok := rowContaining(rows, quoteMark); !ok {
			t.Fatalf("show drew no card:\n%s", strings.Join(rows, "\n"))
		}
		// A DUMP HAS NO GESTURE, so it cannot fold: a reader of `show` who
		// could not see the rest of the passage would have no way to ask.
		if _, found := rowContaining(rows, quoteClose+" "); found {
			t.Fatalf("show folded a card nobody can open:\n%s", strings.Join(rows, "\n"))
		}
		if !strings.Contains(strings.Join(gutterRows(rows), " "), "evening it matters") {
			t.Fatalf("show must print the whole passage:\n%s", strings.Join(rows, "\n"))
		}
	})
}

// Prose that merely opens with a markdown blockquote is prose. The card is
// chrome naming a passage of the conversation, and inventing one over a line
// a reader typed by hand would be a lie about where those words came from.
func TestHandTypedBlockquoteIsNotACard(t *testing.T) {
	m := aria.Message{
		Role:    livedoc.RoleOutput,
		Inquiry: "> as you said earlier\n\nwhat about it?",
		Nodes:   []livedoc.Node{{Type: livedoc.NodeProse, Markdown: "THEANSWER"}},
	}
	rows := plainRows(renderTurnRows(m, 72, 0, renderSettings{}))
	if _, found := rowContaining(rows, quoteMark); found {
		t.Fatalf("hand-typed blockquote drew a card:\n%s", strings.Join(rows, "\n"))
	}
	if _, found := rowContaining(rows, "as you said earlier"); !found {
		t.Fatalf("the reader's own words are gone:\n%s", strings.Join(rows, "\n"))
	}
}

// A narrow pane still draws the card: the gutter costs four columns, and the
// passage wraps inside what is left rather than past the edge.
func TestQuoteCardAtNarrowWidth(t *testing.T) {
	const width = 36
	tr, _ := quotedPagerAt(t, width)
	rows := plainRows(tr.lines())
	if _, ok := rowContaining(rows, quoteMark); !ok {
		t.Fatalf("no card at %d columns:\n%s", width, strings.Join(rows, "\n"))
	}
	for _, r := range rows {
		if len([]rune(r)) > width {
			t.Fatalf("row of %d runes at width %d: %q", len([]rune(r)), width, r)
		}
	}
	if got := len(gutterRows(rows)); got != quoteBriefRows {
		t.Fatalf("narrow shows %d rows of the passage, want %d:\n%s", got, quoteBriefRows, strings.Join(rows, "\n"))
	}
}
