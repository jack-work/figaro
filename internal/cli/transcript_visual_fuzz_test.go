package cli

import (
	"strings"
	"testing"
	"unicode"
	"unicode/utf8"

	"github.com/jack-work/figaro/api/livedoc"
	"github.com/jack-work/figaro/internal/term"
	"github.com/mattn/go-runewidth"
)

// FuzzVisualPaint drives washColumns and cursorCell over every row shape the
// farmer corpus renders, at random ranges and cursor columns, and checks the
// painted row against a cell interpreter:
//
//   - the visible text is untouched;
//   - exactly the cells in [from, to) carry the wash background, none
//     outside it, and a cursor inside the range keeps the wash after it;
//   - the cursor cell alone carries the cursor's background;
//   - the row still ends in the default rendition, which is what compactRow
//     requires of every painted row.
func FuzzVisualPaint(f *testing.F) {
	for _, md := range farmerCorpus() {
		f.Add(md, 60, 3, 17, 9)
	}
	f.Add("plain", 40, 0, 5, 2)
	f.Add("\x1b[1mbold\x1b[0m and \x1b[2mdim\x1b[0m", 30, 2, 9, 12)
	f.Add("日本語 wide", 20, 1, 4, 2)
	f.Fuzz(func(t *testing.T, md string, w, from, to, col int) {
		if !utf8.ValidString(md) || len(md) > 3000 {
			t.Skip()
		}
		defer term.SetColorMode(term.ColorAlways)()
		t.Setenv("COLORTERM", "truecolor")
		wash := term.SelectWash()
		// The interpreter spells colours its own way; ask it.
		probe := func(body string) string {
			v := newVT(2, 1)
			v.Write([]byte("\x1b[1;1H" + body + "x"))
			return v.cells[0][0].s.bg
		}
		washBG, curBG := probe(wash), probe(term.Cursor())
		w = 20 + (w%181+181)%181
		rows := nodeProseRows(livedoc.Node{Type: livedoc.NodeProse, Markdown: md}, w)
		if len(rows) == 0 {
			t.Skip()
		}
		row := plainNodeRow(rows[(from%len(rows)+len(rows))%len(rows)], w)
		from = (from%(w+1) + (w + 1)) % (w + 1)
		to = (to%(w+2) + (w + 2)) % (w + 2)
		col = (col%w + w) % w
		if from > to {
			from, to = to, from
		}
		restore := ""
		if col >= from && col < to {
			restore = wash
		}
		painted := cursorCell(washColumns(row, from, to), col, restore, to)

		if stripANSI(painted) != stripANSI(row) && strings.TrimRight(stripANSI(painted), " ") != strings.TrimRight(stripANSI(row), " ") {
			t.Fatalf("visible text changed:\n%q\n%q", stripANSI(row), stripANSI(painted))
		}
		vt := newVT(w+8, 1)
		vt.Write([]byte("\x1b[1;1H"))
		vt.Write([]byte(painted))
		if vt.cur.bg != "" || vt.cur.reverse {
			t.Fatalf("the painted row did not end in the default rendition: %+v\n%q", vt.cur, painted)
		}
		// Expectations are per RUNE, because a wide rune cannot be half
		// painted: a rune whose cells intersect [from, to) is painted whole,
		// and the cursor is the whole rune under its column.
		plain := []rune(stripANSI(row))
		for _, r := range append([]rune(md), plain...) {
			if runewidth.RuneWidth(r) == 0 || !unicode.IsPrint(r) || unicode.In(r, unicode.Mn, unicode.Me, unicode.Cf) || r >= 0x1F000 {
				// Combining marks, joiners, flags, skin tones and emoji: the
				// painter, the clipper and the interpreter count them
				// differently, and so does every terminal. The text and the final rendition were
				// checked above; the per-cell claim is not one this fixture
				// can make.
				t.Skip()
			}
		}
		type span struct{ from, to int }
		var cursorSpan span
		wantWash := map[int]bool{} // by cell
		c0 := 0
		for _, r := range plain {
			rw := runeCells(r)
			isCursor := c0 <= col && col < c0+rw
			if isCursor {
				cursorSpan = span{c0, c0 + rw}
			}
			if from < to && c0+rw > from && c0 < to {
				for c := c0; c < c0+rw; c++ {
					wantWash[c] = true
				}
			}
			c0 += rw
		}
		if cursorSpan.to == 0 {
			cursorSpan = span{col, col + 1} // past the text: one padded cell
		}
		for c := max(c0, from); c < to; c++ {
			wantWash[c] = true // padding, washed from `from` up to `to`
		}
		for c := 0; c < w+8; c++ {
			bg := vt.cells[0][c].s.bg
			switch {
			case c >= cursorSpan.from && c < cursorSpan.to:
				if bg != curBG {
					t.Fatalf("col %d (cursor) bg=%q want cursor\n%q", c, bg, painted)
				}
			case wantWash[c]:
				if bg != washBG {
					t.Fatalf("col %d in [%d,%d) bg=%q want wash\n%q", c, from, to, bg, painted)
				}
			default:
				if bg == washBG || bg == curBG {
					t.Fatalf("col %d outside [%d,%d) and not the cursor (%d) carries bg=%q\n%q", c, from, to, col, bg, painted)
				}
			}
		}
	})
}
