package render

import (
	"regexp"
	"strconv"
	"strings"
)

// FakeTerminal is an in-memory VT interpreting the ANSI subset the renderer
// emits into a line grid, so tests assert on what a real terminal would show.
type FakeTerminal struct {
	lines    [][]rune
	row, col int
	top      int // index of the viewport's first line (scrollback is < top)
	width    int
	height   int
	autowrap bool

	// Scroll region (DECSTBM), 0-based and relative to the viewport top.
	// regSet distinguishes the reset state from a region that happens to
	// cover the viewport, so a resize keeps tracking.
	regTop, regBot int
	regSet         bool
}

func NewFakeTerminal(width, height int) *FakeTerminal {
	return &FakeTerminal{width: width, height: height}
}

func (t *FakeTerminal) Size() (int, int) { return t.width, t.height }

// Resize emulates a SIGWINCH: on a shrink the cursor is kept inside the new
// viewport, as a terminal does.
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
			i++
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
	case "A": // cursor up clamps at the viewport top
		t.row -= n
		if t.row < t.top {
			t.row = t.top
		}
		if t.row < 0 {
			t.row = 0
		}
	case "B": // cursor down clamps at the viewport bottom, no scroll
		t.row += n
		if t.height > 0 && t.row > t.top+t.height-1 {
			t.row = t.top + t.height - 1
		}
		t.ensure(t.row)
	case "H": // CUP: 1-based row;col, default 1;1
		row, col := 1, 1
		if params != "" {
			p := strings.SplitN(params, ";", 2)
			if p[0] != "" {
				row, _ = strconv.Atoi(p[0])
			}
			if len(p) > 1 && p[1] != "" {
				col, _ = strconv.Atoi(p[1])
			}
		}
		t.row, t.col = row-1, col-1
		if t.row < 0 {
			t.row = 0
		}
		if t.col < 0 {
			t.col = 0
		}
		t.ensure(t.row)
	case "K":
		t.ensure(t.row)
		if n == 2 {
			t.lines[t.row] = nil
			t.col = 0
		} else if t.col < len(t.lines[t.row]) {
			t.lines[t.row] = t.lines[t.row][:t.col]
		}
	case "r": // DECSTBM; no params resets
		if params == "" {
			t.regSet = false
		} else {
			p := strings.SplitN(params, ";", 2)
			top, bot := 1, t.height
			if p[0] != "" {
				top, _ = strconv.Atoi(p[0])
			}
			if len(p) > 1 && p[1] != "" {
				bot, _ = strconv.Atoi(p[1])
			}
			t.regTop, t.regBot, t.regSet = top-1, bot-1, true
		}
		// A real terminal homes the cursor on DECSTBM.
		t.row, t.col = t.top, 0
		t.ensure(t.row)
	case "S":
		t.scrollRegion(max(n, 1))
	case "T":
		t.scrollRegion(-max(n, 1))
	case "J":
		switch n {
		case 2, 3:
			t.lines = nil
			t.row, t.col, t.top = 0, 0, 0
		default: // 0J: scrollback above is kept
			t.ensure(t.row)
			if t.col < len(t.lines[t.row]) {
				t.lines[t.row] = t.lines[t.row][:t.col]
			}
			t.lines = t.lines[:t.row+1]
		}
	}
}

func (t *FakeTerminal) region() (int, int) {
	lo, hi := t.top, t.top+t.height-1
	if t.height <= 0 {
		hi = lo
	}
	if t.regSet {
		lo, hi = t.top+t.regTop, t.top+t.regBot
	}
	if lo < 0 {
		lo = 0
	}
	return lo, hi
}

// scrollRegion moves the scroll region's content by n rows (n>0 up, n<0
// down), blanking the rows that roll in.
func (t *FakeTerminal) scrollRegion(n int) {
	lo, hi := t.region()
	if hi <= lo {
		return
	}
	t.ensure(hi)
	height := hi - lo + 1
	if n >= height || -n >= height {
		for r := lo; r <= hi; r++ {
			t.lines[r] = nil
		}
		return
	}
	if n > 0 {
		copy(t.lines[lo:hi+1-n], t.lines[lo+n:hi+1])
		for r := hi + 1 - n; r <= hi; r++ {
			t.lines[r] = nil
		}
	} else {
		copy(t.lines[lo-n:hi+1], t.lines[lo:hi+1+n])
		for r := lo; r < lo-n; r++ {
			t.lines[r] = nil
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

// Row is the cursor row, absolute and untrimmed.
func (t *FakeTerminal) Row() int { return t.row }

// Screen is the full transcript, scrollback included, trailing blanks trimmed.
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
