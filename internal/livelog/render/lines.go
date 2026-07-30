package render

import (
	"strings"

	"github.com/mattn/go-runewidth"
)

// clip truncates s to at most width display columns and flattens embedded
// control characters (newline/tab/CR/<0x20) to spaces, guaranteeing every
// emitted row is exactly one physical line — the invariant the renderer's
// cursor math depends on. ANSI escape sequences pass through uncounted; a reset
// is appended if the line was cut mid-style so color can't bleed.
//
// Width is approximated as one column per rune (no East-Asian-width table) to
// keep the module dependency-free; that is sufficient for the rendering algorithm
// and easy to swap later.
func clip(s string, width int) string {
	if width <= 0 {
		return ""
	}
	var b strings.Builder
	col := 0
	clipped := false
	rs := []rune(s)
	for i := 0; i < len(rs); {
		if rs[i] == '\x1b' { // copy the whole escape sequence, uncounted
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
		// CELLS, NOT RUNES. A CJK ideograph or an emoji occupies two columns,
		// so counting runes let a row "clipped to width" occupy width + the
		// number of wide runes on it — measured at +12 on a 60-column pane
		// for one line of Japanese, and at exactly +1 for a line carrying a
		// single wide rune, which is the master's "one or two characters
		// beyond the right edge". A row wider than the viewport wraps in the
		// terminal (tmux: the UI breaks up) or is hidden (nvim nowrap: it
		// obscures the right of the GUI) — the two symptoms are one bug.
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

// hardWrap wraps each paragraph of s to at most width COLUMNS, preserving
// explicit newlines. It used to say "width counts runes", and did, which is
// the same defect clip carried: tool output (nodeview.go wraps bodies through
// here) ran past the right edge by one column per wide rune.
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
