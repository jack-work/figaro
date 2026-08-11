package cli

import (
	"strings"
	"testing"

	"github.com/mattn/go-runewidth"

	"github.com/jack-work/figaro/internal/livedoc"
)

// tallTableMarkdown is a table that needs well over a dozen physical rows once
// its cells wrap.
func tallTableMarkdown() string {
	var b strings.Builder
	b.WriteString("| state | meaning |\n|---|---|\n")
	for _, r := range [][2]string{
		{"dormant", "not loaded in memory; nothing is running and the aria costs nothing to keep"},
		{"idle", "loaded into memory with an empty inbox, so no turn is in flight just now"},
		{"active", "the inbox is non-empty, which means a turn is being worked right now"},
		{"sealed", "the trunk was cauterized, so it accepts no further prompts at all"},
	} {
		b.WriteString("| " + r[0] + " | " + r[1] + " |\n")
	}
	return b.String()
}

func proseNode(md string) livedoc.Node {
	return livedoc.Node{Type: livedoc.NodeProse, Markdown: md}
}

// TestNodeExpandable is the predicate the pager's gesture asks before it
// toggles: true exactly when toggling would reveal something.
func TestNodeExpandable(t *testing.T) {
	const w = 40
	cases := []struct {
		name string
		n    livedoc.Node
		want bool
	}{
		{"tool with output", livedoc.Node{Type: livedoc.NodeTool, Name: "bash", Output: "one\ntwo"}, true},
		{"tool without output", livedoc.Node{Type: livedoc.NodeTool, Name: "bash"}, false},
		// Every one of these used to be true, on account of the table clamp.
		{"prose with a tall table", proseNode(tallTableMarkdown()), false},
		{"thinking with a tall table", livedoc.Node{Type: livedoc.NodeThinking, Markdown: tallTableMarkdown()}, false},
		{"steering with a tall table", livedoc.Node{Type: livedoc.NodeSteering, Markdown: tallTableMarkdown()}, false},
		{"ordinary prose, however long", proseNode(strings.Repeat("words and more words. ", 40)), false},
		{"empty prose", proseNode("   "), false},
	}
	for _, tc := range cases {
		if got := nodeExpandable(tc.n); got != tc.want {
			t.Errorf("%s: nodeExpandable = %v, want %v\nrender:\n%s",
				tc.name, got, tc.want, dumpRows(renderNode(tc.n, w, nodeBashCapDefault, 0, false, false)))
		}
	}
}

// TestNodeExpandable_AgreesWithTheRender is the anti-lie test: a predicate that
// DENIES expandability while the two renders differ makes the gesture drop a
// form the user could have seen. The converse (true while they match) is
// asserted for non-tools only, a tool with output shorter than the cap reports
// expandable although nothing changes, which is longstanding.
func TestNodeExpandable_AgreesWithTheRender(t *testing.T) {
	nodes := []livedoc.Node{
		proseNode(tallTableMarkdown()),
		proseNode("| a | b |\n|---|---|\n| 1 | 2 |\n"),
		proseNode("just a paragraph, wrapping over several lines because it is long enough to"),
		{Type: livedoc.NodeThinking, Markdown: tallTableMarkdown()},
		{Type: livedoc.NodeSteering, Markdown: tallTableMarkdown()},
		{Type: livedoc.NodeTool, Name: "bash", Output: strings.Repeat("a line of output\n", 40)},
		{Type: livedoc.NodeTool, Name: "bash", Output: "one short line"},
		{Type: livedoc.NodeTool, Name: "bash"},
	}
	for _, w := range []int{26, 40, 60, 80, 120} {
		for _, n := range nodes {
			collapsed := renderNode(n, w, nodeBashCapDefault, 0, false, false)
			expanded := renderNode(n, w, nodeOutputUnlimited, 0, false, false)
			differ := len(collapsed) != len(expanded)
			got := nodeExpandable(n)
			if !got && differ {
				t.Errorf("%s @ width %d: nodeExpandable = false but collapsed(%d)/expanded(%d) differ",
					n.Type, w, len(collapsed), len(expanded))
			}
			if n.Type != livedoc.NodeTool && got {
				t.Errorf("%s @ width %d: a non-tool reported expandable; only tools have a second form", n.Type, w)
			}
		}
	}
}

// TestSurfaceContract_NoSurfaceCollapsesProse pins that every surface draws
// prose the same: the transcript's expansion state reaches tools only.
func TestSurfaceContract_NoSurfaceCollapsesProse(t *testing.T) {
	n := proseNode(tallTableMarkdown())
	const w = 40
	view := &ariaView{settings: &renderSettings{}}

	full := len(renderProseNode(n, w))

	if got := len(view.Render(n, w, 0)); got != full {
		t.Errorf("incipit: %d rows, want the full %d", got, full)
	}
	if got := len(view.RenderExpanded(n, w, 0, false)); got != full {
		t.Errorf("transcript collapsed prose: %d rows, want the full %d", got, full)
	}
	if got := len(view.RenderExpanded(n, w, 0, true)); got != full {
		t.Errorf("transcript expanded prose to something else: %d rows, want %d", got, full)
	}
	if got := len(renderNodeList([]livedoc.Node{n}, w, 0, renderSettings{})); got != full {
		t.Errorf("show: %d rows, want the full %d", got, full)
	}
	if got := len(renderNodeList([]livedoc.Node{n}, w, 0, renderSettings{verbose: true})); got != full+1 {
		// +1: under -o `show` draws the block's coordinate row above it, the
		// same row Ctrl-O draws in the pager. Metadata is added; nothing is
		// taken away.
		t.Errorf("show -o: %d rows, want the full %d plus its coordinate row", got, full)
	}
}

// TestProseRows_HoldPainterInvariant guards architecture.md invariant #1 over
// the rows a table produces: one physical line per row, never past the edge.
func TestProseRows_HoldPainterInvariant(t *testing.T) {
	nodes := []livedoc.Node{
		proseNode(tallTableMarkdown()),
		{Type: livedoc.NodeThinking, Markdown: tallTableMarkdown()},
		{Type: livedoc.NodeSteering, Markdown: tallTableMarkdown()},
	}
	for w := 26; w <= 120; w += 3 {
		for _, n := range nodes {
			for i, row := range renderNode(n, w, nodeBashCapDefault, 0, false, false) {
				if strings.ContainsAny(row, "\n\r\t") {
					t.Fatalf("%s @ w=%d: row %d smuggles a control char: %q", n.Type, w, i, row)
				}
				if got := runewidth.StringWidth(stripANSI(row)); got > w {
					t.Fatalf("%s @ w=%d: row %d is %d columns: %q", n.Type, w, i, got, stripANSI(row))
				}
			}
		}
	}
}

func dumpRows(rows []string) string {
	var b strings.Builder
	for _, r := range rows {
		b.WriteString("    |" + stripANSI(r) + "|\n")
	}
	return b.String()
}
