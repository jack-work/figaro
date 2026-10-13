package cli

import (
	"fmt"
	"strings"
	"testing"
)

// displayWidthBytes exists because the painter measures a byte buffer without
// allocating; the string version is the oracle and the two must agree over the corpus.
func displayWidthCorpus() []string {
	return []string{
		"",
		" ",
		"plain ascii text",
		"trailing spaces    ",
		"\x1b[1mbold\x1b[0m",
		"\x1b[38;5;252m" + strings.Repeat(" ", 40) + "\x1b[0m",
		"\u65e5\u672c\u8a9e",        // wide
		"a\u65e5b\u672cc",           // wide interleaved with narrow
		"e\u0301 c",                 // combining mark: zero columns
		"\u00e9 c",                  // the precomposed form: one
		"a\u0301\u0302\u0303b",      // a stack of combining marks
		"\u2500\u2502\u250c\u2518",  // box drawing
		"\x1b[7mreverse",            // no reset
		"\x1b]0;a title\x07after",   // OSC with a BEL terminator
		"\x1b]0;a title\x1b\\after", // OSC with an ST terminator
		"\x1bPsixel\x1b\\after",     // DCS
		"trunc \x1b[38;5; and text", // malformed CSI
		"\x1b\u0631",                // bare ESC before a multi-byte rune
		"\x1b",                      // a lone ESC at the end
		"\x1b[",                     // a truncated CSI at the end
		"\xff\xfe invalid utf-8",    // not UTF-8 at all
		"mixed \x1b[31mred\x1b[0m \u65e5 \x1b[1mb", // everything at once
		strings.Repeat("\x1b[38;5;252mx\x1b[0m", 50),
		"\x00\x01control bytes\x7f",
		"tab\there",
	}
}

func TestDisplayWidthBytesAgreesWithDisplayWidth(t *testing.T) {
	for i, s := range displayWidthCorpus() {
		want := displayWidth(s)
		if got := displayWidthBytes([]byte(s)); got != want {
			t.Errorf("case %d %q: displayWidthBytes=%d, displayWidth=%d", i, s, got, want)
		}
		// Every prefix too: a truncated escape or a split rune is what a
		// prefix produces.
		for cut := 0; cut < len(s); cut++ {
			p := s[:cut]
			if got, want := displayWidthBytes([]byte(p)), displayWidth(p); got != want {
				t.Fatalf("case %d %q cut at %d (%q): displayWidthBytes=%d, displayWidth=%d",
					i, s, cut, p, got, want)
			}
		}
	}
}

// Proves the corpus can fail.
func TestDisplayWidthCorpusCatchesADifference(t *testing.T) {
	// A plausible wrong implementation: one column per rune.
	naive := func(b []byte) int {
		col := 0
		for i := 0; i < len(b); {
			if b[i] == 0x1b {
				col++
				i++
				continue
			}
			col++
			i++
		}
		return col
	}
	caught := 0
	for _, s := range displayWidthCorpus() {
		if naive([]byte(s)) != displayWidth(s) {
			caught++
		}
	}
	if caught < 10 {
		t.Fatalf("the corpus caught a deliberately wrong implementation on only"+
			" %d of %d cases; it is not discriminating enough to be evidence",
			caught, len(displayWidthCorpus()))
	}
}

// Past the stop it must report the stop, never more.
func TestDisplayWidthBytesStopsAtTheMargin(t *testing.T) {
	for _, s := range displayWidthCorpus() {
		full := displayWidth(s)
		for _, stop := range []int{0, 1, 2, 5, 20, 200} {
			got := displayWidthBytesUpTo([]byte(s), stop)
			want := full
			if full > stop {
				want = stop
			}
			if got != want {
				t.Fatalf("%q up to %d: got %d, want %d (full %d)", s, stop, got, want, full)
			}
		}
	}
}

func TestDisplayWidthBytesUpToNegativeCountsEverything(t *testing.T) {
	for _, s := range displayWidthCorpus() {
		if got, want := displayWidthBytesUpTo([]byte(s), -1), displayWidth(s); got != want {
			t.Fatalf("%q: got %d, want %d", s, got, want)
		}
	}
	_ = fmt.Sprint()
}
