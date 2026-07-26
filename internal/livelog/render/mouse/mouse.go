// Package mouse parses xterm SGR (1006) extended mouse-report sequences.
package mouse

const (
	Enable  = "\x1b[?1000h\x1b[?1006h"
	Disable = "\x1b[?1006l\x1b[?1000l"
)

type Button int

const (
	WheelUp Button = iota
	WheelDown
	WheelLeft
	WheelRight
	Other
)

type Event struct {
	Button  Button
	Base    int
	Mod     int
	X, Y    int
	Pressed bool
}

var prefix = []byte{0x1b, '[', '<'}

// Parse consumes a single SGR mouse sequence from the front of buf.
//
// A LONE 0x1b IS A BARE ESCAPE KEYPRESS, NEVER A CLAIM ON MORE INPUT. Every
// escape decoder in the input pipeline states that rule for itself —
// parseModifiedKey refuses a buffer shorter than two bytes, and
// consumeEscapeSequence resolves a one-byte buffer to bare Esc — because the
// decoders run in sequence and the first one to answer decides. This parser
// used to disagree: `\x1b` alone is a prefix of `\x1b[<`, so it asked for more
// input, and the input loop dutifully stashed the byte in `pending` and stopped
// the frame.
//
// A bare Escape is ALWAYS exactly one byte at the end of a read, so with the
// pager up — the one place mouse reporting is enabled, and the one place Esc
// has a binding — Escape never dispatched on its own frame. It dispatched on
// the NEXT keystroke, which is how Esc cleared the node selection while the
// selection cue stayed painted until the user scrolled.
//
// The cost of the rule is a mouse report split by the kernel BETWEEN its 0x1b
// and its '[', which would now read as Escape plus a stray sequence. That is
// the classic Esc-timeout trade every terminal program makes, and it is the
// right side of it: a split read is a rare race, whereas a dead Escape key was
// certain.
func Parse(buf []byte) (ev Event, consumed int, ok bool, need bool) {
	// Not our sequence at all.
	if !hasPrefixUpTo(buf, prefix) {
		return Event{}, 0, false, false
	}
	if len(buf) < 2 {
		return Event{}, 0, false, false // bare Esc: not ours, and not worth waiting for
	}
	// Full prefix not yet present -> need more.
	if len(buf) < len(prefix) {
		return Event{}, 0, false, true
	}
	i := len(prefix)
	cb, n, done := readInt(buf[i:])
	if !done {
		return Event{}, 0, false, true
	}
	i += n
	if i >= len(buf) {
		return Event{}, 0, false, true
	}
	if buf[i] != ';' {
		return Event{}, 0, false, false
	}
	i++
	cx, n, done := readInt(buf[i:])
	if !done {
		return Event{}, 0, false, true
	}
	i += n
	if i >= len(buf) {
		return Event{}, 0, false, true
	}
	if buf[i] != ';' {
		return Event{}, 0, false, false
	}
	i++
	cy, n, done := readInt(buf[i:])
	if !done {
		return Event{}, 0, false, true
	}
	i += n
	if i >= len(buf) {
		return Event{}, 0, false, true
	}
	term := buf[i]
	if term != 'M' && term != 'm' {
		return Event{}, 0, false, false
	}
	i++

	mod := cb & (4 | 8 | 16)
	base := cb &^ (4 | 8 | 16)
	ev = Event{
		Button:  classify(base),
		Base:    base,
		Mod:     mod,
		X:       cx,
		Y:       cy,
		Pressed: term == 'M',
	}
	return ev, i, true, false
}

func classify(base int) Button {
	switch base {
	case 64:
		return WheelUp
	case 65:
		return WheelDown
	case 66:
		return WheelLeft
	case 67:
		return WheelRight
	}
	return Other
}

// hasPrefixUpTo reports whether buf matches p for min(len(buf), len(p)) bytes.
func hasPrefixUpTo(buf, p []byte) bool {
	n := len(buf)
	if n > len(p) {
		n = len(p)
	}
	for i := 0; i < n; i++ {
		if buf[i] != p[i] {
			return false
		}
	}
	return true
}

// readInt parses a leading run of decimal digits. done=false means the number
// runs to end-of-buffer with no terminator, so the caller must ask for more.
func readInt(b []byte) (v, n int, done bool) {
	for n < len(b) && b[n] >= '0' && b[n] <= '9' {
		v = v*10 + int(b[n]-'0')
		n++
	}
	if n == 0 {
		// No digits at all: treat as "done" so caller sees the malformed byte.
		return 0, 0, true
	}
	if n == len(b) {
		return v, n, false
	}
	return v, n, true
}
