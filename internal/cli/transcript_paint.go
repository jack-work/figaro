package cli

import (
	"os"
	"strings"
	"unicode/utf8"

	"github.com/mattn/go-runewidth"
)

// ---------------------------------------------------------------------------
// Paint-layer byte compaction.

// sgrStyleMax caps how many distinct SGR sequences we track for one row before
// giving up and emitting it verbatim. Real rows use one or two.
const sgrStyleMax = 8

// sgrStyle is the ordered set of SGR sequences in effect, most recently
// applied last. Replaying the list from a reset reproduces the state exactly,
// because SGR parameters are order-dependent only through last-writer-wins and
// apply() keeps each sequence at its last-applied position.
type sgrStyle struct {
	parts [sgrStyleMax]string
	invis [sgrStyleMax]bool // parts[i] cannot show up on a blank cell
	n     int
	over  bool // more than sgrStyleMax distinct sequences: bail out

	// One-entry memo for paramsInvisibleOnSpace. A row is typically a single
	// style flipped on and off around every segment, so the same parameter
	// string is classified dozens of times per row.
	memoParams string
	memoInvis  bool
	memoValid  bool
}

// invisibleOn classifies a parameter string, memoizing the last answer.
func (s *sgrStyle) invisibleOn(params string) bool {
	if s.memoValid && s.memoParams == params {
		return s.memoInvis
	}
	inv := paramsInvisibleOnSpace(params)
	s.memoParams, s.memoInvis, s.memoValid = params, inv, true
	return inv
}

func (s *sgrStyle) reset() { s.n = 0 }

// apply folds one SGR parameter string into the state. "" and "0" are resets;
// a sequence that merely *opens* with a reset ("0;1;31") resets first and is
// then recorded, so replay still reproduces it exactly.
func (s *sgrStyle) apply(params string) {
	if params == "" || params == "0" {
		s.reset()
		return
	}
	if strings.HasPrefix(params, "0;") {
		s.reset()
	}
	for i := 0; i < s.n; i++ {
		if s.parts[i] == params {
			inv := s.invis[i]
			copy(s.parts[i:], s.parts[i+1:s.n])
			copy(s.invis[i:], s.invis[i+1:s.n])
			s.parts[s.n-1], s.invis[s.n-1] = params, inv
			return
		}
	}
	if s.n == sgrStyleMax {
		s.over = true
		return
	}
	s.parts[s.n] = params
	s.invis[s.n] = s.invisibleOn(params)
	s.n++
}

func (s *sgrStyle) equal(o *sgrStyle) bool {
	if s.n != o.n {
		return false
	}
	for i := 0; i < s.n; i++ {
		if s.parts[i] != o.parts[i] {
			return false
		}
	}
	return true
}

// adopt copies the style state (but not the memo) from o.
func (s *sgrStyle) adopt(o *sgrStyle) {
	s.parts, s.invis, s.n = o.parts, o.invis, o.n
}

// spaceInvisible reports whether a run of spaces under this style is
// indistinguishable from unstyled spaces: no background, no reverse video, no
// underline, no strikethrough. Foreground colour and bold/dim/italic have
// nothing to paint on a blank cell.
func (s *sgrStyle) spaceInvisible() bool {
	for i := 0; i < s.n; i++ {
		if !s.invis[i] {
			return false
		}
	}
	return true
}

// paramsInvisibleOnSpace reports whether every parameter in an SGR sequence is
// one that cannot show up on a blank cell. Anything unrecognized is treated as
// visible: the conservative direction. Allocation-free: it runs per escape
// sequence on every painted row.
func paramsInvisibleOnSpace(params string) bool {
	for params != "" {
		var field string
		field, params = cutSGRField(params)
		n, ok := atoiSmall(field)
		if !ok {
			return false
		}
		switch {
		case n == 0, n == 1, n == 2, n == 3, n == 5, n == 6, n == 8:
			// reset / bold / dim / italic / blink / rapid blink / conceal
		case n == 22, n == 23, n == 25, n == 26, n == 28:
			// the matching "off" codes
		case n == 39: // default foreground
		case n >= 30 && n <= 37, n >= 90 && n <= 97: // foreground colour
		case n == 38: // extended foreground: step over its colour arguments
			var sel string
			sel, params = cutSGRField(params)
			skip := 0
			switch sel {
			case "5":
				skip = 1
			case "2":
				skip = 3
			}
			for ; skip > 0 && params != ""; skip-- {
				_, params = cutSGRField(params)
			}
		default:
			// 4/21 underline, 7/27 reverse, 9/29 strike, 40-49 & 100-107 and 48
			// extended background all repaint the cell.
			return false
		}
	}
	return true
}

