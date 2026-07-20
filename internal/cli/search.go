package cli

import (
	"regexp"
	"strings"
)

// searchPattern is a compiled transcript search. The engine is the stdlib
// regexp (RE2): linear-time, so a pathological pattern can't hang the paged
// historical search over a 50k-message aria. Vim smartcase: an all-lowercase
// query matches case-insensitively; any uppercase makes it exact. Swap
// compileSearch's engine here if backreferences ever matter (dlclark/regexp2
// in RE2 mode) — nothing else knows the regex type.
type searchPattern struct {
	re     *regexp.Regexp
	src    string // as typed (footer display)
	lit    string // non-"" when the query is metacharacter-free: pruning allowed
	folded bool   // smartcase-insensitive (literal pruning must case-fold too)
}

func compileSearch(q string) (*searchPattern, error) {
	folded := q == strings.ToLower(q)
	expr := q
	if folded {
		expr = "(?i)" + expr
	}
	re, err := regexp.Compile(expr)
	if err != nil {
		return nil, err
	}
	p := &searchPattern{re: re, src: q, folded: folded}
	if regexp.QuoteMeta(q) == q {
		p.lit = q
	}
	return p, nil
}

// probe is the pre-render pruning predicate (messageMayRenderQuery): sound
// only for literal queries — a regex can match across text the raw fields
// don't contain verbatim, so non-literal patterns never prune.
func (p *searchPattern) probe(hay string) bool {
	if p.lit == "" {
		return true
	}
	if p.folded {
		return strings.Contains(strings.ToLower(hay), p.lit)
	}
	return strings.Contains(hay, p.lit)
}

// foldForProbe case-folds a haystack for the markdown-aware pruning helpers,
// which compare byte-wise against the (all-lowercase, when folded) literal.
func (p *searchPattern) foldForProbe(hay string) string {
	if p.folded {
		return strings.ToLower(hay)
	}
	return hay
}

// match reports whether the row's VISIBLE text matches — ANSI escapes are
// invisible to the pattern, so a match can span a styling boundary.
func (p *searchPattern) match(row string) bool {
	if !strings.ContainsRune(row, '\x1b') {
		return p.re.MatchString(row)
	}
	visible, _ := visibleWithMap(row)
	return p.re.MatchString(visible)
}

// highlight wraps every match in the row with reverse video (SGR 7/27 — set
// and unset only the one attribute, so the row's own colors and dimming
// survive across the span). Matching runs on the visible text; the spans are
// mapped back into the styled string.
func (p *searchPattern) highlight(row string) string {
	visible, mp := visibleWithMap(row)
	spans := p.re.FindAllStringIndex(visible, -1)
	if len(spans) == 0 {
		return row
	}
	var b strings.Builder
	b.Grow(len(row) + len(spans)*10)
	prev := 0
	for _, sp := range spans {
		if sp[0] == sp[1] {
			continue // zero-width match (e.g. ^): nothing to paint
		}
		lo, hi := mp[sp[0]], mp[sp[1]]
		b.WriteString(row[prev:lo])
		b.WriteString("\x1b[7m")
		b.WriteString(row[lo:hi])
		b.WriteString("\x1b[27m")
		prev = hi
	}
	b.WriteString(row[prev:])
	return b.String()
}

// visibleWithMap strips ANSI escape sequences, returning the visible text and
// a byte-index map from each visible position (plus one-past-end) back into
// the styled string. Same escape walker as clipToWidth: CSI sequences
// run to their final byte; other escapes are two bytes.
func visibleWithMap(row string) (string, []int) {
	var visible strings.Builder
	visible.Grow(len(row))
	mp := make([]int, 0, len(row)+1)
	for i := 0; i < len(row); {
		if row[i] != '\x1b' {
			mp = append(mp, i)
			visible.WriteByte(row[i])
			i++
			continue
		}
		if i+1 >= len(row) {
			break
		}
		if row[i+1] == '[' {
			i += 2
			for i < len(row) {
				final := row[i]
				i++
				if final >= 0x40 && final <= 0x7e {
					break
				}
			}
			continue
		}
		i += 2
	}
	mp = append(mp, len(row))
	return visible.String(), mp
}
