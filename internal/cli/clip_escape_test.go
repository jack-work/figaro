package cli

import (
	"testing"

	"github.com/jack-work/figaro/internal/render"
	"github.com/mattn/go-runewidth"
)

// A clipped row never occupies more columns than it was clipped to; a bare
// ESC and an OSC are not terminated by the first ASCII letter.
func TestClippedRowNeverExceedsItsWidth(t *testing.T) {
	shapes := map[string]string{
		"plain":        "abcdefghijklmnopqrstuvwxyz0123456789",
		"sgr":          "\x1b[31mred\x1b[0m abcdefghijklmnopqrstuvwxyz",
		"bare-esc":     "\x1b abcdefghijklmnopqrstuvwxyz",
		"charset":      "\x1b(B abcdefghijklmnopqrstuvwxyz",
		"ss3":          "\x1bOP abcdefghijklmnopqrstuvwxyz",
		"osc-bel":      "\x1b]0;title\x07 abcdefghijklmnopqrstuvwxyz",
		"osc-st":       "\x1b]0;title\x1b\\ abcdefghijklmnopqrstuvwxyz",
		"dcs":          "\x1bPq payload \x1b\\ abcdefghijklmnopqrstuvwxyz",
		"private":      "\x1b[?25l abcdefghijklmnopqrstuvwxyz",
		"intermediate": "\x1b[?25$p abcdefghijklmnopqrstuvwxyz",
		"truncated":    "\x1b[38;5; abcdefghijklmnopqrstuvwxyz",
		"wide":         "日本語のテキスト abcdefghijklmnopqrstuvwxyz",
		"emoji":        "🎉🚀🧪 abcdefghijklmnopqrstuvwxyz",
	}
	for name, s := range shapes {
		for w := 1; w <= 40; w++ {
			got := clipToWidth(s, w)
			// Measured independently of the clip's own arithmetic.
			if cols := runewidth.StringWidth(render.StripEscapes(got)); cols > w {
				t.Fatalf("%s w=%d: clipped row occupies %d columns: %q", name, w, cols, render.StripEscapes(got))
			}
		}
	}
}

// An undercount is the only arithmetic that can put text past the edge.
func TestDisplayWidthDoesNotUndercount(t *testing.T) {
	for name, s := range map[string]string{
		"bare-esc": "\x1b abcdefghij",
		"charset":  "\x1b(B abcdefghij",
		"ss3":      "\x1bOP abcdefghij",
		"osc-bel":  "\x1b]0;title\x07 abcdefghij",
		"dcs":      "\x1bPq x \x1b\\ abcdefghij",
	} {
		want := runewidth.StringWidth(render.StripEscapes(s))
		if got := displayWidth(s); got < want {
			t.Fatalf("%s: displayWidth=%d,true width=%d: undercount lets a clip overflow", name, got, want)
		}
	}
}
