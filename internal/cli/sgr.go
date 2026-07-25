package cli

import (
	"math"
	"strings"
	"unicode/utf8"
)

// SGR normalization.
//
// glamour's dark style emits its colour per *cell*: a padded prose row comes
// back as `ESC[38;5;252m<space>ESC[0m` repeated forty times, which is 76% ANSI
// by weight. Those sequences are redundant — between two adjacent cells the
// reset/set pair returns the terminal to the state it was already in — so they
// cost retained memory in the row cache and, worse, bytes down the pipe on
// every painted frame (the thing you feel over ssh or inside tmux).
//
// collapseSGR removes them. It is deliberately *subtractive*: it only ever
// drops whole escape sequences, and never synthesizes or reorders one. That is
// what makes "visually identical" a provable property rather than a hope — a
// dropped sequence is one whose effect on the rendition state was nil at the
// point it appeared, so every printable cell is written under exactly the
// rendition it had before, and the state at the end of the row is unchanged.
//
// Two conservative choices are load-bearing:
//
//   - The rendition state on entry is treated as UNKNOWN, not as default. So a
//     row whose leading escape run resolves to "default" still keeps one
//     `ESC[0m`: the row asserts its own starting state instead of trusting the
//     row above it. That upholds the painter's invariant (every row starts and
//     ends in default SGR) for four bytes a row.
//   - Any escape that is not plainly an SGR sequence is opaque: it passes
//     through byte-for-byte, and unless it is one of the few CSI finals that
//     provably cannot touch the rendition (cursor motion, erases), the state
//     afterwards is treated as unknown again, so nothing around it is dropped.

// sgrColorKind distinguishes the four ways SGR can name a colour.
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

// The boolean rendition attributes this model accounts for. Anything else
// (double-underline, overline, proportional spacing, underline colour, …) is
// handled by going opaque, which disables collapsing rather than guessing.
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

// sgrState is a graphic-rendition state, with per-field knowledge: on entry to
// a row nothing is known, and each sequence teaches the model only about the
// fields it sets. The zero value is therefore "unknown", not "default" — a row
// never assumes what the row above it left behind, which is what keeps a
// leading `ESC[0m` from being collapsed away.
type sgrState struct {
	attrs   uint16 // attribute values, meaningful only where attrsKn says so
	attrsKn uint16 // which attribute bits are known
	fg, bg  sgrColor
	fgKn    bool
	bgKn    bool
	// opaque is nonzero when the state contains an effect the model cannot
	// account for: an unrecognized SGR parameter, or an escape that might have
	// changed the rendition in some way this code will not guess at. Each such
	// event gets a distinct token, so an opaque state never compares equal to
	// anything (including itself) and every drop decision around it fails
	// closed. A full reset clears it.
	opaque uint32
}

const attrAllKnown = attrBold | attrFaint | attrItalic | attrUnderline |
	attrBlink | attrReverse | attrConceal | attrStrike

// sgrDefault is the terminal's default rendition, fully known.
func sgrDefault() sgrState {
	return sgrState{attrsKn: attrAllKnown, fgKn: true, bgKn: true}
}

// equal reports whether two states are known to render identically. Fields that
// are unknown in both are equal by construction: one of the two states is
// always derived from the other by applying sequences, so a field neither of
// them touched is literally the same field. An opaque state equals nothing.
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
	escSGR     escKind = iota // CSI <digits and semicolons> m — a rendition change
	escNeutral                // a CSI final that provably cannot change the rendition
	escOpaque                 // anything else: pass through, then forget the state
)

// csiRenditionNeutral lists CSI final bytes that move the cursor or erase but
// never alter the graphic rendition: CUU/CUD/CUF/CUB/CNL/CPL/CHA/CUP, ED, EL,
// IL, DL, DCH, SU, SD, ECH, HPA, HPR, VPA, VPR. They still act as barriers —
// ED/EL/ECH fill with the *current* background — so pending rendition changes
// are always realized before one of them is written.
const csiRenditionNeutral = "ABCDEFGHJKLMPSTX`ade"

// classifyEsc returns the index just past the escape sequence starting at s[i]
// (which must be ESC) and what the collapser may assume about it.
func classifyEsc(s string, i int) (int, escKind) {
	if i+1 >= len(s) {
		return len(s), escOpaque
	}
	if s[i+1] != '[' {
		// Non-CSI: ESC Fe/Fp/Fs, including OSC and DCS whose string payloads
		// this scanner does not parse. Consume the two-byte introducer and
		// treat the rest as ordinary content; the state goes unknown either
		// way, so nothing is dropped on the far side.
		return i + 2, escOpaque
	}
	j := i + 2
	private := false
	for j < len(s) && s[j] >= 0x30 && s[j] <= 0x3f {
		// Digits and ';' are ordinary parameters; ':' (sub-parameters) and
		// '<' '=' '>' '?' (private markers) are not something to reason about.
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

// sgrParam reads one parameter of an SGR parameter string starting at i,
// returning its value and the index of the next parameter. An empty parameter
// is 0 (per ECMA-48). ok is false if the parameter is not a plain decimal
// number the model can trust.
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

// applySGRParams folds an SGR sequence's parameter string (the bytes between
// "\x1b[" and the final 'm') into st. absolute reports whether the resulting
// state is independent of st — true once a reset parameter has been seen —
// which is what lets the collapser drop everything before it.
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
	// The parameter list is walked with an explicit "a separator was consumed,
	// so another parameter follows" flag rather than `i < len(params)`: a
	// trailing ';' means one more (empty, i.e. 0 i.e. RESET) parameter, and
	// `ESC[38;5;252;m` really does end in the default rendition.
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
			// 6, 21, 26, 53, 55, 58, 59, 73…: real attributes this model does
			// not track. Go opaque rather than treat them as no-ops.
			return blur()
		}
		pending = next > i && params[next-1] == ';'
		i = next
	}
	return st, absolute
}

