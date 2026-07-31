package render

import (
	"strings"
	"testing"

	"github.com/mattn/go-runewidth"
)

// Markdown carrying ANSI must not render it as content, and must not overrun.
//
// glamour drops the ESC byte of a sequence it does not understand, keeps the
// parameter bytes as VISIBLE TEXT, and has already wrapped as though the whole
// sequence were zero-width. So `\x1b[31mred` rendered as a row four cells wider
// than the width it was given — at EVERY width — printing "[31m" on screen.
// Models paste ANSI out of tool output constantly, so this is a live path.
//
// THIS TEST EXISTS BECAUSE ITS ABSENCE LET ME SHIP A LIE: a previous commit
// claimed to fix this by calling SanitizeForTerminal on the markdown. That
// function is an OUTPUT sanitizer whose documented job is to KEEP SGR verbatim,
// so it changed nothing here, and nothing in the suite could say so. The fix is
// StripEscapes — for markdown an escape is neither content nor styling.
//
// CANARY (watched): swap StripEscapes for SanitizeForTerminal in Prose ->
//
//	w=40: row 0 is 44 cells and contains "[31m"
func TestMarkdownWithANSIDoesNotLeakOrOverrun(t *testing.T) {
	cases := map[string]string{
		"sgr":         "before \x1b[31mred\x1b[0m after and some more words to make it wrap somewhere",
		"osc":         "title \x1b]0;pwned\x07 and text that continues on for a good while here",
		"privatemode": "alt \x1b[?1049h screen and text that continues on for a good while here",
		"bare-esc":    "bare \x1b escape and text that continues on for a good while here too",
		"truncated":   "trunc \x1b[38;5; and text that continues on for a good while here too",
	}
	for name, md := range cases {
		for w := 20; w <= 120; w++ {
			for i, row := range Prose(md, w) {
				plain := stripSGR(row)
				if strings.ContainsRune(plain, 0x1b) {
					t.Fatalf("%s w=%d row %d: an escape reached the output: %q", name, w, i, plain)
				}
				for _, leak := range []string{"[31m", "[0m", "[?1049h", "0;pwned", "[38;5;"} {
					if strings.Contains(plain, leak) {
						t.Fatalf("%s w=%d row %d: escape printed as text (%q): %q", name, w, i, leak, plain)
					}
				}
				if got := runewidth.StringWidth(plain); got > w {
					t.Fatalf("%s w=%d row %d: %d cells: %q", name, w, i, got, plain)
				}
			}
		}
	}
}

func stripSGR(s string) string {
	out, r, i := make([]rune, 0, len(s)), []rune(s), 0
	for i < len(r) {
		if r[i] == 0x1b && i+1 < len(r) && r[i+1] == '[' {
			i += 2
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
