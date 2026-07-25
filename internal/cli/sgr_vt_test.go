package cli

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/mattn/go-runewidth"
)

// A terminal model, for proving that two byte strings render the same.
//
// This exists because a string comparison cannot check an SGR normalizer: the
// whole point of collapseSGR is that the bytes differ. What must not differ is
// what the terminal ends up showing, so the only honest oracle is a terminal:
// replay both strings and compare the resulting cell grids, character *and*
// rendition, plus the cursor, plus the rendition at the moment of every erase
// (an erase paints with the current background, so an SGR bug that a static
// grid would miss shows up there), plus the final rendition (the painter's
// invariant that a row leaves the terminal in default SGR).
//
// It is deliberately an INDEPENDENT implementation of SGR from sgr.go's
// sgrState: parameters are parsed with strings.Split/strconv and the rendition
// is a canonical string, not a packed struct. A shared parser would cancel out
// exactly the bugs this is here to catch.
//
// ldrender.FakeTerminal cannot serve: it strips SGR, so it is blind to this
// entire class of bug. Axis B built a `vtScreen` in this package's tests for
// the same reason; the names here are distinct (sgrTerm) so the two can coexist
// through the merge and be unified deliberately afterwards.

type sgrRendition struct {
	fg, bg string   // SGR colour parameters, e.g. "31", "38;5;252"; "" = default
	on     []string // set of active attributes, incl. "?N" for params this model does not implement
}

func (r sgrRendition) key() string {
	on := append([]string(nil), r.on...)
	sort.Strings(on)
	return "fg=" + r.fg + " bg=" + r.bg + " [" + strings.Join(on, ",") + "]"
}

func (r sgrRendition) with(attr string, on bool) sgrRendition {
	out := sgrRendition{fg: r.fg, bg: r.bg}
	for _, a := range r.on {
		if a != attr {
			out.on = append(out.on, a)
		}
	}
	if on {
		out.on = append(out.on, attr)
	}
	return out
}

// eraseRendition is what an erase (EL/ED/ECH) leaves in a cell: a blank
// carrying the current background and nothing else.
func (r sgrRendition) eraseRendition() sgrRendition {
	return sgrRendition{bg: r.bg}
}

type sgrCell struct {
	ch   rune
	rend string
}

type sgrTerm struct {
	w, h  int
	cells map[[2]int]sgrCell
	x, y  int
	rend  sgrRendition
	// events records everything that is not a plain cell write, in order: the
	// escape sequences this model does not implement (so a normalizer cannot
	// silently reorder or eat one) and every erase with the rendition in force
	// when it happened.
	events []string
}

func newSGRTerm(w, h int) *sgrTerm {
	return &sgrTerm{w: w, h: h, cells: map[[2]int]sgrCell{}}
}

// sgrRender replays s on a grid of the given size. Both sides of a comparison
// must use the SAME size: an erase runs to the right edge, so sizing the grid
// from the input length would make two strings of different length
// incomparable — and quietly hide a difference.
func sgrRender(s string, w, h int) *sgrTerm {
	t := newSGRTerm(w, h)
	t.write(s)
	return t
}

// sgrGridFor picks one grid big enough for all of the given strings.
func sgrGridFor(ss ...string) (w, h int) {
	w, h = 8, 8
	for _, s := range ss {
		w = max(w, len(s)+8)
		h = max(h, 8+strings.Count(s, "\n"))
	}
	return min(w, 512), min(h, 64)
}

func (t *sgrTerm) write(s string) {
	for i := 0; i < len(s); {
		c := s[i]
		switch {
		case c == 0x1b:
			i = t.escape(s, i)
		case c == '\n':
			t.y++
			if t.y >= t.h {
				t.y = t.h - 1
			}
			i++
		case c == '\r':
			t.x = 0
			i++
		case c == '\b':
			if t.x > 0 {
				t.x--
			}
			i++
		case c == '\t':
			t.x = (t.x/8 + 1) * 8
			if t.x >= t.w {
				t.x = t.w - 1
			}
			i++
		case c < 0x20 || c == 0x7f:
			t.events = append(t.events, fmt.Sprintf("ctrl(%d)@%d,%d", c, t.y, t.x))
			i++
		default:
			r, size := utf8.DecodeRuneInString(s[i:])
			t.put(r)
			i += size
		}
	}
}

