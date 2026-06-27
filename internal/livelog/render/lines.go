package render

import (
	"strings"
	"unicode/utf8"
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
		if col+1 > width {
			clipped = true
			break
		}
		b.WriteRune(r)
		col++
		i++
	}
	if clipped {
		b.WriteString("\x1b[0m")
	}
	return b.String()
}

// hardWrap wraps each paragraph of s to at most width columns, preserving
// explicit newlines. Width counts runes.
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
			if col+1 > width {
				out = append(out, b.String())
				b.Reset()
				col = 0
			}
			b.WriteRune(r)
			col++
		}
		out = append(out, b.String())
	}
	return out
}

// displayWidth counts visible columns in s, skipping ANSI escape sequences.
func displayWidth(s string) int {
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
		col++
		i++
	}
	return col
}

func isLetter(r rune) bool { return (r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z') }

var _ = utf8.RuneCountInString // reserved for a future width strategy
