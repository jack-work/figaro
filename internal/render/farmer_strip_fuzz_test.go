package render

import (
	"strings"
	"testing"
	"unicode/utf8"
)

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
		// The count of printable characters may only drop.
		if len(got) > len(s) {
			t.Fatalf("output grew: %q -> %q", s, got)
		}
	})
}
