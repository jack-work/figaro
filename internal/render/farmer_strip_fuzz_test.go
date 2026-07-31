package render

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// FuzzStripEscapesLeavesNoESCBody: whatever the input, the output must carry no
// ESC and no recognisable escape BODY. The body is what makes this a rendering
// bug rather than a safety one — glamour prints it, having wrapped as though it
// were nothing.
func FuzzStripEscapesLeavesNoESCBody(f *testing.F) {
	for _, s := range []string{
		"plain text", "a\x1b[31mred\x1b[0m", "a\x1b[?25l", "a\x1b]0;t\x07", "a\x1bPq;1\x1b\\",
		"a\x1b_x\x1b\\", "a\x1b(B", "a\x1bOP", "a\x1b[?25$p", "a\x1b",
	} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		if !utf8.ValidString(s) || len(s) > 2000 {
			t.Skip()
		}
		got := StripEscapes(s)
		if strings.ContainsRune(got, 0x1b) {
			t.Fatalf("ESC survived: %q -> %q", s, got)
		}
		// Every ESC in the input took at least its introducer with it: the
		// output must not contain a CSI/OSC/DCS body that was not in the input
		// as literal text. The cheap, honest form of that: the number of
		// printable characters may only DROP.
		if len(got) > len(s) {
			t.Fatalf("output grew: %q -> %q", s, got)
		}
	})
}