// extendedColor parses the sub-parameters of a 38/48 selector: `5;N` for the
// 256-colour cube or `2;R;G;B` for direct colour. Any other form (including
// the colon-delimited variants, which never reach here because classifyEsc
// treats ':' as private) is refused so the caller can go opaque.
func extendedColor(params string, i int) (sgrColor, int, bool) {
	// Every sub-parameter must be present and spelled with digits. A selector
	// with a missing (`ESC[48;5m`) or empty (`ESC[38;5;;m`) sub-parameter is
	// malformed and real terminals disagree about what it means — one reads the
	// empty field as colour 0 and carries on, another abandons the sequence. So
	// refuse to model it and let the caller go opaque, rather than pick a
	// reading and collapse on the strength of it.
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

// sgrSeq is one buffered SGR sequence: its byte span in the row and the
// rendition state it leaves behind. Offsets are int32 to keep the struct (which
// is copied per sequence) small; collapseSGR bails out on anything longer.
type sgrSeq struct {
	start, end int32
	st         sgrState
	absolute   bool
}

// collapseSGR returns s with every rendition-neutral escape sequence removed.
// The result is byte-different but cell-identical (see FuzzSGRCollapse and the
// VT model in sgr_vt_test.go). Rows that have nothing to drop are returned as
// they came in, with no allocation.
func collapseSGR(s string) string {
	if strings.IndexByte(s, 0x1b) < 0 {
		return s
	}
	if len(s) > math.MaxInt32 { // sgrSeq stores offsets as int32
		return s
	}
	if !utf8.ValidString(s) {
		// Escape sequences are pure ASCII, so removing one from valid UTF-8
		// leaves valid UTF-8 with every rune decoding as before. In INVALID
		// UTF-8 that reasoning collapses: FuzzSGRCollapse found that dropping
		// the redundant reset from "\xc4 ESC[m \x8f" splices two stray bytes
		// into "ď", a different glyph on a real terminal. Rows reaching the row
		// cache are valid by construction (clipToWidth substitutes U+FFFD for
		// anything else), so refuse the input rather than reason about it.
		return s
	}
	var c sgrCollapser
	c.s = s
	// The entry state is all-unknown (the zero value), not default: the row
	// asserts its own rendition rather than trusting the row above it.
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
			// Cursor motion / erase: it cannot change the rendition, but it
			// can *consume* it (ESC[K fills with the current background), so
			// the pending state must be realized before it.
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
	// The buffered run lives in a fixed array, indexed rather than sliced: a
	// slice field pointing into its own struct would force the whole collapser
	// onto the heap, and this runs on every cached row.
	run  [24]sgrSeq
	runN int
	tok  uint32
}

// push buffers one SGR sequence. If the buffer fills (pathological — real rows
// run 2 to 4 sequences between printable characters) the run is decided early:
// flush's reasoning is valid at any point, a barrier only makes it more
// effective, so an over-long run is collapsed in pieces rather than kept whole.
func (c *sgrCollapser) push(seq sgrSeq) {
	if c.runN == len(c.run) {
		c.flush()
	}
	c.run[c.runN] = seq
	c.runN++
}

func (c *sgrCollapser) drop(k int) {
	if c.out == nil {
		// The result can only be shorter than the input, so one allocation of
		// len(s) covers the whole row. Growing from nil instead took seven
		// appends per row — a third of the normalizer's cost.
		c.out = make([]byte, 0, len(c.s))
	}
	c.out = append(c.out, c.s[c.copied:int(c.run[k].start)]...)
	c.copied = int(c.run[k].end)
	c.dropped = true
}

// flush decides the fate of the buffered run of SGR sequences, now that a
// barrier (printable text, an opaque escape, or the end of the row) has been
// reached. Sequences are dropped only when the rendition at the barrier is
// reached without them.
func (c *sgrCollapser) flush() {
	run := c.run[:c.runN]
	if len(run) == 0 {
		return
	}
	c.runN = 0
	final := run[len(run)-1].st

	// Whole run is a round trip (`ESC[0m ESC[38;5;252m` back to the colour
	// already in effect — the glamour per-cell pattern): none of it is needed.
	if c.emitted.equal(final) {
		for k := range run {
			c.drop(k)
		}
		c.pend = c.emitted
		return
	}
	// Otherwise: everything before the last absolute sequence (one containing
	// a reset parameter, so its result does not depend on the prior state) is
	// dead, and within the surviving tail every sequence that does not move
	// the state is dead too.
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
