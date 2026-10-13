package render

import (
	"strings"
	"unicode/utf8"

	"github.com/mattn/go-runewidth"

	fig "github.com/jack-work/figaro/internal/render"
)

// Width is the display width of a styled row: escapes count for nothing, a
// wide rune for its cells.
func Width(s string) int {
	col := 0
	rs := []rune(s)
	for i := 0; i < len(rs); {
		if rs[i] == '\x1b' {
			j := i + 1
			for j < len(rs) && !isLetter(rs[j]) {
				j++
			}
			if j < len(rs) {
				j++
			}
			i = j
			continue
		}
		col += runewidth.RuneWidth(rs[i])
		i++
	}
	return col
}

// OverlayRight sets tag flush against the right edge of a row, keeping the
// row exactly width columns wide.
func OverlayRight(line, tag string, width int) string {
	if tag == "" || width <= 0 {
		return line
	}
	tw := Width(tag)
	if tw >= width {
		return clip(tag, width)
	}
	room := width - tw - 1
	left := clip(line, room)
	return left + strings.Repeat(" ", room-Width(left)) + " " + tag
}

// GutterCols is the width of the right gutter, held for the adornment glyph;
// everything else drawn against the right edge stops short of it.
const GutterCols = 1

// OverlayGutter puts glyph in the right gutter of a row, keeping its width.
func OverlayGutter(line, glyph string, width int) string {
	if glyph == "" || width <= GutterCols {
		return line
	}
	room := width - GutterCols
	left := clip(line, room)
	return left + strings.Repeat(" ", room-Width(left)) + glyph
}

// OverlayColumn replaces display column col of a styled row with glyph, which
// must be one column wide; escapes pass through uncounted. A glyph already
// there returns the row as it came, so a no-op repaint costs no copy.
func OverlayColumn(line string, col int, glyph string) string {
	if glyph == "" || col < 0 {
		return line
	}
	at, i := 0, 0
	for i < len(line) {
		if line[i] == '\x1b' {
			i = fig.SkipEscape(line, i)
			continue
		}
		r, size := utf8.DecodeRuneInString(line[i:])
		w := runewidth.RuneWidth(r)
		if at == col {
			if line[i:i+size] == glyph {
				return line
			}
			var b strings.Builder
			b.Grow(len(line) + len(glyph))
			b.WriteString(line[:i])
			b.WriteString(glyph)
			// A wide rune gives up both its cells.
			if w > 1 {
				b.WriteString(strings.Repeat(" ", w-1))
			}
			b.WriteString(line[i+size:])
			return b.String()
		}
		at += w
		i += size
	}
	if at <= col {
		return line + strings.Repeat(" ", col-at) + glyph
	}
	return line
}

// clip truncates s to width display columns and flattens control characters
// to spaces, so every emitted row is exactly one physical line, which the
// cursor math depends on. A reset is appended if the cut was mid-style.
func clip(s string, width int) string {
	if width <= 0 {
		return ""
	}
	var b strings.Builder
	col := 0
	clipped := false
	rs := []rune(s)
	for i := 0; i < len(rs); {
		if rs[i] == '\x1b' {
			j := i + 1
			for j < len(rs) && !isLetter(rs[j]) {
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
		if r < 0x20 || r == 0x7f {
			r = ' '
		}
		// Cells, not runes: a wide rune occupies two columns.
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

// hardWrap wraps each paragraph of s to at most width columns, preserving
// explicit newlines.
func hardWrap(s string, width int) []string {
	if width < 1 {
		width = 1
	}
	var out []string
	for _, para := range strings.Split(s, "\n") {
		if para == "" {
			out = append(out, "")
			continue
		}
		col := 0
		var b strings.Builder
		for _, r := range para {
			w := runewidth.RuneWidth(r)
			if col+w > width {
				out = append(out, b.String())
				b.Reset()
				col = 0
			}
			b.WriteRune(r)
			col += w
		}
		out = append(out, b.String())
	}
	return out
}

func isLetter(r rune) bool { return (r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z') }
