package cli

import "unicode/utf8"

// Vim motion character classes: whitespace separates, and word vs punct runs
// are distinct words ("foo.bar" is three words).
const (
	classSpace = iota
	classWord
	classPunct
)

func motionClass(r rune) int {
	switch {
	case r == ' ' || r == '\t':
		return classSpace
	case r == '_' || r >= 0x80,
		'a' <= r && r <= 'z',
		'A' <= r && r <= 'Z',
		'0' <= r && r <= '9':
		return classWord
	default:
		return classPunct
	}
}

func prevRuneStart(v string, col int) int {
	col--
	for col > 0 && !utf8.RuneStart(v[col]) {
		col--
	}
	if col < 0 {
		return 0
	}
	return col
}

// wordForward returns the byte offset of the start of the NEXT word ('w'):
// skip the rest of the current word/punct run, then any whitespace, landing
// on the first rune of the next run. At end of line, return the offset of the
// last rune (callers move to the next row themselves).
func wordForward(v string, col int) int {
	if v == "" {
		return 0
	}
	col = clampCol(v, col)
	if col >= len(v) {
		return lastRuneStart(v)
	}
	r, sz := utf8.DecodeRuneInString(v[col:])
	cls := motionClass(r)
	i := col + sz
	if cls != classSpace {
		for i < len(v) {
			r, sz = utf8.DecodeRuneInString(v[i:])
			if motionClass(r) != cls {
				break
			}
			i += sz
		}
	}
	for i < len(v) {
		r, sz = utf8.DecodeRuneInString(v[i:])
		if motionClass(r) != classSpace {
			break
		}
		i += sz
	}
	if i >= len(v) {
		return lastRuneStart(v)
	}
	return i
}

// wordEnd returns the byte offset of the LAST rune of the current-or-next
// word ('e'): if already at a word end, advance to the end of the next word.
// With no further word on the row it returns the last rune's offset.
func wordEnd(v string, col int) int {
	if v == "" {
		return 0
	}
	col = clampCol(v, col)
	// 'e' always moves: step one rune before looking for a run end, so a
	// cursor already at a word end reaches the next word.
	i := col
	if i < len(v) {
		_, sz := utf8.DecodeRuneInString(v[i:])
		i += sz
	}
	for i < len(v) {
		r, sz := utf8.DecodeRuneInString(v[i:])
		if motionClass(r) != classSpace {
			break
		}
		i += sz
	}
	if i >= len(v) {
		return lastRuneStart(v)
	}
	r, sz := utf8.DecodeRuneInString(v[i:])
	cls := motionClass(r)
	for {
		next := i + sz
		if next >= len(v) {
			return i
		}
		r, sz2 := utf8.DecodeRuneInString(v[next:])
		if motionClass(r) != cls {
			return i
		}
		i, sz = next, sz2
	}
}

// wordBack returns the byte offset of the start of the current-or-previous
// word ('b'): if at a word start, move to the previous word's start.
func wordBack(v string, col int) int {
	if v == "" {
		return 0
	}
	col = clampCol(v, col)
	if col == 0 {
		return 0
	}
	i := prevRuneStart(v, col)
	for i > 0 {
		r, _ := utf8.DecodeRuneInString(v[i:])
		if motionClass(r) != classSpace {
			break
		}
		i = prevRuneStart(v, i)
	}
	r, _ := utf8.DecodeRuneInString(v[i:])
	if motionClass(r) == classSpace {
		return 0
	}
	cls := motionClass(r)
	for i > 0 {
		p := prevRuneStart(v, i)
		r, _ = utf8.DecodeRuneInString(v[p:])
		if motionClass(r) != cls {
			break
		}
		i = p
	}
	return i
}

// firstNonBlank returns the byte offset of the first non-whitespace rune
// ('^'); 0 for all-blank or empty rows.
func firstNonBlank(v string) int {
	for i := 0; i < len(v); {
		r, sz := utf8.DecodeRuneInString(v[i:])
		if motionClass(r) != classSpace {
			return i
		}
		i += sz
	}
	return 0
}
