// Package partialjson extracts a top-level string field from a JSON object
// prefix that may be truncated anywhere. It never errors: it returns the
// longest safely-decodable prefix of the value so far, so that repeated calls
// on growing inputs are monotonic — the live preview never rewrites what it
// already showed.
package partialjson

import (
	"unicode/utf16"
	"unicode/utf8"
)

// StringField returns the value of the top-level string field name, decoded
// from the (possibly-truncated) JSON prefix data. present is false when the
// field's value has not begun in the prefix yet, or when the field is present
// but is not a string.
func StringField(data []byte, name string) (value string, present bool) {
	p := parser{b: data}
	if !p.skipWS() || p.peek() != '{' {
		return "", false
	}
	p.i++
	for {
		if !p.skipWS() {
			return "", false
		}
		if p.peek() == '}' {
			return "", false
		}
		key, ok := p.parseCompleteString()
		if !ok {
			return "", false
		}
		if !p.skipWS() || p.peek() != ':' {
			return "", false
		}
		p.i++
		if !p.skipWS() {
			return "", false
		}
		if key == name {
			if p.peek() != '"' {
				return "", false
			}
			p.i++
			return p.decodeTolerant(), true
		}
		if !p.skipValue() {
			return "", false
		}
		if !p.skipWS() {
			return "", false
		}
		if p.peek() != ',' {
			return "", false
		}
		p.i++
	}
}

type parser struct {
	b []byte
	i int
}

func (p *parser) peek() byte {
	if p.i >= len(p.b) {
		return 0
	}
	return p.b[p.i]
}

func (p *parser) skipWS() bool {
	for p.i < len(p.b) {
		switch p.b[p.i] {
		case ' ', '\t', '\n', '\r':
			p.i++
		default:
			return true
		}
	}
	return false
}

// parseCompleteString requires a terminated string; returns false on truncation.
func (p *parser) parseCompleteString() (string, bool) {
	if p.peek() != '"' {
		return "", false
	}
	p.i++
	var buf []byte
	for p.i < len(p.b) {
		c := p.b[p.i]
		if c == '"' {
			p.i++
			return string(buf), true
		}
		if c != '\\' {
			buf = append(buf, c)
			p.i++
			continue
		}
		if p.i+1 >= len(p.b) {
			return "", false
		}
		n, r, ok := decodeEscape(p.b[p.i:])
		if !ok {
			return "", false
		}
		if r < 0 {
			return "", false
		}
		if utf16.IsSurrogate(r) {
			if p.i+n+6 > len(p.b) || p.b[p.i+n] != '\\' || p.b[p.i+n+1] != 'u' {
				return "", false
			}
			_, r2, ok2 := decodeEscape(p.b[p.i+n:])
			if !ok2 {
				return "", false
			}
			buf = utf8.AppendRune(buf, utf16.DecodeRune(r, r2))
			p.i += n + 6
		} else {
			buf = utf8.AppendRune(buf, r)
			p.i += n
		}
	}
	return "", false
}

// decodeEscape decodes a JSON escape starting at b[0]=='\\'. Returns bytes
// consumed and the rune (or -1 for byte escapes that were appended raw via r).
// For \uXXXX returns the raw code unit (may be a surrogate half).
// ok=false means malformed or truncated.
func decodeEscape(b []byte) (n int, r rune, ok bool) {
	if len(b) < 2 {
		return 0, 0, false
	}
	switch b[1] {
	case '"':
		return 2, '"', true
	case '\\':
		return 2, '\\', true
	case '/':
		return 2, '/', true
	case 'n':
		return 2, '\n', true
	case 't':
		return 2, '\t', true
	case 'r':
		return 2, '\r', true
	case 'b':
		return 2, '\b', true
	case 'f':
		return 2, '\f', true
	case 'u':
		if len(b) < 6 {
			return 0, 0, false
		}
		v, ok := parseHex4(b[2:6])
		if !ok {
			return 0, 0, false
		}
		return 6, rune(v), true
	}
	return 0, 0, false
}

func parseHex4(b []byte) (uint16, bool) {
	var r uint16
	for i := 0; i < 4; i++ {
		c := b[i]
		var v byte
		switch {
		case c >= '0' && c <= '9':
			v = c - '0'
		case c >= 'a' && c <= 'f':
			v = c - 'a' + 10
		case c >= 'A' && c <= 'F':
			v = c - 'A' + 10
		default:
			return 0, false
		}
		r = r<<4 | uint16(v)
	}
	return r, true
}

