package render

import (
	"strings"
	"testing"
)

// glamour prints the parameter bytes of an escape it dropped the ESC from,
// having wrapped as though the sequence were zero-width.
func TestMarkdownWithANSIDoesNotLeakOrOverrun(t *testing.T) {
	// Expected visible text, not forbidden substrings: the leak is "1049h", not "[?1049h".
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