// cutSGRField splits off the first ';'-separated parameter, reducing a colon
// sub-parameter group (e.g. "4:3" curly underline) to its leading number,
// which is what selects the attribute.
func cutSGRField(params string) (field, rest string) {
	field = params
	if i := strings.IndexByte(params, ';'); i >= 0 {
		field, rest = params[:i], params[i+1:]
	}
	if i := strings.IndexByte(field, ':'); i >= 0 {
		field = field[:i]
	}
	return field, rest
}

func atoiSmall(s string) (int, bool) {
	if s == "" {
		return 0, true
	}
	n := 0
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return 0, false
		}
		n = n*10 + int(s[i]-'0')
		if n > 1<<16 {
			return 0, false
		}
	}
	return n, true
}

// writeTransition appends the shortest escape taking the terminal from `from`
// to `s`. When `from` is a prefix of `s` only the new sequences are needed;
// otherwise a reset comes first.
func (s *sgrStyle) writeTransition(dst []byte, from *sgrStyle) []byte {
	start := 0
	if s.n >= from.n {
		shared := true
		for i := 0; i < from.n; i++ {
			if from.parts[i] != s.parts[i] {
				shared = false
				break
			}
		}
		if shared {
			start = from.n
		}
	}
	if start == 0 && from.n > 0 {
		dst = append(dst, "\x1b[0m"...)
	}
	for i := start; i < s.n; i++ {
		dst = append(dst, "\x1b["...)
		dst = append(dst, s.parts[i]...)
		dst = append(dst, 'm')
	}
	return dst
}

// sgrParams returns the parameter string of an SGR sequence, or ok=false if
// the escape is anything else (a cursor move, an OSC, a private mode…), in
// which case the row is emitted verbatim.
func sgrParams(seq string) (string, bool) {
	if len(seq) < 3 || seq[0] != '\x1b' || seq[1] != '[' || seq[len(seq)-1] != 'm' {
		return "", false
	}
	params := seq[2 : len(seq)-1]
	for i := 0; i < len(params); i++ {
		if c := params[i]; (c < '0' || c > '9') && c != ';' && c != ':' {
			return "", false
		}
	}
	return params, true
}

// compactRow appends the compacted form of row to dst. See the file comment
// for the contract: default SGR in, default SGR out, same visible cells.
func compactRow(dst []byte, row string) []byte {
	return compactRowFrom(dst, row, sgrStyle{})
}

// compactRowFrom is compactRow starting from a row whose leading columns are
// already on screen: `pending` is the style in effect at that point, which has
// to be re-established because SGR is terminal state, not cell state, and the
// escapes that set it were emitted in an earlier frame.
func compactRowFrom(dst []byte, row string, pending sgrStyle) []byte {
	if pending.n == 0 && !strings.ContainsRune(row, '\x1b') {
		return append(dst, trimTrailingSpaces(row)...)
	}
	start := len(dst)
	visible := len(dst) // truncation point: everything after is blank-on-blank
	entry := pending    // style in effect at column 0 of this fragment
	var emitted sgrStyle
	for i := 0; i < len(row); {
		if row[i] == '\x1b' {
			j := skipANSI(row, i)
			params, ok := sgrParams(row[i:j])
			if !ok {
				return verbatimRow(dst[:start], row, &entry)
			}
			pending.apply(params)
			if pending.over {
				return verbatimRow(dst[:start], row, &entry)
			}
			i = j
			continue
		}
		j := i
		for j < len(row) && row[j] != '\x1b' {
			j++
		}
		text := row[i:j]
		i = j
		trailing := len(text) - len(trimTrailingSpaces(text))
		if trailing == len(text) && pending.spaceInvisible() {
			dst = append(dst, text...) // pure blank run under an invisible style
			continue
		}
		if !pending.equal(&emitted) {
			dst = pending.writeTransition(dst, &emitted)
			emitted.adopt(&pending)
		}
		dst = append(dst, text...)
		if emitted.spaceInvisible() {
			visible = len(dst) - trailing
		} else {
			visible = len(dst)
		}
	}
	dst = dst[:visible]
	if emitted.n > 0 {
		dst = append(dst, "\x1b[0m"...)
	}
	return dst
}

// verbatimRow is the bail-out: re-establish the entry style, emit the row
// untouched, then force the default state so the next row starts clean.
func verbatimRow(dst []byte, row string, entry *sgrStyle) []byte {
	if entry.n > 0 {
		dst = entry.writeTransition(dst, &sgrStyle{})
	}
	dst = append(dst, row...)
	if entry.n > 0 || (!strings.HasSuffix(row, "\x1b[0m") && !strings.HasSuffix(row, "\x1b[m")) {
		dst = append(dst, "\x1b[0m"...)
	}
	return dst
}

