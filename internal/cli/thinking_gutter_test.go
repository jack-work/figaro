package cli

import (
	"strings"
	"testing"

	"github.com/jack-work/figaro/internal/livedoc"
)

// A thinking block draws a rule down its left edge. EVERY row must carry it,
// at EVERY width.
//
// WHAT WAS WRONG. The rule came from glamour: thinking was wrapped in markdown
// blockquote syntax and rendered. glamour applies a blockquote prefix per
// MARKDOWN LINE, not per rendered ROW, so a paragraph long enough to word-wrap
// produced continuation rows carrying the paragraph inset and no rule:
//
//	│ I'll need to read through the plan document and review the diffs to give a comprehensive
//	response                                    <- no rule, and it looks like a wrap artefact
//	│ about the repository, the worktree setup, ...
//
// which is exactly what the owner reported, in his own words: wrapping "in
// thinking blocks particularly", appearing at some terminal sizes and not
// others. Measured across widths 50..130 on one real paragraph, 21 of those 81
// widths emitted at least one gutterless row — including 100, 101 and 73. The
// "sometimes" is where the wrap happens to land.
//
// Reducing the width handed to glamour does NOT fix it (measured: 23 of 81
// broken instead of 21 — it only moves which widths break). The defect is not a
// budget: the decoration is applied to the wrong unit. So figaro draws the
// gutter itself, on every row, the way tool output always has.
//
// A SECOND defect lives in the same rows and this test pins it too: glamour's
// blockquote emits a row ONE CELL WIDER than the width it was given at some
// widths — measured at 5 of the 81 widths 50..130 (61, 63, 74, 112, 122),
// while plain prose never does it. That cell is the other half of the report:
// hidden under nvim's nowrap, wrapped under tmux.
//
// CANARIES (both watched):
//   - make repairOneQuoteRow return row unchanged ->
//     `thinking w=50: row 1 has no gutter: "  response"`
//   - drop the clipToWidth in repairQuoteRule ->
//     `thinking w=61: row 3 is 62 cells, past the edge`
func TestThinkingKeepsItsGutterAtEveryWidth(t *testing.T) {
	const sample = "I'll need to read through the plan document and review the diffs to give a " +
		"comprehensive response about the repository, the worktree setup, console issues on " +
		"Windows, and my broader context around skills and approach."

	for _, typ := range []livedoc.NodeType{livedoc.NodeThinking, livedoc.NodeSteering} {
		for w := 50; w <= 130; w++ {
			rows := nodeProseRows(livedoc.Node{Type: typ, Markdown: sample}, w, false)
			if len(rows) < 2 {
				t.Fatalf("%v w=%d: fixture must WRAP to be able to fail, got %d row(s)", typ, w, len(rows))
			}
			for i, r := range rows {
				plain := strings.TrimRight(stripSGRForTest(r), " ")
				if strings.TrimSpace(plain) == "" {
					continue
				}
				if !strings.HasPrefix(strings.TrimLeft(plain, " "), "│") {
					t.Fatalf("%v w=%d: row %d has no gutter: %q", typ, w, i, plain)
				}
				if got := displayWidth(r); got > w {
					t.Fatalf("%v w=%d: row %d is %d cells, past the edge: %q", typ, w, i, got, plain)
				}
			}
		}
	}
}

// TestProseIsNotQuoted guards the other side: ordinary assistant prose must NOT
// grow a gutter. Without this, "give every row a gutter" could be satisfied by
// giving every node one.
func TestProseIsNotQuoted(t *testing.T) {
	rows := nodeProseRows(livedoc.Node{Type: livedoc.NodeProse, Markdown: "plain prose, no rule"}, 60, false)
	for i, r := range rows {
		if strings.Contains(stripSGRForTest(r), "│") {
			t.Fatalf("prose row %d drew a gutter: %q", i, stripSGRForTest(r))
		}
	}
}

func stripSGRForTest(s string) string {
	out, r, i := make([]rune, 0, len(s)), []rune(s), 0
	for i < len(r) {
		if r[i] == 0x1b {
			for i < len(r) && !((r[i] >= 'A' && r[i] <= 'Z') || (r[i] >= 'a' && r[i] <= 'z')) {
				i++
			}
			i++
			continue
		}
		out = append(out, r[i])
		i++
	}
	return string(out)
}
