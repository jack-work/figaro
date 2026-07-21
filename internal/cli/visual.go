package cli

import (
	"strings"
	"unicode/utf8"

	"github.com/mattn/go-runewidth"
)

// Visual selection: vim's three modes over the transcript's RENDERED rows —
// the selection space is exactly what's on screen (message headers, rules,
// tool glyphs included), not the semantic node model. Positions are
// (row, col) where row indexes the assembled lines() slice and col is a byte
// offset into the row's VISIBLE text (ANSI-stripped) — the same space search
// match spans live in, so n/N compose with selection for free.
type visualMode uint8

const (
	visualOff    visualMode = iota
	visualCursor            // 'i': visible cursor, no selection — the pager's command mode
	visualChar
	visualLine
	visualColumn
)

// selecting reports whether the mode carries an actual selection (cursor
// mode does not — y/Enter keep their base-pager meanings there).
func (m visualMode) selecting() bool {
	return m == visualChar || m == visualLine || m == visualColumn
}

type visualPos struct {
	row int
	col int // byte offset into the row's visible text
}

// visualSpan is the selected byte range of one row's visible text.
type visualSpan struct {
	row      int
	from, to int // visible-byte range, from <= to
	wholeRow bool
}

// orderedEndpoints returns the selection endpoints in (row, col) order.
func orderedEndpoints(a, c visualPos) (lo, hi visualPos) {
	if a.row < c.row || (a.row == c.row && a.col <= c.col) {
		return a, c
	}
	return c, a
}

// visualSpans computes the per-row selection spans for the rows [fromRow,
// toRow] of the rendered lines. visible returns a row's visible text.
func visualSpans(mode visualMode, anchor, cursor visualPos, visible func(int) string) []visualSpan {
	lo, hi := orderedEndpoints(anchor, cursor)
	var out []visualSpan
	switch mode {
	case visualLine:
		for r := lo.row; r <= hi.row; r++ {
			out = append(out, visualSpan{row: r, from: 0, to: len(visible(r)), wholeRow: true})
		}
	case visualChar:
		for r := lo.row; r <= hi.row; r++ {
			v := visible(r)
			from, to := 0, len(v)
			if r == lo.row {
				from = clampCol(v, lo.col)
			}
			if r == hi.row {
				to = inclusiveEnd(v, hi.col)
			}
			if from > to {
				from = to
			}
			out = append(out, visualSpan{row: r, from: from, to: to})
		}
	case visualColumn:
		// Rectangular: the display-column range spanned by the two endpoint
		// columns, applied to every row (wide runes are included when their
		// starting column falls inside the rectangle).
		c1 := displayCol(visible(anchor.row), anchor.col)
		c2 := displayCol(visible(cursor.row), cursor.col)
		if c1 > c2 {
			c1, c2 = c2, c1
		}
		for r := lo.row; r <= hi.row; r++ {
			v := visible(r)
			from, to := colRangeToBytes(v, c1, c2)
			out = append(out, visualSpan{row: r, from: from, to: to})
		}
	}
	return out
}

// clampCol clamps a byte offset to a rune boundary within v.
func clampCol(v string, col int) int {
	if col < 0 {
		return 0
	}
	if col >= len(v) {
		return len(v)
	}
	for col > 0 && !utf8.RuneStart(v[col]) {
		col--
	}
	return col
}

// inclusiveEnd returns the end of the rune AT col — vim selections include
// the cursor character.
func inclusiveEnd(v string, col int) int {
	col = clampCol(v, col)
	if col >= len(v) {
		return len(v)
	}
	_, sz := utf8.DecodeRuneInString(v[col:])
	return col + sz
}

// displayCol converts a visible-byte offset to a display column.
func displayCol(v string, col int) int {
	col = clampCol(v, col)
	return runewidth.StringWidth(v[:col])
}

// colRangeToBytes maps an inclusive display-column range [c1, c2] to the
// visible-byte range of the runes whose starting column falls inside it.
func colRangeToBytes(v string, c1, c2 int) (from, to int) {
	from, to = len(v), len(v)
	col := 0
	for i := 0; i < len(v); {
		r, sz := utf8.DecodeRuneInString(v[i:])
		w := runewidth.RuneWidth(r)
		if col >= c1 && from == len(v) {
			from = i
		}
		if col > c2 {
			to = i
			break
		}
		col += w
		i += sz
	}
	if from > to {
		from = to
	}
	return from, to
}

