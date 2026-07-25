package cli

import (
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/mattn/go-runewidth"
)

// clipToWidthReference is the pre-optimization implementation of clipToWidth,
// kept verbatim as the differential oracle: the fast path is only allowed to
// exist if it is byte-identical to this for every input.
func clipToWidthReference(s string, width int) string {
	col := 0
	var b strings.Builder
	rs := []rune(s)
	clipped := false
	for i := 0; i < len(rs); {
		if rs[i] == '\x1b' { // copy the whole escape sequence, uncounted
			j := i + 1
			for j < len(rs) && !((rs[j] >= 'A' && rs[j] <= 'Z') || (rs[j] >= 'a' && rs[j] <= 'z')) {
				j++
			}
			if j < len(rs) {
				j++
			}
			b.WriteString(string(rs[i:j]))
			i = j
			continue
		}
		r := rs[i]
		if r < 0x20 || r == 0x7f { // control char → space
			r = ' '
		}
		w := runewidth.RuneWidth(r)
		if col+w > width {
			clipped = true
			break
		}
		b.WriteRune(r)
		col += w
		i++
	}
	if clipped {
		b.WriteString("\x1b[0m")
	}
	return b.String()
}

// clipCorpus is the shared input corpus for the differential tests: empty,
// ANSI-only, wide runes, combining marks, control characters, exactly-at-width,
// over-width, invalid UTF-8, long runs.
var clipCorpus = []string{
	"",
	" ",
	"hello world",
	strings.Repeat("x", 200),
	"\x1b[2m",
	"\x1b[2mdim\x1b[0m",
	"\x1b[38;5;238mcolored text with a reset\x1b[0m and a tail",
	"\x1b[", // truncated escape, no terminator
	"\x1b",
	"\x1b[0",
	"a\x1bZb",
	"日本語のテキストです",
	"\x1b[36m日本語\x1b[0m mixed ascii",
	"emoji 👍🏽 and 🇯🇵 flags",
	"combining e\u0301\u0301\u0301 marks",
	"tab\there",
	"newline\nembedded",
	"carriage\rreturn",
	"del\x7fchar",
	"nul\x00byte",
	"\x1b[2m  │ \x1b[0m  6  internal/cli/transcript.go:42: some captured tool output line",
	"✓ bash rg --line-number transcript internal/cli [12ms]",
	"↳ you",
	"─────────────────────────",
	"\xff\xfe invalid utf8",
	"valid\xc3\x28mixed",
	"\x1b[\xff\xfemZ",
	strings.Repeat("あ", 60),
	strings.Repeat("\x1b[2mx\x1b[0m", 40),
	"trailing wide あ",
	"\u200bzero width space",
}

var clipWidths = []int{-1, 0, 1, 2, 3, 5, 10, 11, 12, 20, 79, 80, 98, 99, 100, 1000}

func TestClipToWidthMatchesReference(t *testing.T) {
	for _, s := range clipCorpus {
		for _, w := range clipWidths {
			got, want := clipToWidth(s, w), clipToWidthReference(s, w)
			if got != want {
				t.Errorf("clipToWidth(%q, %d) = %q, want %q", s, w, got, want)
			}
		}
	}
}

// TestClipFitsImpliesIdentity pins the contract the fast path relies on:
// clipFits may only report true when the rewrite is genuinely a no-op.
func TestClipFitsImpliesIdentity(t *testing.T) {
	for _, s := range clipCorpus {
		for _, w := range clipWidths {
			if clipFits(s, w) && clipToWidthReference(s, w) != s {
				t.Errorf("clipFits(%q, %d) = true but rewrite changes it to %q",
					s, w, clipToWidthReference(s, w))
			}
		}
	}
}

// TestClipFitsASCIIWidthAssumption pins the assumption that lets the fast
// scan count printable ASCII as one column without consulting runewidth.
func TestClipFitsASCIIWidthAssumption(t *testing.T) {
	for r := rune(0x20); r < 0x7f; r++ {
		if w := runewidth.RuneWidth(r); w != 1 {
			t.Fatalf("runewidth.RuneWidth(%q) = %d, want 1", r, w)
		}
	}
	if !utf8.ValidString("") {
		t.Fatal("sanity")
	}
}

// TestClipToWidthNoAllocOnFit is the point of the exercise: a row that
// already fits must cost zero allocations.
func TestClipToWidthNoAllocOnFit(t *testing.T) {
	rows := []string{
		"plain ascii row well within the width",
		"\x1b[2m  │ \x1b[0m tool output row with dim gutter escapes",
		"日本語 mixed with ascii",
	}
	for _, row := range rows {
		got := testing.AllocsPerRun(100, func() { _ = clipToWidth(row, 100) })
		if got != 0 {
			t.Errorf("clipToWidth(%q, 100) allocated %v times, want 0", row, got)
		}
	}
}

func FuzzClipToWidth(f *testing.F) {
	for _, s := range clipCorpus {
		for _, w := range []int{0, 1, 8, 40} {
			f.Add(s, w)
		}
	}
	f.Fuzz(func(t *testing.T, s string, width int) {
		if width > 4096 || width < -16 { // keep the reference from building huge strings
			return
		}
		got, want := clipToWidth(s, width), clipToWidthReference(s, width)
		if got != want {
			t.Fatalf("clipToWidth(%q, %d) = %q, want %q", s, width, got, want)
		}
	})
}

func BenchmarkClipToWidth(b *testing.B) {
	rows := []string{
		"plain ascii row well within the width",
		"\x1b[2m  │ \x1b[0m    42  internal/cli/transcript.go:7: captured tool output",
		strings.Repeat("x", 200),
	}
	for i, row := range rows {
		b.Run(fmt.Sprint(i), func(b *testing.B) {
			b.ReportAllocs()
			for range b.N {
				_ = clipToWidth(row, 100)
			}
		})
	}
}
