package cli

import (
	"strings"
	"testing"

	"github.com/mattn/go-runewidth"

	"github.com/jack-work/figaro/internal/livedoc"
	"github.com/jack-work/figaro/internal/render"
)

// tallTableMarkdown is a table that needs more than proseTableCapDefault rows
// once its cells wrap — i.e. exactly the case the collapse exists for.
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

// TestNodeExpandable is the predicate ROSINA's gesture asks before it toggles.
// It must be true exactly when toggling would reveal something.
func TestNodeExpandable(t *testing.T) {
	const w = 40
	cases := []struct {
		name string
		n    livedoc.Node
		want bool
	}{
		{"prose with a table taller than the cap", proseNode(tallTableMarkdown()), true},
		{"ordinary prose, however long", proseNode(strings.Repeat("words and more words. ", 40)), false},
		{"prose with a short table", proseNode("| a | b |\n|---|---|\n| 1 | 2 |\n"), false},
		{"empty prose", proseNode("   "), false},
		{"tool with output", livedoc.Node{Type: livedoc.NodeTool, Name: "bash", Output: "one\ntwo"}, true},
		{"tool without output", livedoc.Node{Type: livedoc.NodeTool, Name: "bash"}, false},
		{"thinking with a tall table", livedoc.Node{Type: livedoc.NodeThinking, Markdown: tallTableMarkdown()}, true},
	}
	for _, tc := range cases {
		if got := nodeExpandable(tc.n, w); got != tc.want {
			t.Errorf("%s: nodeExpandable = %v, want %v\ncollapsed render:\n%s",
				tc.name, got, tc.want, dumpRows(renderNode(tc.n, w, nodeBashCapDefault, 0, false, false)))
		}
	}
}

// TestNodeExpandable_AgreesWithTheRender is the anti-lie test. A predicate that
// says "expandable" while the two renders are identical would give ROSINA's
// click a silent no-op, which is the exact failure the predicate exists to
// prevent — so assert the two against each other rather than against a
// hand-written expectation.
func TestNodeExpandable_AgreesWithTheRender(t *testing.T) {
	nodes := []livedoc.Node{
		proseNode(tallTableMarkdown()),
		proseNode("| a | b |\n|---|---|\n| 1 | 2 |\n"),
		proseNode("just a paragraph, wrapping over several lines because it is long enough to"),
		{Type: livedoc.NodeThinking, Markdown: tallTableMarkdown()},
		{Type: livedoc.NodeSteering, Markdown: tallTableMarkdown()},
	}
	for _, w := range []int{26, 40, 60, 80, 120} {
		for _, n := range nodes {
			collapsed := renderNode(n, w, nodeBashCapDefault, 0, false, false)
			expanded := renderNode(n, w, nodeBashCapDefault, 0, false, true)
			differ := len(collapsed) != len(expanded)
			if got := nodeExpandable(n, w); got != differ {
				t.Errorf("%s @ width %d: nodeExpandable = %v but collapsed(%d rows)/expanded(%d rows) differ = %v",
					n.Type, w, got, len(collapsed), len(expanded), differ)
			}
		}
	}
}

// TestRenderProseNode_CollapseHidesAndExpandRestores is the deliverable: the
// collapsed form is shorter and says so, the expanded form is whole, and the
// collapsed form is a PREFIX-preserving subset — no content is invented and the
// header survives (a table without its header row is not a table).
func TestRenderProseNode_CollapseHidesAndExpandRestores(t *testing.T) {
	n := proseNode(tallTableMarkdown())
	const w = 40

	collapsed := renderProseNode(n, w, false)
	expanded := renderProseNode(n, w, true)

	if len(expanded) <= len(collapsed) {
		t.Fatalf("expanded (%d rows) must be taller than collapsed (%d rows)\n%s",
			len(expanded), len(collapsed), dumpRows(collapsed))
	}
	// The collapsed form must ADMIT what it hid; a silent truncation is the bug
	// we are fixing, not the fix.
	hint := stripANSI(collapsed[len(collapsed)-1])
	if !strings.Contains(hint, "…") || !strings.Contains(hint, "more table lines") {
		t.Errorf("collapsed form does not announce the hidden rows; last row = %q", hint)
	}
	// The header must survive the clamp.
	if head := stripANSI(collapsed[0]); !strings.Contains(head, "state") || !strings.Contains(head, "meaning") {
		t.Errorf("clamp ate the header row: %q", head)
	}
	// Expanded loses nothing: every word of every cell is there.
	full := " " + strings.Join(strings.Fields(wordsOnly(stripANSI(strings.Join(expanded, " ")))), " ") + " "
	for _, word := range []string{"dormant", "cauterized", "further", "prompts", "at", "all"} {
		if !strings.Contains(full, " "+word+" ") {
			t.Errorf("expanded render lost %q\n%s", word, dumpRows(expanded))
		}
	}
	// And the CANARY for the clamp itself: the collapsed form must actually be
	// missing something the expanded form has, or this test cannot fail.
	if strings.Contains(" "+strings.Join(strings.Fields(wordsOnly(stripANSI(strings.Join(collapsed, " ")))), " ")+" ", " cauterized ") {
		t.Errorf("collapsed form hid nothing — the clamp is inert\n%s", dumpRows(collapsed))
	}
}

