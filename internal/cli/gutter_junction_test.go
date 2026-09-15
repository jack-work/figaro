package cli

import (
	"strings"
	"testing"
	"time"

	"github.com/jack-work/figaro/api/livedoc"
	"github.com/jack-work/figaro/internal/livelog/aria"
	ldrender "github.com/jack-work/figaro/internal/livelog/render"
	"github.com/jack-work/figaro/internal/term"
)

func gutterThinking() livedoc.Node {
	return livedoc.Node{
		Type:       livedoc.NodeThinking,
		Markdown:   "I should read the handoff first.\n\nThen check the tests before changing anything.",
		FormDeltas: adornDeltas("mantra", "reading the handoff", "checking the tests"),
	}
}

func gutterPager(t testing.TB, n livedoc.Node) *transcript {
	t.Helper()
	tr, _ := gutterPagerOn(t, n)
	return tr
}

// gutterPagerOn is gutterPager with the terminal it paints to, for the tests
// that read the FRAME (chrome included) rather than the body rows.
func gutterPagerOn(t testing.TB, n livedoc.Node) (*transcript, *ldrender.FakeTerminal) {
	t.Helper()
	client := aria.NewClient()
	client.Apply(aria.Page{Parts: []aria.TurnPart{{Turn: aria.Turn{
		ID: 1, Sealed: true, Inquiry: "what should happen here?",
		InquirySegments: []aria.InquirySegment{{Sender: "Gluck", Text: "what should happen here?"}},
		Nodes:           []livedoc.Node{n},
	}}}}, aria.Notify)
	view := &ariaView{settings: &renderSettings{sticky: true}}
	ft := ldrender.NewFakeTerminal(80, 18)
	tr := newTranscript(ft, 80, 18, view, client, "gutter01", time.Time{})
	tr.enter()
	tr.follow = false
	tr.buildIndex()
	return tr, ft
}

func TestThinkingAdornmentJoinsBelowTheText(t *testing.T) {
	defer term.SetColorMode(term.ColorAlways)()
	tr := gutterPager(t, gutterThinking())
	ref := nodeRef{turn: 1, index: 0}
	tr.adorned[ref] = true
	tr.dropTurnsRows(map[int]struct{}{1: {}})

	for _, selected := range []bool{false, true, false} {
		if selected {
			tr.selectRef(deltaRefOf(ref, 1), false)
		} else {
			tr.clearSelection()
		}
		rows := adornScreen(tr)
		start, end, connector, delta := -1, -1, -1, -1
		for i, row := range rows {
			switch {
			case strings.Contains(row, "I should read"):
				start = i
			case strings.Contains(row, "Then check"):
				end = i
			case strings.HasPrefix(row, "Δ─╯"), strings.HasPrefix(row, "╭─╯"):
				connector = i
			case strings.Contains(row, "mantra"):
				delta = i
			}
		}
		if start < 0 || end < 0 || connector != end+1 || delta != connector+1 {
			t.Fatalf("selected=%v: connector must follow the thinking text, then the delta:\n%s", selected, strings.Join(rows, "\n"))
		}
		for _, row := range rows[start : end+1] {
			if !strings.HasPrefix(row, "  │") {
				t.Fatalf("thinking margin changed: %q", row)
			}
		}
		if selected {
			wantRows(t, rows[connector:delta+1], "╭─╯", "Δ mantra  reading the handoff -> checking the tests")
		} else {
			wantRows(t, rows[connector:delta+1], "Δ─╯", "  mantra  reading the handoff -> checking the tests")
		}
	}
}

func TestThinkingAdornmentCollapsedKeepsItsGutterMarker(t *testing.T) {
	tr := gutterPager(t, gutterThinking())
	rows := adornScreen(tr)
	markers := 0
	for _, row := range rows {
		if strings.HasSuffix(row, "Δ") {
			markers++
		}
		if strings.Contains(row, "╯") || strings.Contains(row, "mantra") {
			t.Fatalf("collapsed list drew a connector or delta: %q", row)
		}
	}
	if markers != 1 {
		t.Fatalf("collapsed thinking block has %d markers, want one", markers)
	}
}

func TestStickyRuleJoinsOnlyTheNextRowsGutter(t *testing.T) {
	tr := gutterPager(t, livedoc.Node{Type: livedoc.NodeThinking,
		Markdown: strings.Repeat("The answer needs a careful check before we change it.\n\n", 16)})
	span, ok := tr.nodeSpanOf(nodeRef{turn: 1, index: 0})
	if !ok {
		t.Fatal("thinking node is missing")
	}
	for _, width := range []int{80, 40, 100} {
		tr.resize(width, 18)
		span, _ = tr.nodeSpanOf(nodeRef{turn: 1, index: 0})
		tr.offset = span.first
		tr.render()
		head := headRowsOf(tr)
		if len(head) != stickyText+1 {
			t.Fatalf("expected a sticky question and rule, got %q", head)
		}
		// The header covers body rows: the junction must follow the first
		// uncovered row, not the first row hidden behind the question.
		below := stripANSI(tr.lineAt(tr.offset + len(head)))
		if !strings.HasPrefix(below, "  │") {
			t.Fatalf("fixture has no gutter below the rule: %q", below)
		}
		rule := stripANSI(head[len(head)-1])
		if !strings.HasPrefix(rule, "──┬─") || displayWidth(rule) != width {
			t.Fatalf("width %d: rule above %q is %q", width, below, rule)
		}
		// AND ONLY WHEN THE RULE CUTS IT. One row higher and the block's first
		// row sits directly under the rule: the gutter starts there, so there
		// is nothing above for it to join.
		tr.offset = span.first - len(head)
		tr.render()
		head = headRowsOf(tr)
		rule = stripANSI(head[len(head)-1])
		if strings.Contains(rule, "┬") {
			t.Fatalf("width %d: a block's first row joins nothing, got %q", width, rule)
		}
	}
}

