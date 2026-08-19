// SanitizeForTerminal: strips terminal-state-mutating ANSI sequences
// from text destined for figaro's live region.

package render

import (
	"strings"
	"unicode/utf8"
)

// SanitizeForTerminal returns s with terminal-state-mutating ANSI
// sequences removed. Pure function; preserves SGR and cursor/erase
// primitives. Safe to call repeatedly.
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
		// ESC at end-of-string: drop.
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
				// Always dangerous: drop.
			case final == 'm':
				// SGR: keep verbatim.
				b.WriteString(full)
			case isCursorOrEraseFinal(final):
				// Cursor / erase: keep.
				b.WriteString(full)
			default:
				// Other CSIs (DECSTBM 'r', save/restore 's'/'u',
				// device attributes 'c', etc.): drop.
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
			// Unknown ESC <byte>: drop two bytes.
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
	case b == '?': // a PARAMETER byte. Omitting it leaked "\x1b[?25l" as "25l".
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

// SkipEscape is skipEscape, exported so every package measures escapes with
// ONE grammar. internal/cli had its own, which scanned to the first ASCII
// LETTER: a bare ESC therefore swallowed the character after it, displayWidth
// undercounted by one, and clipToWidth let a row through one cell PAST THE
// EDGE: the reported "one or two characters beyond the right side". An OSC
// title ended it early instead, which clips a row short and loses text.
func SkipEscape(s string, i int) int { return skipEscape(s, i) }

// skipEscape returns the index just past the escape sequence beginning at i
// (s[i] == ESC), consuming the WHOLE sequence for every form the terminal
// grammar defines. Each arm below is a leak that was measured, not imagined:
func skipEscape(s string, i int) int {
	i++ // ESC
	if i >= len(s) {
		return i
	}
	switch c := s[i]; {
	case c == '[':
		// CSI: scan to a FINAL byte (0x40..0x7e), the way a terminal does: so
		// "\x1b[\xff\xfem" is consumed through its 'm' rather than leaking two
		// replacement characters. But abort at a space or a control byte and
		// consume only what was scanned: a real CSI does not contain them, and
		// treating them as part of the sequence is what let a TRUNCATED escape
		// eat the next word ("trunc \x1b[38;5; and text" -> "trunc nd text").
		// Malformed prefix dropped, following text untouched.
		i++
		for i < len(s) {
			c := s[i]
			if c >= 0x40 && c <= 0x7e { // final byte
				i++
				break
			}
			if c == 0x20 || c < 0x20 { // malformed: stop, keep the text
				break
			}
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
		// One RUNE, not one byte. A bare ESC before ordinary text is not a
		// sequence at all, and advancing a single byte cut a multi-byte rune in
		// half: "\x1b\u0631" came back as invalid UTF-8. Only reachable from
		// splitToWidth, and only because that used to carry its own second copy
		// of this scanner; there is one now.
		_, sz := utf8.DecodeRuneInString(s[i:])
		if sz == 0 {
			sz = 1
		}
		i += sz
	}
	if i > len(s) {
		i = len(s)
	}
	return i
}