func (t *sgrTerm) put(r rune) {
	key := t.rend.key()
	width := runewidth.RuneWidth(r)
	if width < 1 {
		width = 1 // combining marks: model them as their own cell, consistently
	}
	if t.x < t.w {
		t.cells[[2]int{t.y, t.x}] = sgrCell{ch: r, rend: key}
		for k := 1; k < width && t.x+k < t.w; k++ {
			t.cells[[2]int{t.y, t.x + k}] = sgrCell{ch: -1, rend: key}
		}
	}
	t.x += width
	if t.x > t.w { // autowrap is off in the pager: the cursor sticks at the edge
		t.x = t.w
	}
}

// escape consumes one escape sequence at s[i] and applies whatever of it the
// model implements.
func (t *sgrTerm) escape(s string, i int) int {
	if i+1 >= len(s) {
		t.events = append(t.events, fmt.Sprintf("ESC@eof:%d,%d", t.y, t.x))
		return len(s)
	}
	if s[i+1] != '[' {
		t.events = append(t.events, fmt.Sprintf("esc:%s@%d,%d", strconv.Quote(s[i+1:i+2]), t.y, t.x))
		return i + 2
	}
	j := i + 2
	for j < len(s) && s[j] >= 0x30 && s[j] <= 0x3f {
		j++
	}
	inter := j
	for j < len(s) && s[j] >= 0x20 && s[j] <= 0x2f {
		j++
	}
	if j >= len(s) {
		t.events = append(t.events, fmt.Sprintf("csi:unterminated:%s@%d,%d", strconv.Quote(s[i:]), t.y, t.x))
		return len(s)
	}
	params, final := s[i+2:inter], s[j]
	j++
	clean := inter == j-1 && !strings.ContainsAny(params, ":<=>?")
	if !clean {
		t.events = append(t.events, fmt.Sprintf("csi:%s@%d,%d", strconv.Quote(s[i:j]), t.y, t.x))
		return j
	}
	switch final {
	case 'm':
		t.sgr(params)
	case 'K':
		t.eraseLine(sgrFirstParam(params))
	case 'J':
		t.eraseDisplay(sgrFirstParam(params))
	case 'X':
		n := max(sgrFirstParam(params), 1)
		t.events = append(t.events, fmt.Sprintf("ECH(%d)@%d,%d rend=%s", n, t.y, t.x, t.rend.key()))
		for k := 0; k < n && t.x+k < t.w; k++ {
			t.cells[[2]int{t.y, t.x + k}] = sgrCell{ch: ' ', rend: t.rend.eraseRendition().key()}
		}
	case 'H', 'f':
		row, col := 1, 1
		ps := strings.Split(params, ";")
		if len(ps) > 0 {
			row = max(atoiOr(ps[0], 1), 1)
		}
		if len(ps) > 1 {
			col = max(atoiOr(ps[1], 1), 1)
		}
		t.y, t.x = min(row-1, t.h-1), min(col-1, t.w-1)
	case 'A':
		t.y = max(t.y-max(sgrFirstParam(params), 1), 0)
	case 'B':
		t.y = min(t.y+max(sgrFirstParam(params), 1), t.h-1)
	case 'C':
		t.x = min(t.x+max(sgrFirstParam(params), 1), t.w-1)
	case 'D':
		t.x = max(t.x-max(sgrFirstParam(params), 1), 0)
	case 'G':
		t.x = min(max(sgrFirstParam(params), 1)-1, t.w-1)
	default:
		t.events = append(t.events, fmt.Sprintf("csi:%s@%d,%d", strconv.Quote(s[i:j]), t.y, t.x))
	}
	return j
}

func (t *sgrTerm) eraseLine(mode int) {
	t.events = append(t.events, fmt.Sprintf("EL(%d)@%d,%d rend=%s", mode, t.y, t.x, t.rend.key()))
	lo, hi := t.x, t.w
	switch mode {
	case 1:
		lo, hi = 0, t.x+1
	case 2:
		lo, hi = 0, t.w
	}
	blank := sgrCell{ch: ' ', rend: t.rend.eraseRendition().key()}
	for x := lo; x < hi; x++ {
		t.cells[[2]int{t.y, x}] = blank
	}
}

