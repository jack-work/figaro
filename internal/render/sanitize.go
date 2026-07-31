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

// StripEscapes removes every ESC-introduced sequence, and any stray ESC, from
// text that is about to be RENDERED AS MARKDOWN.
//
// It is the input-side counterpart to SanitizeForTerminal, and it exists
// because that function is the wrong tool here: SanitizeForTerminal's job is to
// protect the host terminal from state-mutating sequences in output figaro is
// about to print, so it deliberately KEEPS SGR verbatim. Handing markdown to
// glamour with SGR still in it produced a row four cells wider than the width
// it was given, at every width, with "[31m" printed as visible text — glamour
// drops the ESC byte, keeps the parameter bytes as content, and has already
// wrapped as though the whole sequence were zero-width.
//
// Models paste ANSI out of tool output constantly, so this is a live path. The
// right answer for markdown is that an escape is not content and not styling:
// it is noise, and it goes.
func StripEscapes(s string) string {
	if !strings.ContainsRune(s, 0x1b) {
		return s // the overwhelmingly common case, untouched and unallocated
	}
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); {
		if s[i] != 0x1b {
			b.WriteByte(s[i])
			i++
			continue
		}
		i = skipEscape(s, i)
	}
	return b.String()
}

// skipEscape returns the index just past the escape sequence beginning at i
// (s[i] == ESC), consuming the WHOLE sequence for every form the terminal
// grammar defines. Each arm below is a leak that was measured, not imagined:
//
//	CSI      ESC [ params intermediates final — '?' is a PARAMETER byte, and
//	         omitting it left "\x1b[?25l" printing "25l". That is cursor-hide;
//	         with alt-screen it is what every pasted tool transcript carries.
//	OSC      ESC ] … BEL or ST
//	DCS/…    ESC P, ESC _, ESC ^, ESC X … ST — payloads, not two-byte escapes
//	SS2/SS3  ESC N, ESC O + one byte — "\x1bOP" printed "P"
//	charset  ESC ( ) * + - . / + one byte — "\x1b(B" printed "B"
//	other    ESC + one byte
func skipEscape(s string, i int) int {
	i++ // ESC
	if i >= len(s) {
		return i
	}
	switch c := s[i]; {
	case c == '[':
		i++
		for i < len(s) && s[i] >= 0x30 && s[i] <= 0x3f { // params, incl. ? < = > ; :
			i++
		}
		// Intermediates EXCLUDING space (0x21..0x2f, not 0x20). Space is a
		// legal intermediate — `ESC [ Ps SP q` sets the cursor style — but a
		// TRUNCATED sequence followed by ordinary prose is far more common in
		// model-pasted text, and treating space as an intermediate ate the next
		// word's first letter: "trunc \x1b[38;5; and text" printed "trunc nd
		// text". The trade is one leaked final byte from a rare cursor-style
		// sequence against a swallowed word, and the word wins.
		for i < len(s) && s[i] >= 0x21 && s[i] <= 0x2f {
			i++
		}
		if i < len(s) && s[i] >= 0x40 && s[i] <= 0x7e { // a final byte, or nothing
			i++
		}
	case c == ']' || c == 'P' || c == '_' || c == '^' || c == 'X':
		// String-payload sequences: run to BEL or ST.
		i++
		for i < len(s) {
			if s[i] == 0x07 {
				return i + 1
			}
			if s[i] == 0x1b && i+1 < len(s) && s[i+1] == '\\' {
				return i + 2
			}
			i++
		}
	case c == 'N' || c == 'O': // single shifts: one byte follows
		i += 2
	case c == '(' || c == ')' || c == '*' || c == '+' || c == '-' || c == '.' || c == '/':
		i += 2 // charset designator: one byte follows
	default:
		i++
	}
	if i > len(s) {
		i = len(s)
	}
	return i
}
