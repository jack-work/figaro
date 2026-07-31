package cli

import (
	"strings"
	"testing"

	"github.com/jack-work/figaro/internal/livedoc"
	"github.com/jack-work/figaro/internal/render"
)

// The gutter's cost is 2 where there is a margin for the rule to stand in and 4
// where there is not — a hard-wrap continuation chunk has none — and what must
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
			raw := clampTables(render.Prose(nodeMarkdown(n), pw), proseTableCapDefault, pw)
			got := nodeProseRows(n, w, false)
			if len(got) != len(raw) {
				t.Fatalf("%s w=%d: %d rows vs %d rendered", name, w, len(got), len(raw))
			}
			for i := range raw {
				cost := displayWidth(got[i]) - displayWidth(raw[i])
				want := quoteGutterCells
				if strings.HasPrefix(farmerStrip(raw[i]), proseIndent) {
					want -= len(proseIndent)
				}
				if cost != want {
					t.Fatalf("%s w=%d row %d: gutter cost %d cells, want %d: %q",
						name, w, i, cost, want, farmerStrip(got[i]))
				}
				if d := displayWidth(got[i]); d > w && displayWidth(raw[i]) <= pw {
					t.Fatalf("%s w=%d row %d: %d cells past the edge, though glamour stayed inside its %d-column budget: %q",
						name, w, i, d, pw, farmerStrip(got[i]))
				}
			}
		}
	}
}
