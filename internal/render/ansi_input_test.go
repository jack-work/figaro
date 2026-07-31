package render

import (
	"strings"
	"testing"
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
	// input -> the text that must remain VISIBLE. An explicit expectation, not
	// a list of forbidden substrings: my first version grepped for "[?1049h"
	// while the actual leak was "1049h", so five shapes passed green while
	// alt-screen and cursor-hide — the two sequences every pasted tool
	// transcript carries — still printed.
	cases := []struct{ name, in, want string }{
		{"sgr", "before \x1b[31mred\x1b[0m after words to make this wrap somewhere", "before red after words to make this wrap somewhere"},
		{"private-mode", "alt \x1b[?1049h screen and text that continues for a while", "alt screen and text that continues for a while"},
		{"cursor-hide", "hide \x1b[?25l cursor and text that continues for a while", "hide cursor and text that continues for a while"},
		{"csi-intermediate", "q \x1b[?25$p uery and text that continues for a while", "q uery and text that continues for a while"},
		{"osc", "title \x1b]0;pwned\x07 and text that continues for a while", "title and text that continues for a while"},
		{"osc-st", "title \x1b]0;pwned\x1b\\ and text that continues for a while", "title and text that continues for a while"},
		{"dcs", "dcs \x1bPq#0;2;0;0;0\x1b\\ payload and text that continues", "dcs payload and text that continues"},
		{"apc", "apc \x1b_hidden\x1b\\ payload and text that continues", "apc payload and text that continues"},
		{"ss3", "ss3 \x1bOP and text that continues for a while longer", "ss3 and text that continues for a while longer"},
		{"charset", "cs \x1b(B designator and text that continues for a while", "cs designator and text that continues for a while"},
		{"bare-esc", "bare \x1b escape and text that continues for a while", "bare escape and text that continues for a while"},
		{"truncated", "trunc \x1b[38;5; and text that continues for a while", "trunc and text that continues for a while"},
	}
	for _, tc := range cases {
		for w := 20; w <= 120; w++ {
			var seen strings.Builder
			for i, row := range Prose(tc.in, w) {
				plain := stripSGR(row)
				if strings.ContainsRune(plain, 0x1b) {
					t.Fatalf("%s w=%d row %d: an escape reached the output: %q", tc.name, w, i, plain)
				}
				if got := cells(row); got > w {
					t.Fatalf("%s w=%d row %d: %d cells: %q", tc.name, w, i, got, plain)
				}
				seen.WriteString(strings.TrimSpace(plain))
				seen.WriteString(" ")
			}
			got := strings.Join(strings.Fields(seen.String()), " ")
			want := strings.Join(strings.Fields(tc.want), " ")
			if got != want {
				t.Fatalf("%s w=%d: visible text\n got: %q\nwant: %q", tc.name, w, got, want)
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