func trimTrailingSpaces(s string) string {
	n := len(s)
	for n > 0 && s[n-1] == ' ' {
		n--
	}
	return s[:n]
}

// ---------------------------------------------------------------------------
// Shifted-frame painting (scroll regions).

const (
	// maxScrollShift bounds the search. Beyond a half page or so, a repaint of
	// the exposed rows costs as much as the whole frame.
	maxScrollShift = 32
	// minScrollRun is the shortest moved run worth a region switch.
	minScrollRun = 4
	// scrollCost is the byte overhead of DECSTBM + SU/SD + margin reset.
	scrollCost = 24
)

type scrollPlan struct {
	n        int // rows the content moved: >0 up (SU), <0 down (SD)
	top, bot int // scroll region, 0-based inclusive
}

// transcriptScrollRegions is the escape hatch. DECSTBM + SU/SD is VT100/VT420
// bedrock and every terminal this pager runs on implements it, but a paint
// path that moves rows the emulator does not move would be visually wrong
// rather than merely slow, so there is a way to switch it off in the field
// without a rebuild: FIGARO_NO_SCROLL_REGION=1.
var transcriptScrollRegions = os.Getenv("FIGARO_NO_SCROLL_REGION") == ""

// rowKey is an O(1) fingerprint of a row: length plus four sampled bytes. It
// only has to be good enough to *propose* a shift: the winning candidate is
// re-verified with real string equality, so a collision costs a wasted compare
// and never a wrong frame.
func rowKey(s string) uint32 {
	n := len(s)
	if n == 0 {
		return 0
	}
	h := uint32(n) * 2654435761
	h ^= uint32(s[0]) * 40503
	h ^= uint32(s[n-1]) * 2246822519
	h ^= uint32(s[n/2]) * 3266489917
	h ^= uint32(s[n/4]) * 668265263
	return h
}

// planScroll looks for a shift between t.prev and screen. On success it fills
// t.predBuf with the grid the terminal will hold after the scroll and reports
// the plan; the caller diffs against t.predBuf instead of t.prev.
func (t *transcript) planScroll(screen []string) (scrollPlan, bool) {
	prev := t.prev
	h := len(screen)
	if !transcriptScrollRegions || h < minScrollRun*2 || len(prev) != h {
		return scrollPlan{}, false
	}
	t.keysNew = growUint32(t.keysNew, h)
	t.keysOld = growUint32(t.keysOld, h)
	changed := 0
	for r := 0; r < h; r++ {
		t.keysNew[r] = rowKey(screen[r])
		t.keysOld[r] = rowKey(prev[r])
		if screen[r] != prev[r] {
			changed++
		}
	}
	if changed < minScrollRun {
		return scrollPlan{}, false // a small local edit; the plain diff wins
	}

	// Propose: for each candidate shift, the longest run of rows whose
	// fingerprints line up.
	bestN, bestLen, bestAt := 0, 0, 0
	limit := min(maxScrollShift, h-1)
	for s := -limit; s <= limit; s++ {
		if s == 0 {
			continue
		}
		run := 0
		lo, hi := max(0, -s), min(h, h-s)
		for r := lo; r < hi; r++ {
			if t.keysNew[r] == t.keysOld[r+s] {
				run++
				if run > bestLen && run > abs(s) {
					bestLen, bestN, bestAt = run, s, r-run+1
				}
			} else {
				run = 0
			}
		}
	}
	if bestLen < minScrollRun {
		return scrollPlan{}, false
	}

	// Verify with real equality, shrinking the run to what actually matches.
	a, b := bestAt, bestAt+bestLen-1
	for a <= b && screen[a] != prev[a+bestN] {
		a++
	}
	for b >= a && screen[b] != prev[b+bestN] {
		b--
	}
	if b-a+1 < minScrollRun {
		return scrollPlan{}, false
	}
	plan := scrollPlan{n: bestN, top: min(a, a+bestN), bot: max(b, b+bestN)}
	if plan.top < 0 || plan.bot >= h || plan.top >= plan.bot {
		return scrollPlan{}, false
	}

	// Predict the post-scroll grid and price the plan against the plain diff.
	t.predBuf = growStrings(t.predBuf, h)
	copy(t.predBuf, prev)
	for r := plan.top; r <= plan.bot; r++ {
		src := r + plan.n
		if src < plan.top || src > plan.bot {
			t.predBuf[r] = "" // rolled in blank
		} else {
			t.predBuf[r] = prev[src]
		}
	}
	saved, extra := 0, 0
	for r := 0; r < h; r++ {
		wasDirty, nowDirty := screen[r] != prev[r], screen[r] != t.predBuf[r]
		switch {
		case wasDirty && !nowDirty:
			saved += len(screen[r]) + 8
		case !wasDirty && nowDirty:
			extra += len(screen[r]) + 8
		}
	}
	if saved <= extra+scrollCost {
		return scrollPlan{}, false
	}
	return plan, true
}

