package cli

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/jack-work/figaro/api/livedoc"
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
		rows := nodeProseRows(livedoc.Node{Type: livedoc.NodeThinking, Markdown: md}, w)
		col := -1
		for i, r := range rows {
			// Overflow must be INHERITED, never added: glamour overruns the
			// width it is given (an unbreakable token, an unclosed fence), and
			// re-reporting that every run is noise. The contract is the delta.
			if d := displayWidth(r); d > w && !glamourOverranAt(md, w) {
				t.Fatalf("w=%d row %d is %d cells, and glamour stayed inside its own budget: %q", w, i, d, stripANSI(r))
			}
			plain := strings.TrimRight(stripANSI(r), " ")
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
