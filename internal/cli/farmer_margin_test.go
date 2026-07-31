package cli

import (
	"testing"

	"github.com/jack-work/figaro/internal/livedoc"
	"github.com/jack-work/figaro/internal/render"
)

// Which rows have NO two-column margin for the rule to stand in? On those the
// gutter costs four columns, not two, and the stated contract is false.
func TestFarmerRowsWithoutMargin(t *testing.T) {
	corp := farmerCorpus()
	corp["fallback"] = "|a|\n|-|\n" // degenerate table
	corp["blankpara"] = "one\n\n\n\ntwo"
	corp["hardbreak"] = "line one  \nline two"
	for name, md := range corp {
		for w := 20; w <= 200; w++ {
			n := livedoc.Node{Type: livedoc.NodeThinking, Markdown: md}
			pw := proseWidth(n, w)
			for i, r := range clampTables(render.Prose(nodeMarkdown(n), pw), proseTableCapDefault, pw) {
				if dedentProse(r) == r {
					t.Errorf("%s w=%d row %d has no margin to dedent (gutter costs 4, not 2): %q", name, w, i, r)
					return
				}
			}
		}
	}
}
