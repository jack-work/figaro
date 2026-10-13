package cli

import (
	"math"
	"strings"
	"unicode/utf8"
)

type sgrColorKind uint8

const (
	colorDefault sgrColorKind = iota // 39 / 49
	colorBasic                       // 30-37, 90-97 (and bg equivalents)
	color256                         // 38;5;N
	colorRGB                         // 38;2;R;G;B
)

type sgrColor struct {
	kind    sgrColorKind
	a, b, c uint8
}

// Anything else (double-underline, overline, underline colour, …) goes opaque,
// which disables collapsing.
const (
	attrBold uint16 = 1 << iota
	attrFaint
	attrItalic
	attrUnderline
	attrBlink
	attrReverse
	attrConceal
	attrStrike
)

// The zero value is unknown, not default: a row never assumes what the row above
// left behind, which is what keeps a leading ESC[0m from being collapsed away.
type sgrState struct {
	attrs   uint16 // attribute values, meaningful only where attrsKn says so
	attrsKn uint16 // which attribute bits are known
	fg, bg  sgrColor
	fgKn    bool
	bgKn    bool
	// opaque is a distinct token per unmodelled effect, so an opaque state
	// equals nothing (itself included) and every drop decision fails closed. A
	// full reset clears it.
	opaque uint32
}

const attrAllKnown = attrBold | attrFaint | attrItalic | attrUnderline |
	attrBlink | attrReverse | attrConceal | attrStrike

func sgrDefault() sgrState {
	return sgrState{attrsKn: attrAllKnown, fgKn: true, bgKn: true}
}

// Fields unknown in both are equal: one state is always derived from the other. An
// opaque state equals nothing.
func (a sgrState) equal(b sgrState) bool {
	if a.opaque != 0 || b.opaque != 0 {
		return false
	}
	if a.attrsKn != b.attrsKn || a.attrs&a.attrsKn != b.attrs&b.attrsKn {
		return false
	}
	if a.fgKn != b.fgKn || (a.fgKn && a.fg != b.fg) {
		return false
	}
	return a.bgKn == b.bgKn && (!a.bgKn || a.bg == b.bg)
}

type escKind uint8

const (
	escSGR     escKind = iota // CSI <digits and semicolons> m, a rendition change
	escNeutral                // a CSI final that provably cannot change the rendition
	escOpaque                 // anything else: pass through, then forget the state
)

// CSI finals that move or erase but never alter the rendition. Still barriers:
// ED/EL/ECH fill with the current background.
const csiRenditionNeutral = "ABCDEFGHJKLMPSTX`ade"

// classifyEsc returns the index just past the sequence at s[i] (an ESC) and what
// the collapser may assume about it.
func classifyEsc(s string, i int) (int, escKind) {
	if i+1 >= len(s) {
		return len(s), escOpaque
	}
	if s[i+1] != '[' {
		// Non-CSI (OSC, DCS, …): consume the introducer; the state goes
		// unknown, so nothing is dropped past it.
		return i + 2, escOpaque
	}
	j := i + 2
	private := false
	for j < len(s) && s[j] >= 0x30 && s[j] <= 0x3f {
		// ':' (sub-parameters) and '<' '=' '>' '?' (private) are not
		// reasoned about.
		if s[j] == ':' || s[j] >= '<' {
			private = true
		}
		j++
	}
	intermediate := false
	for j < len(s) && s[j] >= 0x20 && s[j] <= 0x2f {
		intermediate = true
		j++
	}
	if j >= len(s) { // unterminated: hand back the tail untouched
		return len(s), escOpaque
	}
	final := s[j]
	j++
	if private || intermediate {
		return j, escOpaque
	}
	if final == 'm' {
		return j, escSGR
	}
	if strings.IndexByte(csiRenditionNeutral, final) >= 0 {
		return j, escNeutral
	}
	return j, escOpaque
}

