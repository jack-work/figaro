package cli

import (
	"strings"
	"testing"

	"github.com/jack-work/figaro/api/livedoc"
	"github.com/jack-work/figaro/internal/render"
)

// The gutter's cost is 2 where there is a margin for the rule to stand in and 4
// where there is not, a hard-wrap continuation chunk has none, and what must
// never happen is a row paying MORE than the gutter is wide, or landing past
// the viewport. This is the same property thinking_gutter_test.go asserts on
// five shapes, over the 24-shape corpus and every width 20..200: CJK, emoji,
// combining marks, zero-width, RTL, unbreakable tokens, URLs, fences, an
// unclosed fence, tables, nested lists, CRLF, tabs, a quote inside a quote.
//
// Canary (watched): reserve width-2 instead of width-4 ->
//
//	cjk w=20 row 0: 22 cells, past the edge (reserved 18)
func TestFarmerGutterCostsNoMoreThanItsWidth(t *testing.T) {
	for name, md := range farmerCorpus() {
		for w := 20; w <= 200; w++ {
			n := livedoc.Node{Type: livedoc.NodeThinking, Markdown: md}
			pw := proseWidth(n, w)
			raw := render.Prose(nodeMarkdown(n), pw)
			got := nodeProseRows(n, w)
			if len(got) != len(raw) {
				t.Fatalf("%s w=%d: %d rows vs %d rendered", name, w, len(got), len(raw))
			}
			for i := range raw {
				cost := displayWidth(got[i]) - displayWidth(raw[i])
				want := quoteGutterCells
				if strings.HasPrefix(stripANSI(raw[i]), proseIndent) {
					want -= len(proseIndent)
				}
				if cost != want {
					t.Fatalf("%s w=%d row %d: gutter cost %d cells, want %d: %q",
						name, w, i, cost, want, stripANSI(got[i]))
				}
				if d := displayWidth(got[i]); d > w && displayWidth(raw[i]) <= pw {
					t.Fatalf("%s w=%d row %d: %d cells past the edge, though glamour stayed inside its %d-column budget: %q",
						name, w, i, d, pw, stripANSI(got[i]))
				}
			}
		}
	}
}

// THE GUTTER MUST NOT EAT TEXT. The companion property to the one above: the
// rule costs columns, and the row it is prefixed to must still say the same
// words. Named for the repair era it was written in, a version that kept the
// row's own leading spaces AND added a two-cell rule, so the clip took two
// cells back off the RIGHT end and, on a row without padding, two characters
// of the agent's own words with them.
func TestFarmerRepairEatsText(t *testing.T) {
	seen := map[string]bool{}
	for name, md := range farmerCorpus() {
		for w := 20; w <= 200; w++ {
			n := livedoc.Node{Type: livedoc.NodeThinking, Markdown: md}
			pw := proseWidth(n, w)
			raw := render.Prose(nodeMarkdown(n), pw)
			got := nodeProseRows(n, w)
			if len(raw) != len(got) {
				t.Fatalf("%s w=%d: row count changed", name, w)
			}
			for i := range raw {
				was := strings.TrimRight(stripANSI(raw[i]), " ")
				now := strings.TrimRight(stripANSI(got[i]), " ")
				// strip the rule the repair adds, then compare the words
				wasT := strings.TrimSpace(strings.ReplaceAll(was, "│", ""))
				nowT := strings.TrimSpace(strings.ReplaceAll(now, "│", ""))
				if wasT != nowT && !seen[name] {
					seen[name] = true
					t.Errorf("%s w=%d row %d: TEXT LOST\n  was %q\n  now %q", name, w, i, was, now)
				}
			}
		}
	}
}
