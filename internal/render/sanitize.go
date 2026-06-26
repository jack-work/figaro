// SanitizeForTerminal — strips terminal-state-mutating ANSI sequences
// from text destined for figaro's live region.
//
// Tool output (bash stdout, model-emitted text, anything originating
// outside figaro's painter) can carry embedded ANSI control sequences
// that mutate global terminal state: alt-screen mode, cursor
// visibility, line wrap, mouse modes, scroll regions, the OS window
// title. If those bytes reach the host terminal, figaro's render loop
// becomes incoherent — the painter thinks it owns the cursor; the
// terminal thinks it's in alt-screen. Recovery is a `tput reset` or,
// pathologically, restarting figaro.
//
// We're tight and conservative: drop everything that touches terminal
// state, keep SGR (colors/style) and the cursor primitives glamour
// itself emits. Applied at every Prose render and as defense-in-depth
// at the painter's write boundary.
//
// References:
//   - DEC private modes (CSI ? Pn h/l): xterm/ECMA-48 set/reset.
//   - OSC: operating system command (set title, palette, hyperlink).
//   - DECSC/DECRC (ESC 7 / ESC 8): cursor save/restore.
//   - RIS (ESC c): full terminal reset.

package render

import "strings"

// SanitizeForTerminal returns s with terminal-state-mutating ANSI
// sequences removed. Pure function; preserves SGR and cursor/erase
// primitives. Safe to call repeatedly.
//
// Drops:
//
//	CSI ? N {h,l}    DEC private modes (alt-screen 1049/47, cursor
//	                 visibility 25, line wrap 7, mouse 1000-1006,
//	                 application cursor keys 1, bracketed paste 2004).
//	ESC ] ... BEL    OSC (set title, palette, hyperlink). Also
//	ESC ] ... ESC \  ST-terminated OSC.
//	ESC c            RIS — full terminal reset.
//	ESC 7, ESC 8     DECSC / DECRC — cursor save / restore.
//	ESC =, ESC >     Application / numeric keypad mode.
//	ESC ( B/0        Charset selection.
//	CSI N r          DECSTBM — scroll region.
//	CSI N s/u        Cursor save / restore (CSI variant).
//
// Kept:
//
//	CSI N m          SGR (color, bold, italic). The whole point.
//	CSI N {A-H,J,K}  Cursor moves and erase — figaro's painter uses
//	                 these; glamour emits them too.
func SanitizeForTerminal(s string) string {
	if s == "" || !strings.ContainsRune(s, 0x1b) {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	i := 0
	for i < len(s) {
		c := s[i]
		if c != 0x1b {
			b.WriteByte(c)
			i++
			continue
		}
		// ESC at end-of-string — drop.
		if i+1 >= len(s) {
			return b.String()
		}
		next := s[i+1]

		switch next {
		case '[':
			// CSI sequence: parameter bytes then a final byte in 0x40..0x7e.
			j := i + 2
			privateMode := false
			if j < len(s) && s[j] == '?' {
				privateMode = true
				j++
			}
			for j < len(s) && isCSIParamByte(s[j]) {
				j++
			}
			if j >= len(s) {
				return b.String() // incomplete
			}
			final := s[j]
			full := s[i : j+1]
			switch {
			case privateMode:
				// Always dangerous — drop.
			case final == 'm':
				// SGR — keep verbatim.
				b.WriteString(full)
			case isCursorOrEraseFinal(final):
				// Cursor / erase — keep.
				b.WriteString(full)
			default:
				// Other CSIs (DECSTBM 'r', save/restore 's'/'u',
				// device attributes 'c', etc.) — drop.
			}
			i = j + 1

		case ']':
			// OSC: ESC ] ... terminator (BEL or ST).
			j := i + 2
			for j < len(s) {
				if s[j] == 0x07 {
					j++
					break
				}
				if s[j] == 0x1b && j+1 < len(s) && s[j+1] == '\\' {
					j += 2
					break
				}
				j++
			}
			i = j

		case 'c', '7', '8', '=', '>':
			i += 2

		case '(', ')':
			if i+2 < len(s) {
				i += 3
			} else {
				return b.String()
			}

		default:
			// Unknown ESC <byte> — drop two bytes.
			i += 2
		}
	}
	return b.String()
}

// SanitizeRows applies SanitizeForTerminal to each row in place.
func SanitizeRows(rows []string) []string {
	for i, r := range rows {
		rows[i] = SanitizeForTerminal(r)
	}
	return rows
}

func isCSIParamByte(b byte) bool {
	switch {
	case b >= '0' && b <= '9':
		return true
	case b == ';' || b == ':':
		return true
	case b == '<' || b == '=' || b == '>':
		return true
	case b == ' ' || b == '!':
		return true
	}
	return false
}

func isCursorOrEraseFinal(b byte) bool {
	switch b {
	case 'A', 'B', 'C', 'D',
		'E', 'F',
		'G', 'H',
		'J', 'K':
		return true
	}
	return false
}