// appendScroll emits DECSTBM + SU/SD + margin reset for the plan.
func appendScroll(dst []byte, p scrollPlan) []byte {
	dst = append(dst, '\x1b', '[')
	dst = appendUint(dst, p.top+1)
	dst = append(dst, ';')
	dst = appendUint(dst, p.bot+1)
	dst = append(dst, 'r', '\x1b', '[')
	if p.n > 0 {
		dst = appendUint(dst, p.n)
		dst = append(dst, 'S')
	} else {
		dst = appendUint(dst, -p.n)
		dst = append(dst, 'T')
	}
	return append(dst, '\x1b', '[', 'r')
}

func abs(n int) int {
	if n < 0 {
		return -n
	}
	return n
}

func growUint32(s []uint32, n int) []uint32 {
	if cap(s) < n {
		return make([]uint32, n)
	}
	return s[:n]
}

func growStrings(s []string, n int) []string {
	if cap(s) < n {
		return make([]string, n)
	}
	return s[:n]
}

// ---------------------------------------------------------------------------
// Shared-prefix row updates.

// minPrefixColumns is the shortest shared prefix worth a cursor address. Below
// this the escape costs about as much as the text it skips.
const minPrefixColumns = 32

// commonRowPrefix reports the byte index at which old and new diverge, the
// display column there, and the style in effect. ok is false when the prefix
// cannot be trusted (see the guards above) or is too short to pay.
func commonRowPrefix(old, new string) (idx, col int, st sgrStyle, ok bool) {
	i := 0
	for i < len(old) && i < len(new) {
		if new[i] == '\x1b' {
			j := skipANSI(new, i)
			if j > len(old) || old[i:j] != new[i:j] {
				break
			}
			params, isSGR := sgrParams(new[i:j])
			if !isSGR {
				return 0, 0, st, false
			}
			st.apply(params)
			if st.over {
				return 0, 0, st, false
			}
			i = j
			continue
		}
		r, size := utf8.DecodeRuneInString(new[i:])
		if size == 0 || (r == utf8.RuneError && size == 1) {
			return 0, 0, st, false
		}
		if i+size > len(old) || old[i:i+size] != new[i:i+size] {
			break
		}
		if runewidth.RuneWidth(r) != 1 {
			return 0, 0, st, false // not confidently one column
		}
		col++
		i += size
	}
	if col < minPrefixColumns || (i == len(new) && i == len(old)) {
		return 0, 0, st, false // too short to pay for a cursor address, or identical
	}
	return i, col, st, true
}

// appendRowUpdate emits the update for one row: a cursor address plus the tail
// when the row shares a long prefix with what is on screen, else the whole row
// after an erase.
func appendRowUpdate(dst []byte, screenRow int, old, row string) []byte {
	if idx, col, st, ok := commonRowPrefix(old, row); ok {
		dst = appendCUPCol(dst, screenRow+1, col+1)
		// Erase BEFORE writing the tail, not after. The pager runs with autowrap
		// off, so writing the last column leaves the cursor ON it, a trailing
		// erase-to-end-of-line would wipe the character just written. (Found by
		// replaying a frame into tmux, which a screen model that let the cursor
		// run past the margin had happily accepted.) Safe unstyled: every row
		// leaves the default background, and the whole frame is inside a
		// synchronized update, so erase-then-write cannot flicker.
		dst = append(dst, "\x1b[K"...)
		return compactRowFrom(dst, row[idx:], st)
	}
	dst = appendCUP(dst, screenRow+1)
	dst = append(dst, "\x1b[2K"...)
	return compactRow(dst, row)
}

// appendCUPCol appends "\x1b[<row>;<col>H".
func appendCUPCol(dst []byte, row, col int) []byte {
	if col == 1 {
		return appendCUP(dst, row)
	}
	dst = append(dst, '\x1b', '[')
	dst = appendUint(dst, row)
	dst = append(dst, ';')
	dst = appendUint(dst, col)
	return append(dst, 'H')
}
