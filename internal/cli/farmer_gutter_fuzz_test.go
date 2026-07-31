package cli

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/jack-work/figaro/internal/livedoc"
)

// FuzzThinkingGutter drives arbitrary markdown at arbitrary widths through the
// quoted renderer and asserts the three invariants the branch claims: a rule on
// every non-blank row of a block, every rule in the same column, every row
// inside the viewport. Seeded with the shapes the corpus already knows break
// wrapping, so the fuzzer starts from the interesting part of the space.
func FuzzThinkingGutter(f *testing.F) {
	for _, md := range farmerCorpus() {
		f.Add(md, 80)
	}
	f.Add("> quoted\n\n- list\n\n```\ncode\n```", 61)
	f.Add("日本語\u200b\u0301x", 23)
	f.Fuzz(func(t *testing.T, md string, w int) {
		if !utf8.ValidString(md) || len(md) > 4000 {
			t.Skip()
		}
		w = 20 + (w%181+181)%181 // 20..200
		rows := nodeProseRows(livedoc.Node{Type: livedoc.NodeThinking, Markdown: md}, w, false)
		col := -1
		for i, r := range rows {
			if d := displayWidth(r); d > w {
				t.Fatalf("w=%d row %d is %d cells: %q", w, i, d, farmerStrip(r))
			}
			plain := strings.TrimRight(farmerStrip(r), " ")
			if strings.TrimSpace(plain) == "" {
				continue
			}
			at := strings.Index(plain, "│")
			if at < 0 {
				t.Fatalf("w=%d row %d has no rule: %q", w, i, plain)
			}
			c := displayWidth(plain[:at])
			if col < 0 {
				col = c
			} else if c != col {
				t.Fatalf("w=%d row %d: rule at column %d, block uses %d: %q", w, i, c, col, plain)
			}
		}
	})
}
