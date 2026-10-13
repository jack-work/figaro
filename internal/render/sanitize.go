package render

import (
	"strings"
)

// SanitizeForTerminal removes terminal-state-mutating sequences, keeping SGR and cursor/erase.
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
		if i+1 >= len(s) {
			return b.String()
		}
		next := s[i+1]

		switch next {
		case '[':
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
				return b.String()
			}
			final := s[j]
			full := s[i : j+1]
			switch {
			case privateMode:
			case final == 'm':
				b.WriteString(full)
			case isCursorOrEraseFinal(final):
				b.WriteString(full)
			default:
			}
			i = j + 1

		case ']':
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
			i += 2
		}
	}
	return b.String()
}

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
	case b == '?':
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

// StripEscapes removes every ESC-introduced sequence and any stray ESC.
func StripEscapes(s string) string {
	if !strings.ContainsRune(s, 0x1b) {
		return s
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

// SkipEscape is the one escape grammar every package measures with.
func SkipEscape(s string, i int) int { return skipEscape(s, i) }

// runeLen reads the length from the lead byte: utf8 has no decoder generic over both representations.
func runeLen[T ~string | ~[]byte](s T, i int) int {
	c := s[i]
	switch {
	case c < 0x80:
		return 1
	case c < 0xC0:
		return 1
	case c < 0xE0:
		return min(2, len(s)-i)
	case c < 0xF0:
		return min(3, len(s)-i)
	default:
		return min(4, len(s)-i)
	}
}

func SkipEscapeBytes(b []byte, i int) int { return skipEscape(b, i) }

// skipEscape returns the index just past the escape sequence at s[i] == ESC.
func skipEscape[T ~string | ~[]byte](s T, i int) int {
	i++
	if i >= len(s) {
		return i
	}
	switch c := s[i]; {
	case c == '[':
		// CSI: scan to a final byte, but a space or control byte means a
		// truncated escape, and the text after it must survive.
		i++
		for i < len(s) {
			c := s[i]
			if c >= 0x40 && c <= 0x7e {
				i++
				break
			}
			if c == 0x20 || c < 0x20 {
				break
			}
			i++
		}
	case c == ']' || c == 'P' || c == '_' || c == '^' || c == 'X':
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
	case c == 'N' || c == 'O':
		i += 2
	case c == '(' || c == ')' || c == '*' || c == '+' || c == '-' || c == '.' || c == '/':
		i += 2
	default:
		// A bare ESC before text: step one rune, not one byte.
		sz := runeLen(s, i)
		i += sz
	}
	if i > len(s) {
		i = len(s)
	}
	return i
}