func TestJoinRule(t *testing.T) {
	// far is the row on the other side of the rule: the one the chrome covers.
	// A gutter on both sides is CUT by the rule and gets a junction; a gutter
	// that begins or ends against it is a whole line and gets none.
	for _, tc := range []struct {
		name, near, far, want string
	}{
		{"tool gutter", "  │ result", "  │ result", "──┬─────"},
		{"left spine", "│ mantra", "│ mantra", "┬───────"},
		{"styled gutter", "\x1b[2m  │\x1b[0m result", "  │ x", "──┬─────"},
		{"split style", " \x1b[2m │\x1b[m", "  │", "──┬─────"},
		{"prose", "  ordinary text", "  │ x", "────────"},
		{"embedded vertical", "  text │ table", "  │ x", "────────"},
		{"ascii pipe", "  | plain", "  │ x", "────────"},
		{"corner", "  ╯", "  │ x", "────────"},
		{"blank", "\x1b[2m   \x1b[m", "  │ x", "────────"},
		{"past width", "        │", "        │", "────────"},
		{"last column", "       │", "       │", "───────┬"},
		{"block boundary", "  │ first row", "∨ The Boss", "────────"},
		{"nothing beyond", "  │ last row", "", "────────"},
		{"gutter moved", "  │ here", "    │ there", "────────"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rule := "\x1b[2m────────\x1b[m"
			got := joinRule(rule, tc.near, tc.far, "┬")
			if want := "\x1b[2m" + tc.want + "\x1b[m"; got != want {
				t.Fatalf("got %q, want %q", got, want)
			}
			if rule != "\x1b[2m────────\x1b[m" {
				t.Fatal("joining mutated the cached rule")
			}
		})
	}
}

func TestThinkingConnectorKeepsCoordinatesAndChromeSeparate(t *testing.T) {
	tr := gutterPager(t, gutterThinking())
	ref := nodeRef{turn: 1, index: 0}
	tr.adorned[ref] = true
	tr.dropTurnsRows(map[int]struct{}{1: {}})
	tr.buildIndex()
	connectors := 0
	for _, e := range tr.index.entries {
		for _, row := range e.rows {
			if row.spine.Kind == ldrender.SpineAnchor && blockOf(row.ref) == ref {
				connectors++
				if !row.chrome || row.ref.delta != 0 {
					t.Fatalf("connector should not be a selectable delta: %+v", row)
				}
			}
			if strings.Contains(row.text, "mantra") && row.ref != deltaRefOf(ref, 1) {
				t.Fatalf("delta row lost its address: %+v", row.ref)
			}
		}
	}
	if connectors != 1 {
		t.Fatalf("got %d connectors, want one", connectors)
	}
}

func TestShowThinkingUsesTheSameConnector(t *testing.T) {
	m := aria.Message{Turn: 1, Role: livedoc.RoleOutput, Nodes: []livedoc.Node{gutterThinking()}}
	rows := plain(renderTurnRows(m, 100, 0, renderSettings{verbose: true}))
	for i, row := range rows {
		if strings.Contains(row, "Then check") {
			wantRows(t, rows[i:i+3],
				"  │ Then check the tests before changing anything.",
				"Δ─╯", "  mantra  reading the handoff -> checking the tests")
			return
		}
	}
	t.Fatalf("show lost the thinking text: %q", rows)
}

// THE FLOOR JOINS TOO. The rule above the status bar had no junctions at all,
// while the sticky header's rule drew one unconditionally; both now say the
// same thing, and say it only when the gutter is CUT by the rule rather than
// ending against it.
func TestFooterRuleJoinsOnlyAGutterItCuts(t *testing.T) {
	tr, ft := gutterPagerOn(t, livedoc.Node{Type: livedoc.NodeThinking,
		Markdown: strings.Repeat("The answer needs a careful check before we change it.\n\n", 16)})
	body, maxOff := tr.layoutNow()
	span, ok := tr.nodeSpanOf(nodeRef{turn: 1, index: 0})
	if !ok || span.last-span.first < body {
		t.Fatalf("fixture must outrun the viewport: span %+v, body %d", span, body)
	}
	floorRule := func() string {
		tr.renderFrame()
		rows := stripANSIAll(ft.Screen())
		for i := len(rows) - 1; i >= 0; i-- {
			if strings.HasPrefix(rows[i], "──") || strings.HasPrefix(rows[i], "─┴") {
				return rows[i]
			}
		}
		t.Fatalf("no closing rule on screen:\n%s", strings.Join(rows, "\n"))
		return ""
	}

	tr.offset = span.first
	if rule := floorRule(); !strings.HasPrefix(rule, "──┴─") {
		t.Fatalf("a block that runs past the rule must be cut by it, got %q", rule)
	}
	// The tail: the block's last row sits against the rule, and a junction
	// there would branch to a conversation that is not there.
	tr.offset = maxOff
	if rule := floorRule(); strings.Contains(rule, "┴") {
		t.Fatalf("the last row of a block joins nothing, got %q", rule)
	}
}
