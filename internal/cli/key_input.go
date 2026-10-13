package cli

import (
	"bytes"
	"strings"
)

// Every nav key arrives as an escape sequence, never a byte.
type navKey uint8

const (
	navNone navKey = iota
	navUp
	navDown
	navPageUp
	navPageDown
	navHome
	navEnd
	// Left/Right exist for the ':' box; modes that do not bind them ignore them.
	navLeft
	navRight
)

type modifiedKey struct {
	code  byte
	ctrl  bool
	shift bool
	alt   bool
	nav   navKey // non-zero for the arrow cluster; code is then meaningless
}

func (k modifiedKey) asByte() (byte, bool) {
	if k.nav != navNone {
		return 0, false // a motion, not a character
	}
	if k.ctrl {
		switch {
		case k.code >= 'a' && k.code <= 'z':
			return k.code & 0x1f, true
		case k.code >= 'A' && k.code <= 'Z':
			return k.code & 0x1f, true
		}
		// Ctrl+punctuation: a CSI-u terminal reports it, a legacy one
		// sends the bare byte. Ctrl-[ is Escape.
		if b, ok := ctrlPunctuation[k.code]; ok {
			return b, true
		}
	}
	if !k.alt && !k.ctrl {
		return k.code, true
	}
	return 0, false
}

// C0 codes 0x00 and 0x1b-0x1f, plus DEL.
var ctrlPunctuation = map[byte]byte{
	'@':  0x00,
	' ':  0x00, // Ctrl-Space is Ctrl-@ on every terminal that reports either
	'[':  0x1b, // Escape
	'\\': 0x1c,
	']':  0x1d,
	'^':  0x1e,
	'_':  0x1f,
	'-':  0x1f, // Ctrl-- is how a keyboard without a shifted _ sends Ctrl-_
	'?':  0x7f, // DEL
}

const (
	enableModifiedKeyReporting  = "\x1b[>1u"
	disableModifiedKeyReporting = "\x1b[<u"

	// Without bracketed paste every bound byte in a paste acts: Tab completes,
	// CR submits.
	enableBracketedPaste  = "\x1b[?2004h"
	disableBracketedPaste = "\x1b[?2004l"

	pasteStart = "\x1b[200~"
	pasteEnd   = "\x1b[201~"
)

// need is true for a paste that has begun and not ended: the caller holds the
// bytes and asks again.
func parseBracketedPaste(data []byte) (text string, consumed int, ok, need bool) {
	if len(data) == 0 || data[0] != 0x1b {
		return "", 0, false, false
	}
	if len(data) < len(pasteStart) {
		// A bare ESC is a key: only a sequence already past the ESC can be
		// an unfinished paste.
		if len(data) >= 2 && strings.HasPrefix(pasteStart, string(data)) {
			return "", 0, false, true
		}
		return "", 0, false, false
	}
	if string(data[:len(pasteStart)]) != pasteStart {
		return "", 0, false, false
	}
	rest := data[len(pasteStart):]
	end := bytes.Index(rest, []byte(pasteEnd))
	if end < 0 {
		return "", 0, false, true
	}
	return string(rest[:end]), len(pasteStart) + end + len(pasteEnd), true, false
}

// Ordinary bytes are left to the raw path, so legacy terminals are unchanged.
func parseModifiedKey(buf []byte) (key modifiedKey, consumed int, ok, need bool) {
	if len(buf) < 2 || buf[0] != 0x1b {
		return modifiedKey{}, 0, false, false
	}
	if buf[1] != '[' {
		switch buf[1] {
		case 0x0e:
			return modifiedKey{code: 'n', ctrl: true, alt: true}, 2, true, false
		case 0x10:
			return modifiedKey{code: 'p', ctrl: true, alt: true}, 2, true, false
		default:
			return modifiedKey{}, 0, false, false
		}
	}
	if len(buf) < 3 {
		return modifiedKey{}, 0, false, true
	}
	i := 2
	code, n, complete := parseKeyNumber(buf[i:])
	if !complete {
		return modifiedKey{}, 0, false, true
	}
	if n == 0 {
		return modifiedKey{}, 0, false, false
	}
	i += n
	modifiers := 1
	if i < len(buf) && buf[i] == ';' {
		i++
		var mn int
		mn, n, complete = parseKeyNumber(buf[i:])
		if !complete {
			return modifiedKey{}, 0, false, true
		}
		if n == 0 {
			return modifiedKey{}, 0, false, false
		}
		i += n
		modifiers = mn
	}
	if i >= len(buf) {
		return modifiedKey{}, 0, false, true
	}
	if buf[i] != 'u' {
		return modifiedKey{}, 0, false, false
	}
	mask := modifiers - 1
	key = modifiedKey{
		ctrl:  mask&4 != 0,
		shift: mask&1 != 0,
		alt:   mask&2 != 0,
	}
	switch code {
	case 14:
		key.code, key.ctrl = 'n', true
	case 16:
		key.code, key.ctrl = 'p', true
	default:
		if code < 0 || code > 255 {
			return modifiedKey{}, 0, false, false
		}
		key.code = byte(code)
	}
	return key, i + 1, true, false
}

func parseKeyNumber(buf []byte) (value, consumed int, complete bool) {
	for consumed < len(buf) && buf[consumed] >= '0' && buf[consumed] <= '9' {
		value = value*10 + int(buf[consumed]-'0')
		consumed++
	}
	return value, consumed, consumed < len(buf)
}

// Alt held, Ctrl not: reducing a Ctrl+Alt report to Meta would drop the Ctrl.
func metaKey(key modifiedKey) (byte, bool) {
	if !key.alt || key.ctrl || key.nav != navNone {
		return 0, false
	}
	if key.code == 0 || key.code >= 128 {
		return 0, false
	}
	return metaFold(key.code), true
}

