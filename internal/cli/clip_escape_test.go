package cli

import (
	"testing"

	"github.com/jack-work/figaro/internal/render"
	"github.com/mattn/go-runewidth"
)

// A clipped row must never occupy more columns than it was clipped to.
//
// THIS IS THE BUG THE OWNER REPORTED, three times: "we continue to render text
// beyond the limit the right side provides", "one or two characters beyond the
// edge", worse in tool output. It was never the gutter. There were THREE
// hand-rolled escape scanners in this tree, and two of them — displayWidth's
// escapeEnd and clipToWidthRewrite's inline loop — advanced to the first ASCII
// LETTER, which is not what an escape sequence is:
//
//	"\x1b abc"       a bare ESC swallowed the 'a', so the width was one short
//	                 and the clip wrote one cell PAST THE EDGE
//	"\x1b]0;title\x07"  the OSC ended at the 't' of "title", so the row was
//	                 clipped SHORT and text was lost instead
//
// Tool output reaches these clips with escapes intact — SanitizeForTerminal
// deliberately keeps SGR — which is why the owner saw it worst there.
//
// MEASURED IN A REAL PANE, before and after: clipToWidth(s, 10) on a bare-ESC
// row rendered 11 columns before the fix and 10 or fewer after, for every shape
// below.
//
// CANARY (watched): restore the first-ASCII-letter scan in clipToWidthRewrite
// and this fails with `bare-esc w=10: clipped row occupies 11 columns`.
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
			// Independent of the clip's own arithmetic: strip the escapes with
			// the input-side stripper, then measure what is left.
			if cols := runewidth.StringWidth(render.StripEscapes(got)); cols > w {
				t.Fatalf("%s w=%d: clipped row occupies %d columns: %q", name, w, cols, render.StripEscapes(got))
			}
		}
	}
}

// displayWidth must not UNDERCOUNT — an undercount is the only arithmetic that
// can put text past the edge, because every clip trusts it.
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
			t.Fatalf("%s: displayWidth=%d,true width=%d — undercount lets a clip overflow", name, got, want)
		}
	}
}
