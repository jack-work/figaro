package render

import (
	"regexp"
	"strconv"
	"strings"
)

// FakeTerminal is an in-memory VT implementing Terminal. It interprets the
// subset of ANSI the renderer emits (cursor up/down, CR/LF with scroll, erase
// line, clear screen/scrollback) into a growing line grid, so tests assert on
// exactly what a real terminal would show — deterministically, no tty. It is a
// public type on purpose: it's the shared mock for isolation-testing the
// renderer and the end-to-end pipeline.
//
// Not safe for concurrent use.
type FakeTerminal struct {
	lines    [][]rune
	row, col int
	top      int // index of the viewport's first line (scrollback is < top)
	width    int
	height   int
	autowrap bool
}

// NewFakeTerminal creates a VT of the given size.
func NewFakeTerminal(width, height int) *FakeTerminal {
	return &FakeTerminal{width: width, height: height}
}

func (t *FakeTerminal) Size() (int, int) { return t.width, t.height }

// Resize changes the viewport size, emulating a SIGWINCH: on a shrink the
// cursor is kept inside the new viewport (content scrolls up), as a terminal
// does. The renderer is expected to reconcile on the next draw.
func (t *FakeTerminal) Resize(width, height int) {
	t.width, t.height = width, height
	if t.height > 0 && t.row > t.top+t.height-1 {
		t.top = t.row - (t.height - 1)
	}
}

var fakeCSI = regexp.MustCompile(`^\x1b\[\??([0-9;]*)([A-Za-z])`)

func (t *FakeTerminal) Write(p []byte) (int, error) {
	rs := []rune(string(p))
	for i := 0; i < len(rs); {
		if rs[i] == '\x1b' {
			if m := fakeCSI.FindStringSubmatch(string(rs[i:])); m != nil {
				t.csi(m[1], m[2])
				i += len([]rune(m[0]))
				continue
			}
			i++ // unrecognized escape: skip the ESC
			continue
		}
		switch rs[i] {
		case '\r':
			t.col = 0
		case '\n':
			t.row++
			t.col = 0
			t.scroll()
			t.ensure(t.row)
		default:
			t.put(rs[i])
		}
		i++
	}
	return len(p), nil
}

func (t *FakeTerminal) csi(params, final string) {
	n := 0
	if params != "" {
		n, _ = strconv.Atoi(strings.SplitN(params, ";", 2)[0])
	}
	switch final {
	case "A": // cursor up — clamps at the viewport top (can't enter scrollback)
		t.row -= n
		if t.row < t.top {
			t.row = t.top
		}
		if t.row < 0 {
			t.row = 0
		}
	case "B": // cursor down — clamps at the viewport bottom (no scroll)
		t.row += n
		if t.height > 0 && t.row > t.top+t.height-1 {
			t.row = t.top + t.height - 1
		}
		t.ensure(t.row)
	case "H": // home: top-left of a fresh screen
		t.row, t.col, t.top = 0, 0, 0
	case "K": // erase line (0K from cursor, 2K whole line)
		t.ensure(t.row)
		if n == 2 {
			t.lines[t.row] = nil
			t.col = 0
		} else if t.col < len(t.lines[t.row]) {
			t.lines[t.row] = t.lines[t.row][:t.col]
		}
	case "J":
		switch n {
		case 2, 3: // clear screen / scrollback — full reset (the pi full-redraw)
			t.lines = nil
			t.row, t.col, t.top = 0, 0, 0
		default: // 0J: erase from the cursor to the end of screen (scrollback above kept)
			t.ensure(t.row)
			if t.col < len(t.lines[t.row]) {
				t.lines[t.row] = t.lines[t.row][:t.col]
			}
			t.lines = t.lines[:t.row+1]
		}
	}
}

func (t *FakeTerminal) scroll() {
	if t.height > 0 && t.row > t.top+t.height-1 {
		t.top = t.row - (t.height - 1)
	}
}

func (t *FakeTerminal) ensure(r int) {
	for len(t.lines) <= r {
		t.lines = append(t.lines, nil)
	}
}

func (t *FakeTerminal) put(r rune) {
	t.ensure(t.row)
	for len(t.lines[t.row]) <= t.col {
		t.lines[t.row] = append(t.lines[t.row], ' ')
	}
	t.lines[t.row][t.col] = r
	t.col++
	if t.col >= t.width && t.autowrap {
		t.row++
		t.col = 0
		t.scroll()
		t.ensure(t.row)
	}
}

// Row returns the current cursor row (absolute, untrimmed) — lets tests assert
// where the cursor lands, which Screen() can't show (it trims trailing blanks).
func (t *FakeTerminal) Row() int { return t.row }

// Screen returns the full transcript (scrollback + viewport), trailing blank
// lines trimmed.
func (t *FakeTerminal) Screen() []string {
	out := make([]string, 0, len(t.lines))
	for _, l := range t.lines {
		out = append(out, strings.TrimRight(string(l), " "))
	}
	for len(out) > 0 && out[len(out)-1] == "" {
		out = out[:len(out)-1]
	}
	return out
}