// ESC <byte> is what a terminal without CSI-u sends for Alt+<byte>; claimed only
// in a mode that binds Meta (metaBoundIn). ESC ESC is two Escapes.
func metaEscapePrefix(data []byte, mode keyMode) (byte, bool) {
	if len(data) < 2 || data[0] != 0x1b {
		return 0, false
	}
	b := data[1]
	if b == 0x1b || b > 0x7f {
		return 0, false
	}
	// Control bytes are left alone (ESC ^C must not eat an interrupt), except
	// Enter: Alt+Enter is ESC CR, or ESC LF from tmux.
	if b == 0x0a {
		b = 0x0d
	}
	if b != 0x7f && b != 0x0d && b < 0x20 {
		return 0, false
	}
	folded := metaFold(b)
	// Chords that open the pager are claimed in every mode.
	if !metaBoundIn(mode, folded) && !metaOpens(folded) {
		return 0, false
	}
	return folded, true
}

// Ctrl+Left/Right as M-b/M-f is the /etc/inputrc line every distribution ships.
func metaForCtrlArrow(key modifiedKey) (byte, bool) {
	if !key.ctrl {
		return 0, false
	}
	switch key.nav {
	case navLeft:
		return 'b', true
	case navRight:
		return 'f', true
	}
	return 0, false
}

// Distinguishes a bare Esc from the prefix of a CSI (\x1b[A), SS3 (\x1bOP) or OSC
// (\x1b]…ST) sequence.
func consumeEscapeSequence(data []byte) (consumed int, need bool) {
	if len(data) == 0 || data[0] != 0x1b {
		return 0, false
	}
	if len(data) == 1 {
		return 0, false // bare Esc (or the trailing byte of a split read)
	}
	switch data[1] {
	case '[': // CSI: params (0x30-0x3f)* intermediates (0x20-0x2f)* final (0x40-0x7e)
		for i := 2; i < len(data); i++ {
			if c := data[i]; c >= 0x40 && c <= 0x7e {
				return i + 1, false
			}
		}
		return 0, true
	case 'O': // SS3: exactly one following byte
		if len(data) < 3 {
			return 0, true
		}
		return 3, false
	case ']': // OSC: terminated by BEL (0x07) or ST (0x1b\)
		for i := 2; i < len(data); i++ {
			if data[i] == 0x07 {
				return i + 1, false
			}
			if data[i] == 0x1b {
				if i+1 >= len(data) {
					return 0, true
				}
				if data[i+1] == '\\' {
					return i + 2, false
				}
				return 0, false // malformed; let caller re-enter
			}
		}
		return 0, true
	default:
		return 0, false // unknown; treat leading 0x1b as bare Esc
	}
}

// Classifies a sequence consumeEscapeSequence has already delimited.
func navKeyFor(seq []byte) (modifiedKey, bool) {
	if len(seq) < 3 || seq[0] != 0x1b {
		return modifiedKey{}, false
	}
	switch seq[1] {
	case 'O': // SS3: exactly one final byte, never parameterized
		if len(seq) != 3 {
			return modifiedKey{}, false
		}
		if nav, ok := navForFinal(seq[2]); ok {
			return modifiedKey{nav: nav}, true
		}
		return modifiedKey{}, false
	case '[':
	default:
		return modifiedKey{}, false
	}
	first, second, ok := navParams(seq[2 : len(seq)-1])
	if !ok {
		return modifiedKey{}, false
	}
	key := navModifiers(second)
	final := seq[len(seq)-1]
	if final == '~' {
		nav, ok := navForTilde(first)
		if !ok {
			return modifiedKey{}, false
		}
		key.nav = nav
		return key, true
	}
	// The letter finals take no parameter, or xterm's "1;<mods>" form.
	if first > 1 {
		return modifiedKey{}, false
	}
	nav, ok := navForFinal(final)
	if !ok {
		return modifiedKey{}, false
	}
	key.nav = nav
	return key, true
}

func navForFinal(final byte) (navKey, bool) {
	switch final {
	case 'A':
		return navUp, true
	case 'B':
		return navDown, true
	case 'H':
		return navHome, true
	case 'F':
		return navEnd, true
	case 'C':
		return navRight, true
	case 'D':
		return navLeft, true
	}
	return navNone, false
}

func navForTilde(code int) (navKey, bool) {
	switch code {
	case 1, 7: // VT220 Find / rxvt Home
		return navHome, true
	case 4, 8: // VT220 Select / rxvt End
		return navEnd, true
	case 5:
		return navPageUp, true
	case 6:
		return navPageDown, true
	}
	return navNone, false
}

// Absent parameters read as 0; a private introducer, a non-numeric body or a third
// parameter is not a nav key.
func navParams(params []byte) (first, second int, ok bool) {
	if len(params) == 0 {
		return 0, 0, true
	}
	fields := 0
	values := [2]int{}
	for i := 0; i < len(params); {
		if fields == len(values) {
			return 0, 0, false
		}
		v, n, _ := parseKeyNumber(params[i:])
		if n == 0 {
			return 0, 0, false
		}
		values[fields] = v
		fields++
		i += n
		if i < len(params) {
			if params[i] != ';' {
				return 0, 0, false
			}
			i++
			if i == len(params) { // trailing ';' with no value
				return 0, 0, false
			}
		}
	}
	return values[0], values[1], true
}

// navModifiers decodes the xterm 1-based modifier parameter.
func navModifiers(param int) modifiedKey {
	if param <= 1 {
		return modifiedKey{}
	}
	mask := param - 1
	return modifiedKey{
		shift: mask&1 != 0,
		alt:   mask&2 != 0,
		ctrl:  mask&4 != 0,
	}
}