// TestClampTables_HoldsPainterInvariant guards architecture.md invariant #1
// across the rows the clamp INTRODUCES. The hint row is the only row in a
// rendered node that this package writes itself, so it is the only one that
// could smuggle a newline or overrun the viewport.
func TestClampTables_HoldsPainterInvariant(t *testing.T) {
	nodes := []livedoc.Node{
		proseNode(tallTableMarkdown()),
		{Type: livedoc.NodeThinking, Markdown: tallTableMarkdown()},
		{Type: livedoc.NodeSteering, Markdown: tallTableMarkdown()},
	}
	for w := 26; w <= 120; w += 3 {
		for _, n := range nodes {
			for _, expanded := range []bool{false, true} {
				for i, row := range renderNode(n, w, nodeBashCapDefault, 0, false, expanded) {
					if strings.ContainsAny(row, "\n\r\t") {
						t.Fatalf("%s @ w=%d expanded=%v: row %d smuggles a control char: %q",
							n.Type, w, expanded, i, row)
					}
					if got := runewidth.StringWidth(stripANSI(row)); got > w {
						t.Fatalf("%s @ w=%d expanded=%v: row %d is %d columns: %q",
							n.Type, w, expanded, i, got, stripANSI(row))
					}
				}
			}
		}
	}
}

// TestClampTables_LeavesShortTablesAlone pins the no-op fast path: at any
// ordinary width a normal table is untouched, and the SAME SLICE comes back so
// the hot render path allocates nothing.
func TestClampTables_LeavesShortTablesAlone(t *testing.T) {
	rows := render.Prose("| a | b |\n|---|---|\n| 1 | 2 |\n", 60)
	out := clampTables(rows, proseTableCapDefault, 0)
	if len(out) != len(rows) {
		t.Fatalf("short table was clamped: %d -> %d rows", len(rows), len(out))
	}
	if len(rows) > 0 && &out[0] != &rows[0] {
		t.Errorf("no-op path reallocated; it must return the same backing array")
	}
	// And prose with no table at all.
	rows = render.Prose(strings.Repeat("a paragraph of words. ", 30), 60)
	if out := clampTables(rows, proseTableCapDefault, 0); &out[0] != &rows[0] {
		t.Errorf("table-free prose reallocated")
	}
}

// TestClampTables_Uncapped is the escape hatch: proseTableUncapped renders
// everything and, as a consequence, makes prose unexpandable.
func TestClampTables_Uncapped(t *testing.T) {
	rows := render.Prose(tallTableMarkdown(), 40)
	if out := clampTables(rows, proseTableUncapped, 0); len(out) != len(rows) {
		t.Errorf("uncapped clamp removed %d rows", len(rows)-len(out))
	}
}

func dumpRows(rows []string) string {
	var b strings.Builder
	for _, r := range rows {
		b.WriteString("    |" + stripANSI(r) + "|\n")
	}
	return b.String()
}

// wordsOnly reduces a row to bare words so a cell abutting its column rule, or
// carrying trailing punctuation, still matches.
func wordsOnly(s string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-':
			return r
		}
		return ' '
	}, s)
}

