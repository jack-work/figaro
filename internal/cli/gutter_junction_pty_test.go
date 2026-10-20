package cli

import (
	"strings"
	"testing"

	"github.com/jack-work/figaro/api/aria"
	"github.com/jack-work/figaro/api/livedoc"
)

func TestGutterJunctionPTY(t *testing.T) {
	if testing.Short() {
		t.Skip("drives replay through tmux")
	}
	t.Run("thinking-delta", func(t *testing.T) {
		p := newAdornPaneFor(t, aria.Page{Parts: []aria.TurnPart{{Turn: aria.Turn{
			ID: 1, Sealed: true, Inquiry: "what should happen here?",
			InquirySegments: []aria.InquirySegment{{Sender: "Duke", Text: "what should happen here?"}},
			FormDeltas:      adornDeltas("system.forked_from", "", "e87bb154", "mode", "", "testing"),
			Nodes:           []livedoc.Node{gutterThinking()},
		}}}}, "Then check the tests")
		rows := p.rows()
		heading := p.only(t, rows, "Duke")
		if !strings.HasPrefix(rows[heading], "> Duke ⑂ "+ariaName("e87bb154")) || !strings.HasSuffix(rows[heading], "Δ") {
			t.Fatalf("inquiry heading lost metadata: %s", p.dump())
		}
		p.key("C-n")
		p.key("C-n")
		p.key("Enter")
		p.key("Escape")

		assertThinkingConnector := func(want string) {
			t.Helper()
			rows := p.rows()
			first := p.only(t, rows, "I should read")
			last := p.only(t, rows, "Then check")
			delta := p.only(t, rows, "mantra")
			if delta != last+3 || strings.TrimSpace(rows[last+1]) != want || !strings.Contains(rows[last+2], "address") {
				t.Fatalf("connector should be %s below the thinking: %s", want, p.dump())
			}
			for _, row := range rows[first : last+1] {
				if !strings.HasPrefix(row, "  │") {
					t.Fatalf("extra spine alongside thinking text: %q", row)
				}
			}
		}
		assertThinkingConnector(headGlyph + "─╯")
		t.Logf("parked:\n%s", p.capture(true))
		p.key("C-n")
		p.key("C-n")
		p.key("C-n")
		p.key("C-n")
		assertThinkingConnector("╭─╯")
		rows = p.rows()
		delta := p.only(t, rows, "mantra")
		if !strings.HasPrefix(rows[delta], deltaGlyph+" mantra") {
			t.Fatalf("head did not follow the delta selection: %s", p.dump())
		}
		t.Logf("delta selected:\n%s", p.capture(true))
		p.key("Enter")
		if !hasRow(p.rows(), "mantra") || !hasRow(p.rows(), "+ checking the tests") {
			t.Fatalf("Enter on a delta row must open that row and keep the list: %s", p.dump())
		}
		p.key("Enter")
		p.key("C-p")
		p.key("C-p")
		p.key("Enter")
		if hasRow(p.rows(), "mantra") || hasRow(p.rows(), "╯") {
			t.Fatalf("closed list left a connector or delta behind: %s", p.dump())
		}
	})

	t.Run("sticky-rule", func(t *testing.T) {
		n := livedoc.Node{Type: livedoc.NodeThinking,
			Markdown: strings.Repeat("The gutter should meet the rule above it.\n\n", 40) + "GUTTERLAST"}
		p := newAdornPaneFor(t, aria.Page{Parts: []aria.TurnPart{{Turn: aria.Turn{
			ID: 1, Sealed: true, Inquiry: "keep the question above the scrolling text",
			InquirySegments: []aria.InquirySegment{{Sender: "Duke", Text: "keep the question above the scrolling text"}},
			FormDeltas:      adornDeltas("system.forked_from", "", "e87bb154", "mode", "", "testing"),
			Nodes:           []livedoc.Node{n},
		}}}}, "GUTTERLAST")
		p.key("C-n")
		p.key("Escape")
		p.key("g")
		p.key("g")
		p.key("d")
		check := func() {
			t.Helper()
			rows := p.rows()
			if !strings.HasPrefix(rows[0], "∨ Duke ⑂ "+ariaName("e87bb154")) || !strings.HasSuffix(rows[0], "1 Δ") || !strings.Contains(rows[1], "keep the question") {
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
		p.tmux("resize-window", "-t", "0", "-x", "60", "-y", "40")
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

// A fenced diff in tool output arrives painted, and the floor rule is cut by
// a gutter that runs past it.
func TestFencedDiffAndFloorJunctionPTY(t *testing.T) {
	if testing.Short() {
		t.Skip("drives replay through tmux")
	}
	diff := "$ git diff retry.py\n```diff\n--- a/retry.py\n+++ b/retry.py\n@@ -1,2 +1,2 @@\n-    for i in range(retries):\n+    for i in range(retries + 1):\n```\n1 file changed"
	p := newAdornPaneFor(t, aria.Page{Parts: []aria.TurnPart{{Turn: aria.Turn{
		ID: 1, Sealed: true, Inquiry: "what changed?",
		InquirySegments: []aria.InquirySegment{{Sender: "Duke", Text: "what changed?"}},
		Nodes: []livedoc.Node{
			{Type: livedoc.NodeTool, Name: "bash", Status: livedoc.StatusOK,
				Args: map[string]any{"command": "git diff retry.py"}, Output: diff},
			{Type: livedoc.NodeThinking,
				Markdown: strings.Repeat("The retry loop was off by one and now it is not.\n\n", 30)},
		},
	}}}}, "The retry loop was off by one")

	p.key("g")
	p.key("g")
	for _, r := range p.rows() {
		if strings.Contains(r, "```") {
			t.Fatalf("a fence reached the screen: %q\n%s", r, p.dump())
		}
	}
	styled := p.styled()
	del := p.only(t, p.rows(), "-    for i in range(retries):")
	add := p.only(t, p.rows(), "+    for i in range(retries + 1):")
	if !strings.Contains(styled[del], "\x1b[") || !strings.Contains(styled[add], "\x1b[") {
		t.Fatalf("the diff arrived unpainted:\n%q\n%q", styled[del], styled[add])
	}
	if styled[del] == styled[add] {
		t.Fatalf("both sides of the diff painted the same: %q", styled[del])
	}
	if rule := floorRuleRow(t, p); !strings.Contains(rule, "┴") {
		t.Fatalf("a block that outruns the pane must be cut by the floor: %q\n%s", rule, p.dump())
	}
	p.key("G")
	if rule := floorRuleRow(t, p); strings.Contains(rule, "┴") {
		t.Fatalf("the last row of a block joined the floor: %q\n%s", rule, p.dump())
	}
}

// floorRuleRow is the lowest rule on the pane; the status bar sits under it.
func floorRuleRow(t *testing.T, p *adornPane) string {
	t.Helper()
	rows := p.rows()
	for i := len(rows) - 1; i >= 0; i-- {
		if strings.HasPrefix(rows[i], "─") {
			return rows[i]
		}
	}
	t.Fatalf("no closing rule on the pane\n%s", p.dump())
	return ""
}
