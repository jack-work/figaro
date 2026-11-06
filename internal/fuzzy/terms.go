package fuzzy

import (
	"bytes"
	"strings"
	"unicode/utf8"

	"github.com/junegunn/fzf/src/util"
)

type termKind uint8

const (
	termFuzzy termKind = iota
	termExact
	termPrefix
	termSuffix
)

type Term struct {
	kind    termKind
	negate  bool
	onKey   bool
	onValue bool
	text    string
	lower   string
	lowerB  []byte
	runes   []rune
}

func Parse(query string) []Term {
	var out []Term
	for _, f := range strings.Fields(query) {
		t := Term{onKey: true, onValue: true}
		switch {
		case strings.HasPrefix(f, "k:"):
			t.onValue, f = false, f[2:]
		case strings.HasPrefix(f, "v:"):
			t.onKey, f = false, f[2:]
		}
		if strings.HasPrefix(f, "!") {
			t.negate, f = true, f[1:]
		}
		switch {
		case strings.HasPrefix(f, "'"):
			t.kind, f = termExact, f[1:]
		case strings.HasPrefix(f, "^"):
			t.kind, f = termPrefix, f[1:]
		case len(f) > 1 && strings.HasSuffix(f, "$"):
			t.kind, f = termSuffix, f[:len(f)-1]
		}
		if t.negate && t.kind == termFuzzy {
			t.kind = termExact
		}
		if f == "" {
			continue
		}
		t.text, t.lower = f, strings.ToLower(f)
		t.lowerB, t.runes = []byte(t.lower), []rune(t.lower)
		out = append(out, t)
	}
	return out
}

func (t Term) spans() bool { return strings.ContainsRune(t.text, '.') }

func containsLower(s []byte, lower string) bool {
	for _, c := range s {
		if c >= utf8.RuneSelf {
			return strings.Contains(strings.ToLower(string(s)), lower)
		}
	}
	n := len(lower)
	for i := 0; i+n <= len(s); i++ {
		j := 0
		for j < n {
			c := s[i+j]
			if 'A' <= c && c <= 'Z' {
				c += 'a' - 'A'
			}
			if c != lower[j] {
				break
			}
			j++
		}
		if j == n {
			return true
		}
	}
	return false
}

func (t Term) bestKey(m *Matcher, e *Entry) (seg, score int) {
	seg = -1
	for i := range e.Keys {
		text := Text{chars: util.ToChars(e.segment(i))}
		if got, ok := m.Match(&text, t.runes); ok && (seg < 0 || got.Score > score) {
			seg, score = i, got.Score
		}
	}
	return seg, score
}

func (t Term) matchKey(m *Matcher, e *Entry) (score int, ok, spread bool) {
	if !t.onKey {
		return 0, false, false
	}
	if t.kind == termFuzzy {
		if t.spans() {
			got, found := m.Match(&e.key, t.runes)
			return got.Score, found, found && !strings.Contains(strings.ToLower(e.Path), t.lower)
		}
		seg, score := t.bestKey(m, e)
		if seg < 0 {
			return 0, false, false
		}
		return score, true, !containsLower(e.segment(seg), t.lower)
	}
	plain := e.Path
	low := strings.ToLower(plain)
	switch t.kind {
	case termExact:
		ok = strings.Contains(low, t.lower)
	case termPrefix:
		ok = strings.HasPrefix(low, t.lower)
	case termSuffix:
		ok = strings.HasSuffix(low, t.lower)
	}
	if ok {
		score = 16 * utf8.RuneCountInString(t.text)
	}
	return score, ok, false
}

type valueHit struct {
	ok     bool
	spread bool
	at     int
	score  int
}

func (t Term) matchValue(m *Matcher, e *Entry) valueHit {
	if !t.onValue {
		return valueHit{}
	}
	if t.kind != termFuzzy {
		at := t.valueAt(e.value)
		return valueHit{ok: at >= 0, at: at, score: 16 * len(t.runes)}
	}
	text := e.valueText()
	got, ok := m.ScanLower(&text, t.runes)
	if !ok {
		return valueHit{}
	}
	at := got.Start
	if e.wide != nil {
		at = runeOffset(e.value, at)
	}
	return valueHit{ok: true, spread: !bytes.Contains(e.value, t.lowerB), at: at, score: got.Score}
}

func runeOffset(s []byte, runes int) int {
	at := 0
	for ; runes > 0 && at < len(s); runes-- {
		_, n := utf8.DecodeRune(s[at:])
		at += n
	}
	return at
}

func (t Term) valueAt(lowerValue []byte) int {
	switch t.kind {
	case termPrefix:
		if bytes.HasPrefix(lowerValue, t.lowerB) {
			return 0
		}
		return -1
	case termSuffix:
		if bytes.HasSuffix(lowerValue, t.lowerB) {
			return len(lowerValue) - len(t.lowerB)
		}
		return -1
	}
	return bytes.Index(lowerValue, t.lowerB)
}

func (t Term) valueMarks(m *Matcher, text string, from int) []int {
	if from < 0 || from > len(text) {
		return nil
	}
	tail := text[from:]
	if t.kind == termFuzzy {
		chars := Text{chars: util.ToChars([]byte(tail))}
		pos := m.Positions(&chars, t.runes)
		out := make([]int, 0, len(pos))
		if chars.chars.IsBytes() {
			for _, p := range pos {
				out = append(out, from+p)
			}
			return out
		}
		for _, p := range pos {
			out = append(out, from+runeOffset([]byte(tail), p))
		}
		return out
	}
	low := strings.ToLower(tail)
	var out []int
	for at := 0; at < len(low); {
		i := strings.Index(low[at:], t.lower)
		if i < 0 {
			break
		}
		for k := range len(t.lower) {
			out = append(out, from+at+i+k)
		}
		at += i + len(t.lower)
	}
	return out
}

func (t Term) keyPositions(m *Matcher, e *Entry) []int {
	if !t.onKey || t.negate {
		return nil
	}
	if t.kind == termFuzzy {
		if t.spans() {
			return m.Positions(&e.key, t.runes)
		}
		seg, _ := t.bestKey(m, e)
		if seg < 0 {
			return nil
		}
		text := Text{chars: util.ToChars(e.segment(seg))}
		out := m.Positions(&text, t.runes)
		at := e.segmentRunes(seg)
		for i := range out {
			out[i] += at
		}
		return out
	}
	plain := e.Path
	low := strings.ToLower(plain)
	at := -1
	switch t.kind {
	case termExact:
		at = strings.Index(low, t.lower)
	case termPrefix:
		if strings.HasPrefix(low, t.lower) {
			at = 0
		}
	case termSuffix:
		if strings.HasSuffix(low, t.lower) {
			at = len(low) - len(t.lower)
		}
	}
	if at < 0 {
		return nil
	}
	first := utf8.RuneCountInString(plain[:at])
	out := make([]int, 0, len(t.runes))
	for i := range t.runes {
		out = append(out, first+i)
	}
	return out
}

func narrows(prev, next []Term) bool {
	if len(next) < len(prev) {
		return false
	}
	for i, p := range prev {
		n := next[i]
		if p.kind != n.kind || p.negate != n.negate || p.onKey != n.onKey || p.onValue != n.onValue {
			return false
		}
		if p.negate || p.kind == termSuffix {
			if n.lower != p.lower {
				return false
			}
			continue
		}
		if !strings.HasPrefix(n.lower, p.lower) || p.kind == termFuzzy && p.spans() != n.spans() {
			return false
		}
	}
	return true
}
