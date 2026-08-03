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
// The block is: "> input" / blank / indented text / blank / RULE / "< figaro".
// All three surfaces must agree on it, because a live-vs-committed difference
// here is not cosmetics — it is the same exchange telling two stories.
func TestInquiryChromeAgreesAcrossViews(t *testing.T) {
	const question = "THEQUESTION"
	nodes := []livedoc.Node{{Type: livedoc.NodeProse, Markdown: "THEANSWER"}}
	want := []string{"> input", "", question, "", "─", "< figaro", "", "THEANSWER"}

	t.Run("show", func(t *testing.T) {
		got := renderTurnRows(aria.Message{Role: livedoc.RoleOutput, Inquiry: question, InquirySegments: nil, Nodes: nodes}, 48, 0, renderSettings{})
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
		if shape[i] == "> input" {
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
	case strings.HasPrefix(s, ">"):
		return "> input"
	case strings.HasPrefix(s, "<"):
		return "< figaro"
	}
	return s
}

// THE RULE IS THE OVERLINE OF THE HEADER BENEATH IT, EVERYWHERE.
//
// TestInquiryChromeAgreesAcrossViews above pins the seam INSIDE a message —
// question, blank, rule, "< figaro" hard against it. Nothing pinned the same
// seam at a message BOUNDARY, and that is exactly where it drifted: the pager
// separated two units with a blank/rule/blank triple and the inline renderer
// printed a static opening rule and then let the first message add its own
// leading blank, so "> input" sat one row lower than "< figaro" in both views.
//
// The asymmetry was a residue of e7fb039. When the inquiry was its own message,
// both of its seams were message boundaries and both were loose; once it became
// text on the turn, the seam below it moved inside a message and tightened
// while the seam above it stayed behind.
//
// So this asserts the invariant rather than one shape: no rendered surface may
// put a blank row between a bare rule and the voice header it introduces. The
// assistant bookend is untouched by it — that closer ends in status TEXT, not a
// rule, so the blank under it is not this pattern and stays.
func TestVoiceHeaderHugsItsRule(t *testing.T) {
	nodes1 := []livedoc.Node{{Type: livedoc.NodeProse, Markdown: "FIRSTANSWER"}}
	nodes2 := []livedoc.Node{{Type: livedoc.NodeProse, Markdown: "SECONDANSWER"}}

	t.Run("show", func(t *testing.T) {
		assertNoGapBelowRule(t, "renderTurnRows",
			renderTurnRows(aria.Message{Role: livedoc.RoleOutput, Inquiry: "QUESTIONONE", InquirySegments: nil, Nodes: nodes1}, 48, 0, renderSettings{}))
	})

	t.Run("pager", func(t *testing.T) {
		client := aria.NewClient()
		client.Apply(aria.Page{Parts: []aria.TurnPart{
			{Turn: aria.Turn{ID: 1, Inquiry: "QUESTIONONE", Sealed: true, Nodes: nodes1}},
			{Turn: aria.Turn{ID: 2, Inquiry: "QUESTIONTWO", Sealed: true, Nodes: nodes2}},
		}})
		ft := ldrender.NewFakeTerminal(48, 40)
		tr := newTranscript(ft, 48, 40, &ariaView{settings: &renderSettings{}}, client, "aria1234", time.Unix(0, 0))
		tr.enter()
		tr.follow = false
		assertNoGapBelowRule(t, "transcript", tr.lines())
	})

	// Inline: the opening rule then a turn, then a SECOND turn so the boundary
	// after an assistant bookend is covered too (that one keeps its blank).
	t.Run("inline", func(t *testing.T) {
		ft, in := chromeIncipit(48, 40)
		in.OpenRule()
		for i, q := range []string{"QUESTIONONE", "QUESTIONTWO"} {
			m := aria.Message{Turn: i + 1, Inquiry: q, Role: livedoc.RoleOutput, Nodes: nodes1}
			in.Open(m)
			in.Freeze(m)
		}
		assertNoGapBelowRule(t, "incipit", ft.Screen())
	})

	// Resume is the pager-exit path: it reprints closed messages to scrollback
	// and must reproduce the same seams, or leaving the pager would reintroduce
	// the gap the inline path just removed.
	t.Run("inline-resume", func(t *testing.T) {
		ft, in := chromeIncipit(48, 40)
		in.OpenRule()
		in.Resume([]aria.Message{
			{Turn: 1, Inquiry: "QUESTIONONE", Role: livedoc.RoleOutput, Nodes: nodes1},
			{Turn: 2, Inquiry: "QUESTIONTWO", Role: livedoc.RoleOutput, Nodes: nodes2},
		}, nil)
		assertNoGapBelowRule(t, "incipit resume", ft.Screen())
	})
}

// chromeIncipit builds an inline renderer with the real chrome hooks: the
// bookend matters, because its trailing status row is what makes the blank
// after an assistant message correct rather than a violation.
func chromeIncipit(w, h int) (*ldrender.FakeTerminal, *ldrender.Incipit) {
	ft := ldrender.NewFakeTerminal(w, h)
	in := ldrender.NewIncipit(ft, &ariaView{settings: &renderSettings{}})
	in.Header = messageHeader
	in.Rule = func() string { return strings.Repeat("─", w) }
	in.Bookend = func() []string { return []string{strings.Repeat("─", w), "STATUSLINE"} }
	return ft, in
}

// assertNoGapBelowRule fails if any bare rule is separated from the voice
// header it introduces by one or more blank rows.
func assertNoGapBelowRule(t *testing.T, where string, rows []string) {
	t.Helper()
	shape := make([]string, 0, len(rows))
	for _, r := range rows {
		shape = append(shape, classifyChromeRow(r))
	}
	for i := range shape {
		if shape[i] != "─" {
			continue
		}
		j := i + 1
		for j < len(shape) && shape[j] == "" {
			j++
		}
		if j == i+1 || j >= len(shape) {
			continue // hugged already, or nothing below
		}
		if shape[j] == "> input" || shape[j] == "< figaro" {
			t.Fatalf("%s: %d blank row(s) between the rule at line %d and %q at line %d;\n"+
				"the rule is that header's OVERLINE and nothing may come between them\n--- shape ---\n%s",
				where, j-i-1, i+1, shape[j], j+1, strings.Join(shape, "\n"))
		}
	}
}
