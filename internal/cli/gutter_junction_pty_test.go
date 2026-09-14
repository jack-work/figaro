package cli

import (
	"strings"
	"testing"

	"github.com/jack-work/figaro/api/aria"
	"github.com/jack-work/figaro/api/livedoc"
)

// Synthetic replay exercises the production renderer in a private terminal
// without a provider, credentials, or a daemon on the user's store.
func TestGutterJunctionPTY(t *testing.T) {
	if testing.Short() {
		t.Skip("drives replay through tmux")
	}
	t.Run("thinking-delta", func(t *testing.T) {
		p := newAdornPaneFor(t, aria.Page{Parts: []aria.TurnPart{{Turn: aria.Turn{
			ID: 1, Sealed: true, Inquiry: "what should happen here?",
			InquirySegments: []aria.InquirySegment{{Sender: "Gluck", Text: "what should happen here?"}},
			FormDeltas:      adornDeltas("system.forked_from", "", "e87bb154", "mode", "", "testing"),
			Nodes:           []livedoc.Node{gutterThinking()},
		}}}}, "Then check the tests")
		rows := p.rows()
		heading := p.only(t, rows, "Gluck")
		if !strings.HasPrefix(rows[heading], "> Gluck ⑂ e87bb154") || !strings.HasSuffix(rows[heading], "Δ") {
			t.Fatalf("inquiry heading lost metadata: %s", p.dump())
		}
		p.key("C-n") // question
		p.key("C-n") // thinking
		p.key("Enter")
		p.key("Escape")

		assertThinkingConnector := func(want string) {
			t.Helper()
			rows := p.rows()
			first := p.only(t, rows, "I should read")
			last := p.only(t, rows, "Then check")
			delta := p.only(t, rows, "mantra")
			if delta != last+2 || strings.TrimSpace(rows[last+1]) != want {
				t.Fatalf("connector should be %s below the thinking: %s", want, p.dump())
			}
			for _, row := range rows[first : last+1] {
				if !strings.HasPrefix(row, "  │") {
					t.Fatalf("extra spine alongside thinking text: %q", row)
				}
			}
		}
		assertThinkingConnector("Δ─╯")
		t.Logf("parked:\n%s", p.capture(true))
		p.key("C-n") // question
		p.key("C-n") // thinking
		p.key("C-n") // delta
		assertThinkingConnector("╭─╯")
		rows = p.rows()
		delta := p.only(t, rows, "mantra")
		if !strings.HasPrefix(rows[delta], "Δ mantra") {
			t.Fatalf("head did not follow the delta selection: %s", p.dump())
		}
		t.Logf("delta selected:\n%s", p.capture(true))
		p.key("Enter") // collapse
		if hasRow(p.rows(), "mantra") || hasRow(p.rows(), "╯") {
			t.Fatalf("closed list left a connector or delta behind: %s", p.dump())
		}
	})

	t.Run("sticky-rule", func(t *testing.T) {
		n := livedoc.Node{Type: livedoc.NodeThinking,
			Markdown: strings.Repeat("The gutter should meet the rule above it.\n\n", 40) + "GUTTERLAST"}
		p := newAdornPaneFor(t, aria.Page{Parts: []aria.TurnPart{{Turn: aria.Turn{
			ID: 1, Sealed: true, Inquiry: "keep the question above the scrolling text",
			InquirySegments: []aria.InquirySegment{{Sender: "Gluck", Text: "keep the question above the scrolling text"}},
			FormDeltas:      adornDeltas("system.forked_from", "", "e87bb154", "mode", "", "testing"),
			Nodes:           []livedoc.Node{n},
		}}}}, "GUTTERLAST")
		p.key("C-n")
		p.key("Escape")
		p.key("g")
		p.key("g")
		p.key("d") // page into the thinking, away from the question
		check := func() {
			t.Helper()
			rows := p.rows()
			if !strings.HasPrefix(rows[0], "∨ Gluck ⑂ e87bb154") || !strings.HasSuffix(rows[0], "1 Δ") || !strings.Contains(rows[1], "keep the question") {
				t.Fatalf("fixture has no pinned question: %s", p.dump())
			}
			if !strings.HasPrefix(rows[2], "──┬─") || !strings.HasPrefix(rows[3], "  │") {
				t.Fatalf("rule and gutter do not meet: %s", p.dump())
			}
		}
		check()
		t.Logf("sticky junction:\n%s", p.capture(true))
		p.key("j")
		check()
		p.tmux("resize-window", "-t", "0", "-x", "60", "-y", "41")
		p.w, p.h = 60, p.height()
		p.settle()
		check()
		t.Logf("after resize:\n%s", p.capture(true))
		p.key("g")
		p.key("g")
		if hasRow(p.rows(), "┬") {
			t.Fatalf("junction survived after the header let go: %s", p.dump())
		}
	})
}