// An empty parameter is 0 (ECMA-48); ok is false for anything but a plain decimal.
func sgrParam(params string, i int) (val int, next int, ok bool) {
	digits := 0
	for i < len(params) {
		c := params[i]
		if c == ';' {
			return val, i + 1, true
		}
		if c < '0' || c > '9' {
			return 0, len(params), false
		}
		if digits == 4 { // absurd parameter: don't pretend to understand it
			return 0, len(params), false
		}
		val = val*10 + int(c-'0')
		digits++
		i++
	}
	return val, i, true
}

// absolute is true once a reset parameter has been seen: the result no longer
// depends on st.
func applySGRParams(st sgrState, params string, tok *uint32) (out sgrState, absolute bool) {
	if params == "" { // ESC[m == ESC[0m
		return sgrDefault(), true
	}
	blur := func() (sgrState, bool) {
		*tok++
		st.opaque = *tok
		return st, absolute
	}
	set := func(bit uint16, on bool) {
		st.attrsKn |= bit
		if on {
			st.attrs |= bit
		} else {
			st.attrs &^= bit
		}
	}
	// A trailing ';' is one more empty parameter, i.e. a reset: ESC[38;5;252;m
	// ends in the default rendition.
	for i, pending := 0, true; pending; {
		p, next, ok := sgrParam(params, i)
		if !ok {
			return blur()
		}
		switch {
		case p == 0:
			st, absolute = sgrDefault(), true
		case p == 1:
			set(attrBold, true)
		case p == 2:
			set(attrFaint, true)
		case p == 3:
			set(attrItalic, true)
		case p == 4:
			set(attrUnderline, true)
		case p == 5:
			set(attrBlink, true)
		case p == 7:
			set(attrReverse, true)
		case p == 8:
			set(attrConceal, true)
		case p == 9:
			set(attrStrike, true)
		case p == 22:
			set(attrBold, false)
			set(attrFaint, false)
		case p == 23:
			set(attrItalic, false)
		case p == 24:
			set(attrUnderline, false)
		case p == 25:
			set(attrBlink, false)
		case p == 27:
			set(attrReverse, false)
		case p == 28:
			set(attrConceal, false)
		case p == 29:
			set(attrStrike, false)
		case p >= 30 && p <= 37, p >= 90 && p <= 97:
			st.fg, st.fgKn = sgrColor{kind: colorBasic, a: uint8(p)}, true
		case p == 39:
			st.fg, st.fgKn = sgrColor{}, true
		case p >= 40 && p <= 47, p >= 100 && p <= 107:
			st.bg, st.bgKn = sgrColor{kind: colorBasic, a: uint8(p)}, true
		case p == 49:
			st.bg, st.bgKn = sgrColor{}, true
		case p == 38 || p == 48:
			col, cn, ok := extendedColor(params, next)
			if !ok {
				return blur()
			}
			next = cn
			if p == 38 {
				st.fg, st.fgKn = col, true
			} else {
				st.bg, st.bgKn = col, true
			}
		default:
			// Real attributes this model does not track: go opaque,
			// not no-op.
			return blur()
		}
		pending = next > i && params[next-1] == ';'
		i = next
	}
	return st, absolute
}

// Colon-delimited variants never reach here: classifyEsc treats ':' as private.
func extendedColor(params string, i int) (sgrColor, int, bool) {
	// A missing or empty sub-parameter (ESC[48;5m, ESC[38;5;;m) is read
	// differently by real terminals, so it is refused.
	read := func() (int, bool) {
		if i >= len(params) || params[i] < '0' || params[i] > '9' {
			return 0, false
		}
		v, n, ok := sgrParam(params, i)
		if !ok {
			return 0, false
		}
		i = n
		return v, true
	}
	mode, ok := read()
	if !ok {
		return sgrColor{}, i, false
	}
	readByte := func() (uint8, bool) {
		v, ok := read()
		if !ok || v > 255 {
			return 0, false
		}
		return uint8(v), true
	}
	switch mode {
	case 5:
		n, ok := readByte()
		if !ok {
			return sgrColor{}, i, false
		}
		return sgrColor{kind: color256, a: n}, i, true
	case 2:
		r, ok1 := readByte()
		g, ok2 := readByte()
		b, ok3 := readByte()
		if !ok1 || !ok2 || !ok3 {
			return sgrColor{}, i, false
		}
		return sgrColor{kind: colorRGB, a: r, b: g, c: b}, i, true
	}
	return sgrColor{}, i, false
}

