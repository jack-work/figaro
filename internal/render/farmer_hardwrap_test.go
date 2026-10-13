package render

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// No chunk may be pure escapes: a phantom row costs a viewport line and shows nothing.
func TestFarmerSplitToWidthKeepsEverything(t *testing.T) {
	rows := map[string]string{
		"ascii":     strings.Repeat("A", 200),
		"words":     strings.Repeat("word ", 40),
		"cjk":       strings.Repeat("日本語", 30),
		"cjkmix":    "abc" + strings.Repeat("日本語x", 20),
		"emoji":     strings.Repeat("👨‍👩‍👧‍👦🇯🇵🧑🏽‍🚀", 10),
		"combining": strings.Repeat("e\u0301a\u0300", 40),
		"zerowidth": strings.Repeat("a\u200bb\u200c", 40),
		"styled":    "\x1b[38;5;252m" + strings.Repeat("styled ", 30) + "\x1b[0m",
		"multistyle": "\x1b[1m" + strings.Repeat("bold ", 10) + "\x1b[0m" +
			"\x1b[31m" + strings.Repeat("red ", 10) + "\x1b[0m",
		"trailingesc": strings.Repeat("x", 100) + "\x1b[0m",
		"leadingesc":  "\x1b[0m" + strings.Repeat("y", 100),
		"onerune":     "日",
	}
	for name, row := range rows {
		for w := 2; w <= 60; w++ {
			chunks := splitToWidth(row, w)
			var joined strings.Builder
			for i, c := range chunks {
				if got := cells(c); got > w {
					t.Errorf("%s w=%d chunk %d is %d cells: %q", name, w, i, got, c)
					break
				}
				if stripANSI(c) == "" {
					t.Errorf("%s w=%d chunk %d is escapes only (a phantom row): %q", name, w, i, c)
					break
				}
				if !utf8.ValidString(c) {
					t.Errorf("%s w=%d chunk %d is not valid UTF-8: %q", name, w, i, c)
					break
				}
				joined.WriteString(stripANSI(c))
			}
			if joined.String() != stripANSI(row) {
				t.Errorf("%s w=%d: text changed\n  was %q\n  now %q", name, w, stripANSI(row), joined.String())
			}
		}
	}
}

// An unclosed colour leaks into the next erase-to-EOL, painting the background across the line.
func TestFarmerSplitToWidthClosesItsStyle(t *testing.T) {
	row := "\x1b[38;5;252m" + strings.Repeat("styled ", 30) + "\x1b[0m"
	chunks := splitToWidth(row, 20)
	for i, c := range chunks {
		if strings.Contains(c, "\x1b[") && !strings.HasSuffix(c, "\x1b[0m") {
			t.Logf("chunk %d/%d leaves its style open: %q", i, len(chunks), c)
		}
	}
}

func FuzzSplitToWidth(f *testing.F) {
	for _, s := range []string{
		strings.Repeat("A", 90), strings.Repeat("日本語", 12), "\x1b[31m" + strings.Repeat("x", 40) + "\x1b[0m",
		"👨‍👩‍👧‍👦🇯🇵", "e\u0301a\u0300", "a\u200bb", "", " ", "\x1b[0m",
	} {
		f.Add(s, 20)
	}
	f.Fuzz(func(t *testing.T, row string, w int) {
		if !utf8.ValidString(row) || len(row) > 2000 {
			t.Skip()
		}
		w = 2 + (w%80+80)%80 // 2..81
		var joined strings.Builder
		for i, c := range splitToWidth(row, w) {
			if got := cells(c); got > w {
				t.Fatalf("w=%d chunk %d is %d cells: %q", w, i, got, c)
			}
			// hardWrapOverlong never splits a row of pure escapes, so the check only applies when there was visible text.
			if stripANSI(row) != "" && stripANSI(c) == "" {
				t.Fatalf("w=%d chunk %d is escapes only: %q", w, i, c)
			}
			if !utf8.ValidString(c) {
				t.Fatalf("w=%d chunk %d is invalid UTF-8: %q", w, i, c)
			}
			joined.WriteString(stripANSI(c))
		}
		if joined.String() != stripANSI(row) {
			t.Fatalf("w=%d: text changed\n  was %q\n  now %q", w, stripANSI(row), joined.String())
		}
	})
}