func (t *sgrTerm) eraseDisplay(mode int) {
	t.events = append(t.events, fmt.Sprintf("ED(%d)@%d,%d rend=%s", mode, t.y, t.x, t.rend.key()))
	blank := sgrCell{ch: ' ', rend: t.rend.eraseRendition().key()}
	for y := 0; y < t.h; y++ {
		for x := 0; x < t.w; x++ {
			after := y > t.y || (y == t.y && x >= t.x)
			switch mode {
			case 0:
				if !after {
					continue
				}
			case 1:
				if after {
					continue
				}
			}
			t.cells[[2]int{y, x}] = blank
		}
	}
}

// sgr applies one SGR parameter string. Written from ECMA-48 directly rather
// than shared with sgr.go, on purpose.
func (t *sgrTerm) sgr(params string) {
	if params == "" {
		t.rend = sgrRendition{}
		return
	}
	ps := strings.Split(params, ";")
	for i := 0; i < len(ps); i++ {
		p := atoiOr(ps[i], 0) // an omitted parameter is 0 (ECMA-48 default)
		switch {
		case p == 0:
			t.rend = sgrRendition{}
		case p == 1:
			t.rend = t.rend.with("bold", true)
		case p == 2:
			t.rend = t.rend.with("faint", true)
		case p == 3:
			t.rend = t.rend.with("italic", true)
		case p == 4:
			t.rend = t.rend.with("underline", true)
		case p == 5:
			t.rend = t.rend.with("blink", true)
		case p == 7:
			t.rend = t.rend.with("reverse", true)
		case p == 8:
			t.rend = t.rend.with("conceal", true)
		case p == 9:
			t.rend = t.rend.with("strike", true)
		case p == 22:
			t.rend = t.rend.with("bold", false).with("faint", false)
		case p == 23:
			t.rend = t.rend.with("italic", false)
		case p == 24:
			t.rend = t.rend.with("underline", false)
		case p == 25:
			t.rend = t.rend.with("blink", false)
		case p == 27:
			t.rend = t.rend.with("reverse", false)
		case p == 28:
			t.rend = t.rend.with("conceal", false)
		case p == 29:
			t.rend = t.rend.with("strike", false)
		case (p >= 30 && p <= 37) || (p >= 90 && p <= 97):
			t.rend.fg = strconv.Itoa(p)
		case p == 39:
			t.rend.fg = ""
		case (p >= 40 && p <= 47) || (p >= 100 && p <= 107):
			t.rend.bg = strconv.Itoa(p)
		case p == 49:
			t.rend.bg = ""
		case p == 38 || p == 48:
			col, used, ok := sgrTermExtended(ps[i+1:])
			if !ok {
				// Unparseable extended colour: record the rest verbatim, the
				// same way a terminal would be left confused.
				t.rend = t.rend.with("?"+strings.Join(ps[i:], ";"), true)
				return
			}
			if p == 38 {
				t.rend.fg = col
			} else {
				t.rend.bg = col
			}
			i += used
		default:
			t.rend = t.rend.with("?"+ps[i], true)
		}
	}
}

// sgrTermExtended reads a 38/48 colour selector's sub-parameters, returning the
// canonical colour string and how many parameters it consumed.
func sgrTermExtended(rest []string) (string, int, bool) {
	if len(rest) == 0 {
		return "", 0, false
	}
	switch atoiOr(rest[0], -1) {
	case 5:
		if len(rest) < 2 {
			return "", 0, false
		}
		n := atoiOr(rest[1], -1)
		if n < 0 || n > 255 {
			return "", 0, false
		}
		return "5;" + strconv.Itoa(n), 2, true
	case 2:
		if len(rest) < 4 {
			return "", 0, false
		}
		var out []string
		for _, p := range rest[1:4] {
			n := atoiOr(p, -1)
			if n < 0 || n > 255 {
				return "", 0, false
			}
			out = append(out, strconv.Itoa(n))
		}
		return "2;" + strings.Join(out, ";"), 4, true
	}
	return "", 0, false
}