// moveCol advances a byte offset by one rune left (-1) or right (+1),
// clamped to the row.
func moveCol(v string, col, dir int) int {
	col = clampCol(v, col)
	if dir > 0 {
		if col >= len(v) {
			return col
		}
		_, sz := utf8.DecodeRuneInString(v[col:])
		next := col + sz
		if next > lastRuneStart(v) {
			return lastRuneStart(v)
		}
		return next
	}
	if col == 0 {
		return 0
	}
	col--
	for col > 0 && !utf8.RuneStart(v[col]) {
		col--
	}
	return col
}

// lastRuneStart is the byte offset of the row's final rune (0 for empty).
func lastRuneStart(v string) int {
	if v == "" {
		return 0
	}
	i := len(v) - 1
	for i > 0 && !utf8.RuneStart(v[i]) {
		i--
	}
	return i
}

const (
	visualBgOn  = "\x1b[48;5;240m" // gray background — composes with fg colors
	visualBgOff = "\x1b[49m"

	cursorCellOn  = "\x1b[48;5;255;38;5;16m" // near-white block, black glyph
	cursorCellOff = "\x1b[49;39m"
)

// overlayCursorCell paints the single cell at the cursor column so the cursor
// reads on screen in every interactive mode (selection bg alone can't show
// WHERE the moving endpoint is). restore is re-asserted after the cell when
// the cursor sits inside a background span (selection / current match) — the
// cell's own closer would otherwise reset that background for the rest of
// the span.
func overlayCursorCell(row string, col int, restore string) string {
	off := cursorCellOff + restore
	v, mp := visibleWithMap(row)
	col = clampCol(v, col)
	end := inclusiveEnd(v, col)
	if col >= len(mp) || end >= len(mp) {
		return row + cursorCellOn + " " + cursorCellOff // cursor past EOL: phantom cell
	}
	lo, hi := mp[col], mp[end]
	if lo == hi { // empty row
		return row + cursorCellOn + " " + cursorCellOff
	}
	var b strings.Builder
	b.Grow(len(row) + 24)
	b.WriteString(row[:lo])
	b.WriteString(cursorCellOn)
	writeReassertingSeq(&b, row[lo:hi], cursorCellOn)
	b.WriteString(off)
	b.WriteString(row[hi:])
	return b.String()
}

// overlayVisual paints one selection span onto a styled row, mapping the
// visible-byte span into the styled string and re-asserting the background
// across the row's own SGR sequences (same discipline as search highlight).
func overlayVisual(row string, from, to int) string {
	if from >= to {
		return row
	}
	_, mp := visibleWithMap(row)
	if from >= len(mp) {
		return row
	}
	if to >= len(mp) {
		to = len(mp) - 1
	}
	lo, hi := mp[from], mp[to]
	var b strings.Builder
	b.Grow(len(row) + 24)
	b.WriteString(row[:lo])
	b.WriteString(visualBgOn)
	writeReassertingSeq(&b, row[lo:hi], visualBgOn)
	b.WriteString(visualBgOff)
	b.WriteString(row[hi:])
	return b.String()
}

// visualYank extracts the selection's visible text, rows joined by newlines
// (trailing per-row padding is not added; column mode yields the rectangle).
func visualYank(spans []visualSpan, visible func(int) string) string {
	var rows []string
	for _, sp := range spans {
		v := visible(sp.row)
		from, to := clampCol(v, sp.from), clampCol(v, sp.to)
		if to > len(v) {
			to = len(v)
		}
		if from > to {
			from = to
		}
		rows = append(rows, v[from:to])
	}
	return strings.Join(rows, "\n")
}

// visualPoint is an LT-anchored selection endpoint: the rendered row indices
// shift as history folds in, so endpoints anchor to the owning message (lt)
// and the line offset within it — the same discipline resize anchoring uses.
type visualPoint struct {
	lt     int
	within int
	col    int
}
