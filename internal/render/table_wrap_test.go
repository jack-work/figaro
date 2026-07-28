package render

import (
	"fmt"
	"strings"
	"testing"

	"github.com/mattn/go-runewidth"
)

// tableCase is a markdown table whose cells do not fit the widths under test,
// so the renderer has to wrap them, plus the cell words that must survive.
type tableCase struct {
	name  string
	md    string
	words []string
}

var tableCases = []tableCase{
	{
		name: "two columns, prose in the second",
		md: "| state | meaning |\n|---|---|\n" +
			"| dormant | not loaded in memory; nothing running |\n" +
			"| idle | loaded, inbox empty (no turn in flight) |\n",
		words: []string{
			"dormant", "not", "loaded", "in", "memory", "nothing", "running",
			"idle", "inbox", "empty", "no", "turn", "flight",
		},
	},
	longCellCase(),
	{
		name: "styled cells (code spans)",
		md: "| flag | effect |\n|---|---|\n" +
			"| `-e` | the aria is not persisted, so there is nothing to clean up |\n",
		words: []string{"-e", "aria", "not", "persisted", "nothing", "clean", "up"},
	},
}

// longCellCase is one row whose second cell needs several lines at any width a
// terminal actually has. The words are NUMBERED so a partial render cannot pass
// by accident: with a repeated filler word, keeping the first line keeps every
// distinct word there is.
func longCellCase() tableCase {
	var cell strings.Builder
	words := []string{"alpha", "description"}
	for i := 0; i < 40; i++ {
		w := fmt.Sprintf("w%02d", i)
		words = append(words, w)
		cell.WriteString(w + " ")
	}
	return tableCase{
		name:  "one long cell",
		md:    "| key | description |\n|---|---|\n| alpha | " + cell.String() + "|\n",
		words: words,
	}
}

// TestProse_TableWrapPreservesContent is the regression test for the bug the
// user reported as "aria prose in a table is always clipped".
//
// The loss happened INSIDE the markdown renderer, upstream of anything either
// view could do about it: glamour v0.8.0's ansi.TableElement.setStyles gave
// every cell a lipgloss style with Inline(true), which disables word wrap in
// the cell render, while lipgloss/table sized each row to the WRAPPED height of
// its content. So a cell that needed two lines got two lines of space, its
// first line of text, and a blank — the remainder was discarded by the cell's
// MaxWidth. render.Prose therefore returned rows that were comfortably inside
// the requested width (clipToWidth is innocent) but with the text already gone.
//
// The assertion is content preservation, not layout: every word of every cell
// must appear somewhere in the rendered rows.
func TestProse_TableWrapPreservesContent(t *testing.T) {
	for _, tc := range tableCases {
		for _, w := range []int{30, 40, 50, 60, 80} {
			rows := Prose(tc.md, w)
			// Words may land on either side of a wrap, so match against the
			// whitespace-collapsed concatenation of every row, reduced to bare
			// words by unbox so column rules and punctuation do not hide a hit.
			got := " " + strings.Join(strings.Fields(unbox(stripANSI(strings.Join(rows, " ")))), " ") + " "
			for _, word := range tc.words {
				if !strings.Contains(got, " "+word+" ") {
					t.Errorf("%s @ width %d: cell word %q lost\nrendered:\n%s",
						tc.name, w, word, indentRows(rows))
				}
			}
		}
	}
}

// TestProse_TableRowsHoldPainterInvariant guards the invariant a wrapping fix
// is most likely to break: architecture.md invariant #1, ONE PHYSICAL LINE PER
// ROW. Every row render.Prose hands back must fit the requested width and carry
// no embedded newline, or the live painter's one-row-per-line cursor math
// desyncs and rows duplicate or vanish.
//
// The renderer is asked to wrap to width-2 (the dark style adds a 2-column
// document margin on top of the wrap width), so width is the ceiling.
func TestProse_TableRowsHoldPainterInvariant(t *testing.T) {
	for _, tc := range tableCases {
		for w := 8; w <= 120; w += 7 {
			for i, row := range Prose(tc.md, w) {
				if strings.ContainsAny(row, "\n\r\t") {
					t.Errorf("%s @ width %d: row %d smuggles a control char: %q", tc.name, w, i, row)
				}
				if got := runewidth.StringWidth(stripANSI(row)); got > w {
					t.Errorf("%s @ width %d: row %d is %d columns wide: %q", tc.name, w, i, got, stripANSI(row))
				}
			}
		}
	}
}

// unbox reduces a rendered row to bare words: everything that is not a letter,
// digit or hyphen becomes a space. That makes the table's box-drawing rules
// word boundaries (without it a cell abutting its column rule reads as
// "dormant│not", one word, and both halves look lost) and lets a cell's
// trailing punctuation ("memory;", "flight)") match the bare word.
func unbox(s string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-':
			return r
		}
		return ' '
	}, s)
}

func indentRows(rows []string) string {
	var b strings.Builder
	for _, r := range rows {
		b.WriteString("    |" + stripANSI(r) + "|\n")
	}
	return b.String()
}