func sgrFirstParam(params string) int {
	if params == "" {
		return 0
	}
	return atoiOr(strings.Split(params, ";")[0], 0)
}

func atoiOr(s string, def int) int {
	if s == "" {
		return def
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return def
	}
	return n
}

// diff describes the first difference between two replays, or "" if they are
// indistinguishable: same characters, same rendition per cell, same cursor,
// same final rendition, same event log.
func (t *sgrTerm) diff(o *sgrTerm) string {
	for y := 0; y < max(t.h, o.h); y++ {
		for x := 0; x < max(t.w, o.w); x++ {
			a, b := t.cells[[2]int{y, x}], o.cells[[2]int{y, x}]
			if a != b {
				return fmt.Sprintf("cell %d,%d: %q/%s vs %q/%s", y, x, a.ch, a.rend, b.ch, b.rend)
			}
		}
	}
	if t.x != o.x || t.y != o.y {
		return fmt.Sprintf("cursor %d,%d vs %d,%d", t.y, t.x, o.y, o.x)
	}
	if t.rend.key() != o.rend.key() {
		return fmt.Sprintf("final rendition %s vs %s", t.rend.key(), o.rend.key())
	}
	if len(t.events) != len(o.events) {
		return fmt.Sprintf("events %v vs %v", t.events, o.events)
	}
	for i := range t.events {
		if t.events[i] != o.events[i] {
			return fmt.Sprintf("event %d: %s vs %s", i, t.events[i], o.events[i])
		}
	}
	return ""
}

// sgrCellDiff replays a and b on a shared grid and reports the first
// difference, or "" if a terminal cannot tell them apart.
func sgrCellDiff(a, b string) string {
	w, h := sgrGridFor(a, b)
	return sgrRender(a, w, h).diff(sgrRender(b, w, h))
}

// sgrFinalRendition is the rendition a string leaves the terminal in — the
// painter's "every row ends in default SGR" invariant, observable.
func sgrFinalRendition(s string) string {
	w, h := sgrGridFor(s)
	return sgrRender(s, w, h).rend.key()
}

// TestSGRTermHasTeeth checks the oracle before trusting it: a model that
// cannot tell a tinted cell from a plain one would pass every collapse test.
func TestSGRTermHasTeeth(t *testing.T) {
	sensitive := []struct{ name, a, b string }{
		{"colour", "\x1b[31mx", "x"},
		{"256 vs basic", "\x1b[38;5;1mx", "\x1b[31mx"},
		{"attribute", "\x1b[1mx", "x"},
		{"dropped reset tints the tail", "\x1b[31ma\x1b[0mb", "\x1b[31mab"},
		{"erase background", "\x1b[41m\x1b[K", "\x1b[K"},
		{"erase sees state at the erase", "\x1b[41m\x1b[K\x1b[0m", "\x1b[41m\x1b[0m\x1b[K"},
		{"opaque escape order", "\x1b[0m\x1b[?25hx", "\x1b[0mx\x1b[?25h"},
		{"final rendition", "x\x1b[31m", "x"},
		{"truecolour components", "\x1b[38;2;1;2;3mx", "\x1b[38;2;1;2;4mx"},
	}
	for _, c := range sensitive {
		if d := sgrCellDiff(c.a, c.b); d == "" {
			t.Errorf("%s: model cannot distinguish %q from %q", c.name, c.a, c.b)
		}
	}
	// …and it must not cry wolf on genuinely equivalent byte strings.
	equivalent := []struct{ name, a, b string }{
		{"redundant reset", "\x1b[0m\x1b[0mx", "\x1b[0mx"},
		{"reset spelling", "\x1b[0mx", "\x1b[mx"},
		{"round trip", "\x1b[31ma\x1b[0m\x1b[31mb", "\x1b[31mab"},
		{"leading zeros", "\x1b[00031mx", "\x1b[31mx"},
	}
	for _, c := range equivalent {
		if d := sgrCellDiff(c.a, c.b); d != "" {
			t.Errorf("%s: model reports %q != %q: %s", c.name, c.a, c.b, d)
		}
	}
}
