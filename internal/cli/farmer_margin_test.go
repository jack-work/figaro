package cli

import (
	"strings"
	"testing"

	"github.com/jack-work/figaro/api/livedoc"
	"github.com/jack-work/figaro/internal/render"
)

// The gutter costs 2 where a margin exists for the rule and 4 where it does
// not; a row never pays more than the gutter is wide.
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

// The rule costs columns; the row it prefixes must still say the same words.
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
