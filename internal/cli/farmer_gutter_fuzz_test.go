package cli

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/jack-work/figaro/api/livedoc"
)

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
		w = 20 + (w%181+181)%181
		rows := nodeProseRows(livedoc.Node{Type: livedoc.NodeThinking, Markdown: md}, w)
		col := -1
		for i, r := range rows {
			// A row may exceed the viewport only where glamour's own
			// row already did.
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
