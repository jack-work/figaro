package cli

// navKey names the logical cursor-motion keys. They are the one part of the
// keyboard a terminal cannot deliver as a byte: every one of them arrives as
// an escape sequence: which is why they need a vocabulary of their own
// alongside modifiedKey's byte codes.
type navKey uint8

const (
	navNone navKey = iota
	navUp
	navDown
	navPageUp
	navPageDown
	navHome
	navEnd
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
	}
	if !k.alt && !k.ctrl {
		return k.code, true
	}
	return 0, false
}

const (
	enableModifiedKeyReporting  = "\x1b[>1u"
	disableModifiedKeyReporting = "\x1b[<u"
)

// parseModifiedKey recognizes CSI-u enhanced keyboard reports and the
// portable Alt+Ctrl fallback. It leaves ordinary bytes to the existing input
// path so raw-mode behavior remains unchanged on legacy terminals.
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

// consumeEscapeSequence eats an unhandled ANSI escape sequence starting at
// data[0]==0x1b, so the caller can distinguish an actual Escape keypress
// (bare 0x1b) from a prefix byte of e.g. an arrow key (\x1b[A), an SS3
// F-key (\x1bOP), or an OSC (\x1b]…ST). CSI-u sequences we DO recognize
// are already handled by parseModifiedKey: this is the catch-all so bare
// Esc can drive its own binding without a spurious CSI prefix triggering it.
//
// Returns consumed=0, need=false for a bare Esc (either the buffer holds
// just 0x1b, or 0x1b is followed by a byte that does not begin a known
// sequence). Returns need=true when a sequence is split across reads.
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

// navKeyFor classifies a COMPLETE escape sequence: one already delimited by
// consumeEscapeSequence, as a logical navigation key. Splitting the job in
// two keeps the delimiter (which owns the "bare Esc vs. sequence prefix"
// guarantee and the split-read `need`) untouched, and makes classification a
// total function over a finished sequence.
//
// Anything not in the navigation vocabulary: F-keys, OSC replies, cursor
// position reports, Left/Right: returns ok=false and stays swallowed exactly
// as before.
//
// Both encodings are accepted: CSI (\x1b[A, the normal cursor mode) and SS3
// (\x1bOA, which terminals emit for the arrows once an application turns on
// DECCKM: vim, less and the pager's own alt-screen entry all can). Home and
// End are the worst-behaved keys in the terminal world: xterm sends \x1b[H /
// \x1b[F, the VT220 lineage sends \x1b[1~ / \x1b[4~, and rxvt sends
// \x1b[7~ / \x1b[8~. All six are honored.
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
	}
	return navNone, false // C/D are Left/Right: the pager has no horizontal axis
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

// navParams reads the "<code>;<modifiers>" parameter string of a CSI
// sequence. Absent parameters read as 0. A private-parameter introducer
// (\x1b[<…, \x1b[?…), a non-numeric body, or more than two parameters is not
// a navigation key.
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

// navModifiers decodes the xterm 1-based modifier parameter. Modifiers do not
// change what these keys do today; they are carried so the arrow cluster can
// grow a Shift-extends-selection binding without a second parser.
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