// decodeTolerant reads from the byte after the opening quote, tolerating
// truncation. Returns the longest safe prefix.
func (p *parser) decodeTolerant() string {
	var buf []byte
	for p.i < len(p.b) {
		c := p.b[p.i]
		if c == '"' {
			return string(buf)
		}
		if c == '\\' {
			if p.i+1 >= len(p.b) {
				return string(buf)
			}
			n, r, ok := decodeEscape(p.b[p.i:])
			if !ok {
				// Might just be truncation mid-\uXXXX. Distinguish by looking
				// at whether we have enough bytes for the escape family.
				if p.b[p.i+1] == 'u' && p.i+6 > len(p.b) {
					return string(buf)
				}
				return string(buf)
			}
			if utf16.IsSurrogate(r) {
				if p.i+n+6 > len(p.b) {
					return string(buf)
				}
				if p.b[p.i+n] != '\\' || p.b[p.i+n+1] != 'u' {
					buf = utf8.AppendRune(buf, utf8.RuneError)
					p.i += n
					continue
				}
				_, r2, ok2 := decodeEscape(p.b[p.i+n:])
				if !ok2 {
					return string(buf)
				}
				buf = utf8.AppendRune(buf, utf16.DecodeRune(r, r2))
				p.i += n + 6
				continue
			}
			buf = utf8.AppendRune(buf, r)
			p.i += n
			continue
		}
		if c < 0x80 {
			buf = append(buf, c)
			p.i++
			continue
		}
		// Multi-byte UTF-8 start; hold back if continuation bytes are missing
		// so growth is monotonic.
		want := 0
		switch {
		case c&0xE0 == 0xC0:
			want = 2
		case c&0xF0 == 0xE0:
			want = 3
		case c&0xF8 == 0xF0:
			want = 4
		default:
			buf = append(buf, c)
			p.i++
			continue
		}
		if p.i+want > len(p.b) {
			return string(buf)
		}
		buf = append(buf, p.b[p.i:p.i+want]...)
		p.i += want
	}
	return string(buf)
}

func (p *parser) skipValue() bool {
	if !p.skipWS() {
		return false
	}
	switch c := p.peek(); {
	case c == '"':
		return p.skipString()
	case c == '{' || c == '[':
		return p.skipContainer()
	case c == 't' || c == 'f' || c == 'n':
		return p.skipLiteral()
	default:
		return p.skipNumber()
	}
}

func (p *parser) skipString() bool {
	if p.peek() != '"' {
		return false
	}
	p.i++
	for p.i < len(p.b) {
		c := p.b[p.i]
		if c == '\\' {
			if p.i+1 >= len(p.b) {
				return false
			}
			if p.b[p.i+1] == 'u' {
				if p.i+6 > len(p.b) {
					return false
				}
				p.i += 6
			} else {
				p.i += 2
			}
			continue
		}
		if c == '"' {
			p.i++
			return true
		}
		p.i++
	}
	return false
}

func (p *parser) skipContainer() bool {
	depth := 0
	for p.i < len(p.b) {
		switch p.b[p.i] {
		case '"':
			if !p.skipString() {
				return false
			}
		case '{', '[':
			depth++
			p.i++
		case '}', ']':
			depth--
			p.i++
			if depth == 0 {
				return true
			}
		default:
			p.i++
		}
	}
	return false
}

func (p *parser) skipLiteral() bool {
	rest := p.b[p.i:]
	for _, lit := range []string{"true", "false", "null"} {
		if len(rest) >= len(lit) && string(rest[:len(lit)]) == lit {
			p.i += len(lit)
			return true
		}
	}
	return false
}

func (p *parser) skipNumber() bool {
	start := p.i
	if p.i < len(p.b) && p.b[p.i] == '-' {
		p.i++
	}
	for p.i < len(p.b) {
		c := p.b[p.i]
		if (c >= '0' && c <= '9') || c == '.' || c == 'e' || c == 'E' || c == '+' || c == '-' {
			p.i++
			continue
		}
		break
	}
	// A complete number requires we stopped because of a non-numeric byte,
	// not because we ran off the end.
	if p.i == len(p.b) {
		return false
	}
	return p.i > start
}