// TestSurfaceContract_OnlyTheTranscriptCollapses pins WHERE the collapsed form
// exists, which is the decision this branch had to make and got wrong once.
//
// The first answer was "the incipit expands with ^O", on the reasoning that ^O
// already re-renders the live unit. Driven in a real pty, it does not work: a
// table clamped to "… +4 more table lines" was still clamped after ^O, because
// its prose node had already been flushed and flushed nodes are frozen in
// scrollback (architecture.md invariant #2). A collapsed form nothing can
// un-collapse is not a preview, it is data loss.
//
// So: the incipit (ariaView.Render) and `show` (renderNodeList) never collapse;
// the transcript (ariaView.RenderExpanded, driven by t.expanded[ref]) does.
func TestSurfaceContract_OnlyTheTranscriptCollapses(t *testing.T) {
	n := proseNode(tallTableMarkdown())
	const w = 40
	view := &ariaView{settings: &renderSettings{}}

	full := len(renderProseNode(n, w, true))

	if got := len(view.Render(n, w, 0)); got != full {
		t.Errorf("incipit collapsed prose: %d rows, want the full %d", got, full)
	}
	if got := len(renderNodeList([]livedoc.Node{n}, w, nodeBashCapDefault, 0, renderSettings{verbose: true})); got != full {
		t.Errorf("show collapsed prose: %d rows, want the full %d", got, full)
	}
	// `show` with verbose off must behave the same: verbosity is about tool
	// args, not about hiding table rows.
	if got := len(renderNodeList([]livedoc.Node{n}, w, nodeBashCapDefault, 0, renderSettings{})); got != full {
		t.Errorf("show (non-verbose) collapsed prose: %d rows, want the full %d", got, full)
	}
	if got := len(view.RenderExpanded(n, w, 0, false)); got >= full {
		t.Errorf("transcript did NOT collapse prose: %d rows, full is %d", got, full)
	}
	if got := len(view.RenderExpanded(n, w, 0, true)); got != full {
		t.Errorf("transcript expansion did not restore prose: %d rows, want %d", got, full)
	}
}

// TestClampTables_NeverEmitsABlankRow exists for a bug hunt happening on
// another branch: a resize-duplication defect rooted in the painters' row diff,
// where a shortened frame leaves `old` defaulting to "" for rows past the base
// (Incipit.diffRange, and transcript.paint's `base` when it is short or is
// predBuf). Anything that can put a BLANK row where there used to be text can
// interact with that repro.
//
// The collapse cannot. Every row it emits is either a row it was handed,
// verbatim, or the single dim hint — and the hint is never blank. So the number
// of blank rows can only ever go DOWN across a clamp, never up.
//
// (The wrap fix moves the same way, and harder: before it, each wrapped cell
// emitted height-1 VISIBLY BLANK continuation rows — that was the bug's
// signature. After it, those rows carry text; 0 blank rows in any table render
// across widths 26..140.)
func TestClampTables_NeverEmitsABlankRow(t *testing.T) {
	blanks := func(rows []string) int {
		n := 0
		for _, r := range rows {
			if strings.TrimSpace(stripANSI(r)) == "" {
				n++
			}
		}
		return n
	}
	// A document with real blank separators AND a clampable table, so the count
	// is not trivially zero on both sides.
	md := "A paragraph before the table.\n\n" + tallTableMarkdown() +
		"\nA paragraph after it, long enough to wrap at least once at any width.\n"
	for w := 26; w <= 120; w += 2 {
		in := render.Prose(md, w)
		out := clampTables(in, proseTableCapDefault, 0)
		if len(out) >= len(in) {
			continue // nothing clamped at this width; nothing to assert
		}
		if got, was := blanks(out), blanks(in); got > was {
			t.Errorf("w=%d: clamp ADDED blank rows (%d -> %d)\n%s", w, was, got, dumpRows(out))
		}
		// And the row the clamp writes itself must never be blank.
		found := false
		for _, r := range out {
			if strings.Contains(stripANSI(r), "more table lines") {
				found = true
				if strings.TrimSpace(stripANSI(r)) == "" {
					t.Errorf("w=%d: the hint row is blank: %q", w, r)
				}
			}
		}
		if !found {
			t.Errorf("w=%d: rows were dropped (%d -> %d) with no hint row to say so", w, len(in), len(out))
		}
	}
}
