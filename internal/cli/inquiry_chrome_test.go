package cli

import (
	"strings"
	"testing"
	"time"

	"github.com/jack-work/figaro/internal/livedoc"
	"github.com/jack-work/figaro/internal/livelog/aria"
	ldrender "github.com/jack-work/figaro/internal/livelog/render"
)

// THE CHROME AROUND THE QUESTION IS PART OF THE QUESTION.
//
// When the inquiry was a NODE it travelled as its own message and got the
// blank lines and the closing rule for free — the rule was that message's
// closer. As TEXT it is drawn by hand at three sites (inline incipit, the
// pager, `fig show`), and the first cut of that drew the header and the text
// and nothing else: the reply ran straight into the question with no rule
// between the voices.
//
// The block is: "❯ input" / blank / indented text / blank / RULE / "‹ figaro".
// All three surfaces must agree on it, because a live-vs-committed difference
// here is not cosmetics — it is the same exchange telling two stories.
func TestInquiryChromeAgreesAcrossViews(t *testing.T) {
	const question = "THEQUESTION"
	nodes := []livedoc.Node{{Type: livedoc.NodeProse, Markdown: "THEANSWER"}}
	want := []string{"❯ input", "", question, "", "─", "‹ figaro", "", "THEANSWER"}

	t.Run("show", func(t *testing.T) {
		got := renderTurnRows(question, nodes, 48, 0, 0, renderSettings{})
		assertChrome(t, got, want)
	})

	t.Run("pager", func(t *testing.T) {
		client := aria.NewClient()
		client.Apply(aria.Page{Parts: []aria.TurnPart{
			{Turn: aria.Turn{ID: 1, Inquiry: question, Sealed: true, Nodes: nodes}},
		}})
		ft := ldrender.NewFakeTerminal(48, 20)
		tr := newTranscript(ft, 48, 20, &ariaView{settings: &renderSettings{}}, client, "aria1234", time.Unix(0, 0))
		tr.enter()
		tr.follow = false
		assertChrome(t, tr.lines(), want)
	})

	t.Run("inline", func(t *testing.T) {
		ft := ldrender.NewFakeTerminal(48, 20)
		in := ldrender.NewIncipit(ft, &ariaView{settings: &renderSettings{}})
		in.Header = messageHeader
		in.Rule = func() string { return strings.Repeat("─", 48) }
		m := aria.Message{Turn: 1, Inquiry: question, Role: livedoc.RoleOutput, Nodes: nodes}
		in.Open(m)
		in.Freeze(m)
		assertChrome(t, ft.Screen(), want)
	})
}

// assertChrome checks that want appears in rows in order, with the blanks
// exactly where want says — a row is "blank" iff it holds no printable text,
// and "─" matches a rule row (a run of box-drawing dashes and nothing else).
func assertChrome(t *testing.T, rows []string, want []string) {
	t.Helper()
	var shape []string
	for _, r := range rows {
		shape = append(shape, classifyChromeRow(r))
	}
	start := -1
	for i := range shape {
		if shape[i] == "❯ input" {
			start = i
			break
		}
	}
	if start < 0 {
		t.Fatalf("no input header in:\n%s", strings.Join(shape, "\n"))
	}
	got := shape[start:]
	if len(got) > len(want) {
		got = got[:len(want)]
	}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("inquiry chrome:\n got %q\nwant %q\n--- rows ---\n%s",
			got, want, strings.Join(shape, "\n"))
	}
}

// classifyChromeRow reduces a rendered row to the token the shape speaks in:
// "" for blank, "─" for a rule, the header verbatim, otherwise the row's text.
func classifyChromeRow(row string) string {
	s := strings.TrimSpace(stripANSI(row))
	switch {
	case s == "":
		return ""
	case strings.Trim(s, "─") == "":
		return "─"
	case strings.HasPrefix(s, "❯"):
		return "❯ input"
	case strings.HasPrefix(s, "‹"):
		return "‹ figaro"
	}
	return s
}
