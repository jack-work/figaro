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

func quoteRowsOf(t *testing.T, width int, set renderSettings) []string {
	t.Helper()
	nodes := []livedoc.Node{{Type: livedoc.NodeProse, Markdown: "THEANSWER"}}
	m := aria.Message{
		Role:    livedoc.RoleOutput,
		Inquiry: quotedQuestion(t, "<5.0:0-180>!", "what did you mean by this?"),
		Nodes:   nodes,
	}
	return plainRows(renderTurnRows(m, width, 0, set))
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

// A quote is CONTEXT for the question under it, so the default is a heading
// that names the passage, a few rows of it, and a count of what is folded.
func TestQuoteCardIsBriefAndFoldsTheRest(t *testing.T) {
	rows := quoteRowsOf(t, 72, renderSettings{})
	head, ok := rowContaining(rows, quoteMark)
	if !ok {
		t.Fatalf("no quote heading in:\n%s", strings.Join(rows, "\n"))
	}
	if !strings.Contains(rows[head], "lt 5.0") {
		t.Fatalf("the heading must name the coordinate: %q", rows[head])
	}
	for _, loud := range []string{"aria 4939c266", "turn 1", "chars", "<5.0:0-180>!"} {
		if _, found := rowContaining(rows, loud); found {
			t.Fatalf("%q is detail a brief reader did not ask for:\n%s", loud, strings.Join(rows, "\n"))
		}
	}
	body := gutterRows(rows)
	if len(body) != quoteBriefRows {
		t.Fatalf("brief shows %d rows of the passage, want %d:\n%s", len(body), quoteBriefRows, strings.Join(rows, "\n"))
	}
	if _, found := rowContaining(rows, quoteClose+" "); !found {
		t.Fatalf("a folded quote must say how much it folded:\n%s", strings.Join(rows, "\n"))
	}
	if _, found := rowContaining(rows, "what did you mean by this?"); !found {
		t.Fatalf("the question itself is gone:\n%s", strings.Join(rows, "\n"))
	}
}

// The count on the closing row is the number of rows the toggle reveals.
// Brief and verbose are each other's oracle: without the second column this
// test would assert that some number was printed, which any number satisfies.
func TestQuoteFoldCountIsWhatTheToggleReveals(t *testing.T) {
	brief := quoteRowsOf(t, 72, renderSettings{})
	full := quoteRowsOf(t, 72, renderSettings{verbose: true})
	hidden := len(gutterRows(full)) - len(gutterRows(brief))
	if hidden <= 0 {
		t.Fatalf("the fixture folds nothing, so it cannot report: %d vs %d rows",
			len(gutterRows(full)), len(gutterRows(brief)))
	}
	want := quoteClose + " " + plural(hidden, "row") + " more"
	if _, found := rowContaining(brief, want); !found {
		t.Fatalf("want a closing row saying %q:\n%s", want, strings.Join(brief, "\n"))
	}
}

// M-m is the toggle that puts addresses and timings on the screen; it opens
// the quote too, heading and all.
func TestQuoteCardOpensUnderVerbose(t *testing.T) {
	rows := quoteRowsOf(t, 72, renderSettings{verbose: true})
	head, ok := rowContaining(rows, quoteMark)
	if !ok {
		t.Fatalf("no quote heading in:\n%s", strings.Join(rows, "\n"))
	}
	for _, want := range []string{"lt 5.0", "aria 4939c266", "turn 1", "chars"} {
		if !strings.Contains(rows[head], want) {
			t.Fatalf("the open heading must carry %q: %q", want, rows[head])
		}
	}
	if _, found := rowContaining(rows, quoteClose+" "); found {
		t.Fatalf("nothing is folded, so nothing should offer to unfold:\n%s", strings.Join(rows, "\n"))
	}
	if !strings.Contains(strings.Join(gutterRows(rows), " "), "evening it matters") {
		t.Fatalf("the open card must show the end of the passage:\n%s", strings.Join(rows, "\n"))
	}
}

// One card, three surfaces. A quote drawn only by `show` would be a second
// renderer for one representation, which is the defect class the shared
// composer exists to remove.
func TestQuoteCardAgreesAcrossViews(t *testing.T) {
	inquiry := quotedQuestion(t, "<5.0:0-180>!", "what did you mean by this?")
	nodes := []livedoc.Node{{Type: livedoc.NodeProse, Markdown: "THEANSWER"}}
	const width = 72

	show := quoteRowsOf(t, width, renderSettings{})
	wantHead, _ := rowContaining(show, quoteMark)

	t.Run("pager", func(t *testing.T) {
		client := aria.NewClient()
		client.Apply(aria.Page{Parts: []aria.TurnPart{{
			Turn: aria.Turn{ID: 1, Inquiry: inquiry, Sealed: true, Nodes: nodes},
		}}}, aria.Notify)
		ft := ldrender.NewFakeTerminal(width, 24)
		tr := newTranscript(ft, width, 24, &ariaView{settings: &renderSettings{}}, client, "aria1234", time.Unix(0, 0))
		tr.enter()
		tr.follow = false
		rows := plainRows(tr.lines())
		if _, ok := rowContaining(rows, quoteMark); !ok {
			t.Fatalf("the pager drew no card:\n%s", strings.Join(rows, "\n"))
		}
		if got := len(gutterRows(rows)); got != len(gutterRows(show)) {
			t.Fatalf("pager shows %d rows of the passage, show shows %d", got, len(gutterRows(show)))
		}
	})

	t.Run("inline", func(t *testing.T) {
		ft := ldrender.NewFakeTerminal(width, 24)
		in := ldrender.NewIncipit(ft, &ariaView{settings: &renderSettings{}})
		in.Header = messageHeader
		in.InputHeader = inputHeader
		in.Quote = func(q quote.Mention, w int) []string { return quoteRows(q, w, false) }
		in.Rule = func() string { return strings.Repeat("─", width) }
		m := aria.Message{Turn: 1, Inquiry: inquiry, Role: livedoc.RoleOutput, Nodes: nodes}
		in.Open(m)
		in.Freeze(m)
		rows := plainRows(ft.Screen())
		if _, ok := rowContaining(rows, quoteMark); !ok {
			t.Fatalf("the inline view drew no card:\n%s", strings.Join(rows, "\n"))
		}
	})

	if wantHead < 0 {
		t.Fatal("the show fixture itself drew no card")
	}
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
	rows := quoteRowsOf(t, width, renderSettings{})
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
