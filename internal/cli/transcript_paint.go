package cli

import "strings"

// ---------------------------------------------------------------------------
// Paint-layer byte compaction.
//
// The node renderers compose rows out of styled segments (reflow's padding
// writer restyles every padding cell), so a rendered row is mostly SGR churn:
// a full-width blank row arrives as 96 repetitions of "\x1b[38;5;252m \x1b[0m"
// — 1539 bytes to paint nothing. Over a pipe or an ssh link that is the
// dominant cost of a frame, and it is invisible in a benchmark that writes to
// io.Discard.
//
// compactRow is a pure, renderer-agnostic rewrite of one row into the shortest
// byte sequence producing the same cells. It is deliberately at the paint
// layer rather than in the renderers: it fixes every producer at once, it
// cannot change what the row *means*, and it keeps t.prev holding the original
// rows so the frame diff is unaffected.
//
// Three transforms, each provably appearance-preserving:
//
//  1. Lazy SGR: an escape that is never followed by text it can affect is
//     dropped. Style state is tracked and emitted only just before text that
//     needs it.
//  2. Invisible-space styling: a run of spaces under a style with default
//     background, no reverse, no underline and no strikethrough is
//     indistinguishable from an unstyled run of spaces, so no style change is
//     emitted for it.
//  3. Trailing-blank trim: such spaces at end of row are dropped entirely —
//     paint has already cleared the row (or covered it), so they paint blank
//     on blank.
//
// Invariant established for callers: compactRow's output starts assuming
// default SGR and always leaves the terminal in default SGR. paint relies on
// that both ways (an erase-line inherits the current background, and a row
// that opens with no escape must not inherit the previous row's colour).
// Rows are already required to be self-contained — a diffing painter may skip
// the row above entirely — so this only removes a latent hazard.
// ---------------------------------------------------------------------------

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
// visible — the conservative direction. Allocation-free: it runs per escape
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
	if !strings.ContainsRune(row, '\x1b') {
		return append(dst, trimTrailingSpaces(row)...)
	}
	start := len(dst)
	visible := len(dst) // truncation point: everything after is blank-on-blank
	var pending, emitted sgrStyle
	for i := 0; i < len(row); {
		if row[i] == '\x1b' {
			j := skipANSI(row, i)
			params, ok := sgrParams(row[i:j])
			if !ok {
				return verbatimRow(dst[:start], row)
			}
			pending.apply(params)
			if pending.over {
				return verbatimRow(dst[:start], row)
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

// verbatimRow is the bail-out: emit the row untouched, then force the default
// state so the next row starts clean.
func verbatimRow(dst []byte, row string) []byte {
	dst = append(dst, row...)
	if !strings.HasSuffix(row, "\x1b[0m") && !strings.HasSuffix(row, "\x1b[m") {
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
