package cli

import (
	"fmt"
	"strings"
	"testing"
)

// displayWidth and displayWidthBytes are one rule with two implementations,
// which exists only because the painter measures a row it has just built in a
// byte buffer and may not allocate a string to do it. Two implementations of
// one rule drift, and the drift is silent: a column count that is one too small
// erases a character the reader was reading.
//
// So the string version is kept as the ORACLE, permanently, and the two must
// agree byte for byte over a corpus. The corpus is not scaffolding to be
// deleted once this passes: an equivalence claim is a fact about every day
// after the change, and it stays checkable only while both sides are present.
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
		// Every prefix too: the interesting failures are at a boundary, and a
		// truncated escape or a split rune is exactly what a prefix produces.
		for cut := 0; cut < len(s); cut++ {
			p := s[:cut]
			if got, want := displayWidthBytes([]byte(p)), displayWidth(p); got != want {
				t.Fatalf("case %d %q cut at %d (%q): displayWidthBytes=%d, displayWidth=%d",
					i, s, cut, p, got, want)
			}
		}
	}
}

// TestDisplayWidthCorpusCatchesADifference proves the corpus can fail. Without
// this, "the two agree" is indistinguishable from "the corpus cannot tell them
// apart", and a corpus of nothing but ASCII would pass forever while the wide
// and combining cases rotted.
func TestDisplayWidthCorpusCatchesADifference(t *testing.T) {
	// A plausible wrong implementation: one column per rune, which is what the
	// VT model did before tmux settled the question, and what anyone writing
	// this in a hurry produces.
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

// TestDisplayWidthBytesStopsAtTheMargin pins the early exit that the painter
// relies on: past the stop it must report the stop, and never more.
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