// Offsets are int32 to keep the per-sequence copy small; collapseSGR bails out on
// longer rows.
type sgrSeq struct {
	start, end int32
	st         sgrState
	absolute   bool
}

// Indirected so the golden regenerator can produce pre-collapse companion frames.
var sgrCollapse = collapseSGR

// The result is byte-different but cell-identical (sgr_vt_test.go). Rows with
// nothing to drop are returned as is.
func collapseSGR(s string) string {
	if strings.IndexByte(s, 0x1b) < 0 {
		return s
	}
	if len(s) > math.MaxInt32 { // sgrSeq stores offsets as int32
		return s
	}
	if !utf8.ValidString(s) {
		// Dropping an ASCII escape from invalid UTF-8 can splice stray
		// bytes into a different glyph; rows reaching the cache are valid
		// by construction.
		return s
	}
	var c sgrCollapser
	c.s = s
	// The entry state is all-unknown, not default.
	for i := 0; i < len(s); {
		if s[i] != 0x1b {
			c.flush()
			next := strings.IndexByte(s[i:], 0x1b) // SIMD beats a byte loop here
			if next < 0 {
				break
			}
			i += next
			continue
		}
		end, kind := classifyEsc(s, i)
		switch kind {
		case escSGR:
			st, absolute := applySGRParams(c.pend, s[i+2:end-1], &c.tok)
			c.pend = st
			c.push(sgrSeq{start: int32(i), end: int32(end), st: st, absolute: absolute})
		case escNeutral:
			// Erase consumes the rendition (ESC[K fills with the
			// current background), so the pending state is realized
			// first.
			c.flush()
		default:
			c.flush()
			c.tok++
			c.emitted = sgrState{opaque: c.tok}
			c.pend = c.emitted
		}
		i = end
	}
	c.flush()
	if !c.dropped {
		return s
	}
	return string(append(c.out, s[c.copied:]...))
}

type sgrCollapser struct {
	s       string
	out     []byte   // kept bytes so far; nil until the first drop
	copied  int      // s[:copied] has been decided (kept in out, or dropped)
	dropped bool     // …because "out is still empty" is not the same question
	emitted sgrState // the rendition the bytes kept so far leave the terminal in
	pend    sgrState // …after also applying the buffered run
	// A fixed array, indexed: a slice into its own struct would force the
	// collapser onto the heap.
	run  [24]sgrSeq
	runN int
	tok  uint32
}

// A full buffer is decided early: flush is valid at any point, a barrier only
// makes it more effective.
func (c *sgrCollapser) push(seq sgrSeq) {
	if c.runN == len(c.run) {
		c.flush()
	}
	c.run[c.runN] = seq
	c.runN++
}

func (c *sgrCollapser) drop(k int) {
	if c.out == nil {
		// The result is never longer than the input, so one allocation
		// covers the row.
		c.out = make([]byte, 0, len(c.s))
	}
	c.out = append(c.out, c.s[c.copied:int(c.run[k].start)]...)
	c.copied = int(c.run[k].end)
	c.dropped = true
}

// Sequences are dropped only when the rendition at the barrier is reached without them.
func (c *sgrCollapser) flush() {
	run := c.run[:c.runN]
	if len(run) == 0 {
		return
	}
	c.runN = 0
	final := run[len(run)-1].st

	// The whole run is a round trip: none of it is needed.
	if c.emitted.equal(final) {
		for k := range run {
			c.drop(k)
		}
		c.pend = c.emitted
		return
	}
	// Everything before the last absolute sequence is dead, and so is any tail
	// sequence that does not move the state.
	start := 0
	for k := len(run) - 1; k >= 0; k-- {
		if run[k].absolute {
			start = k
			break
		}
	}
	for k := 0; k < start; k++ {
		c.drop(k)
	}
	cur := c.emitted
	for k := start; k < len(run); k++ {
		if cur.equal(run[k].st) {
			c.drop(k)
			continue
		}
		cur = run[k].st
	}
	c.emitted, c.pend = final, final
}
