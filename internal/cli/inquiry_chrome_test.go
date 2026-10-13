package cli

import (
	"strings"
	"testing"
	"time"

	"github.com/jack-work/figaro/api/livedoc"
	ldrender "github.com/jack-work/figaro/internal/livelog/render"
	"github.com/jack-work/figaro/sdk/aria"
)

// The block is "> input" / text / blank / rule / "< figaro", drawn by hand at
// three sites; the heading sits on its first line because the sticky header
// pins those two rows.
func TestInquiryChromeAgreesAcrossViews(t *testing.T) {
	const question = "THEQUESTION"
	nodes := []livedoc.Node{{Type: livedoc.NodeProse, Markdown: "THEANSWER"}}
	want := []string{"> input", question, "", "─", "< figaro", "", "THEANSWER"}

	t.Run("show", func(t *testing.T) {
		got := renderTurnRows(aria.Message{Role: livedoc.RoleOutput, Inquiry: question, InquirySegments: nil, Nodes: nodes}, 48, 0, renderSettings{})
		assertChrome(t, got, want)
	})

	t.Run("pager", func(t *testing.T) {
		client := aria.NewClient()
		client.Apply(aria.Page{Parts: []aria.TurnPart{
			{Turn: aria.Turn{ID: 1, Inquiry: question, Sealed: true, Nodes: nodes}},
		}}, aria.Notify)
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

// In want, "" is a blank row and "─" a rule row.
func assertChrome(t *testing.T, rows []string, want []string) {
	t.Helper()
	var shape []string
	for _, r := range rows {
		shape = append(shape, classifyChromeRow(r))
	}
	start := -1
	for i := range shape {
		if strings.HasPrefix(shape[i], "> ") {
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

func classifyChromeRow(row string) string {
	s := strings.TrimSpace(stripANSI(row))
	switch {
	case s == "":
		return ""
	case strings.Trim(s, "─") == "":
		return "─"
	case strings.HasPrefix(s, ">"):
		return s
	case strings.HasPrefix(s, "<"):
		return "< figaro"
	}
	return s
}

// No surface may put a blank row between a bare rule and the voice header it
// introduces; the assistant bookend ends in status text, not a rule, so its
// trailing blank stays.
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
		}}, aria.Notify)
		ft := ldrender.NewFakeTerminal(48, 40)
		tr := newTranscript(ft, 48, 40, &ariaView{settings: &renderSettings{}}, client, "aria1234", time.Unix(0, 0))
		tr.enter()
		tr.follow = false
		assertNoGapBelowRule(t, "transcript", tr.lines())
	})

	// A second turn covers the boundary after an assistant bookend.
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

	// The pager-exit path reprints closed messages and must reproduce the seams.
	t.Run("inline-resume", func(t *testing.T) {
		ft, in := chromeIncipit(48, 40)
		in.OpenRule()
		in.Resume([]aria.Message{
			{Turn: 1, Inquiry: "QUESTIONONE", Role: livedoc.RoleOutput, Nodes: nodes1},
			{Turn: 2, Inquiry: "QUESTIONTWO", Role: livedoc.RoleOutput, Nodes: nodes2},
		}, nil, 0)
		assertNoGapBelowRule(t, "incipit resume", ft.Screen())
	})
}

// The real bookend matters: its trailing status row is what makes the blank
// after an assistant message correct.
func chromeIncipit(w, h int) (*ldrender.FakeTerminal, *ldrender.Incipit) {
	ft := ldrender.NewFakeTerminal(w, h)
	in := ldrender.NewIncipit(ft, &ariaView{settings: &renderSettings{}})
	in.Header = messageHeader
	in.Rule = func() string { return strings.Repeat("─", w) }
	in.Bookend = func() []string { return []string{strings.Repeat("─", w), "STATUSLINE"} }
	return ft, in
}

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
			continue
		}
		if shape[j] == "> input" || shape[j] == "< figaro" {
			t.Fatalf("%s: %d blank row(s) between the rule at line %d and %q at line %d;\n"+
				"the rule is that header's OVERLINE and nothing may come between them\n--- shape ---\n%s",
				where, j-i-1, i+1, shape[j], j+1, strings.Join(shape, "\n"))
		}
	}
}
