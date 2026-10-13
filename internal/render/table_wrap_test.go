package render

import (
	"fmt"
	"strings"
	"testing"

	"github.com/mattn/go-runewidth"
)

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

// Numbered words, so keeping only the first line cannot pass by accident.
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

// Every word of every cell must appear somewhere in the rendered rows.
func TestProse_TableWrapPreservesContent(t *testing.T) {
	for _, tc := range tableCases {
		for _, w := range []int{30, 40, 50, 60, 80} {
			rows := Prose(tc.md, w)
			// Words may land on either side of a wrap; match against the collapsed concatenation.
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

// Every row must fit the width and carry no newline, or the live painter's cursor math desyncs.
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

// Box-drawing rules and punctuation become word boundaries.
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
